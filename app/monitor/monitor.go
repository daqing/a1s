// Package monitor implements the health monitor process: it marks lost
// workers, requeues their containers and enforces restart policies
// (docs/ROADMAP.md, Phase 5).
package monitor

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/daqing/a1s/app/models"
	"github.com/daqing/airway/lib/repo"
	"github.com/daqing/airway/lib/utils"
	buildingsql "github.com/daqing/airway/lib/sql"
)

const (
	// defaultTimeout matches A1S_HEARTBEAT_TIMEOUT in .env.example.
	defaultTimeout = 15 * time.Second
	// defaultInterval matches A1S_MONITOR_INTERVAL in .env.example.
	defaultInterval = 5 * time.Second
)

// Main runs the monitor process; it returns the process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("monitor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: a1s monitor")
		return 2
	}

	timeout, ok := parseDuration(os.Getenv("A1S_HEARTBEAT_TIMEOUT"), defaultTimeout)
	if !ok {
		log.Printf("invalid A1S_HEARTBEAT_TIMEOUT %q, using %s", os.Getenv("A1S_HEARTBEAT_TIMEOUT"), defaultTimeout)
	}

	interval, ok := parseDuration(os.Getenv("A1S_MONITOR_INTERVAL"), defaultInterval)
	if !ok {
		log.Printf("invalid A1S_MONITOR_INTERVAL %q, using %s", os.Getenv("A1S_MONITOR_INTERVAL"), defaultInterval)
	}

	if dsn := utils.GetEnvMulti("A1S_DSN", "AIRWAY_DSN", "DSN"); dsn != "" {
		if _, err := repo.SetupDB(dsn); err != nil {
			log.Printf("database setup failed: %v", err)
			return 3
		}
	} else {
		log.Println("A1S_DSN is not set; the monitor reads system state from PostgreSQL")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("monitor: heartbeat timeout=%s interval=%s", timeout, interval)

	runLoop(ctx, timeout, interval)

	log.Printf("monitor shutting down")
	return 0
}

// runLoop marks stale workers lost on every tick until ctx is canceled.
func runLoop(ctx context.Context, timeout, interval time.Duration) {
	markLostWorkers(timeout)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			markLostWorkers(timeout)
		}
	}
}

// workerProbe is the slim projection the stale-worker query scans.
type workerProbe struct {
	ID      int64 `db:"id"`
	Version int64 `db:"version"`
}

// markLostWorkers flips active workers whose heartbeat is older than the
// timeout to lost. Each transition goes through the version lock, so two
// monitor instances never double-mark: a conflict means the other instance
// already won and is skipped.
func markLostWorkers(timeout time.Duration) {
	cutoff := time.Now().Add(-timeout)

	b := buildingsql.SelectColumns("id", "version").
		From("workers").
		Where(buildingsql.AllOf(
			buildingsql.Eq("status", models.WorkerActive),
			buildingsql.Lt("last_heartbeat_at", cutoff),
		))

	rows, err := repo.Find[workerProbe](repo.CurrentDB(), b)
	if err != nil {
		log.Printf("mark lost workers: query failed: %v", err)
		return
	}

	for _, probe := range rows {
		_, err := models.UpdateWhereVersion[models.Worker](probe.ID, probe.Version, buildingsql.H{
			"status": models.WorkerLost,
		})
		if err != nil {
			if errors.Is(err, models.ErrVersionConflict) {
				continue // the other monitor instance marked it first
			}

			log.Printf("mark lost workers: worker %d failed: %v", probe.ID, err)
			continue
		}

		log.Printf("worker %d marked lost (heartbeat older than %s)", probe.ID, timeout)
	}
}

// parseDuration validates a duration env value, falling back to the
// default when unset, malformed or non-positive.
func parseDuration(raw string, fallback time.Duration) (time.Duration, bool) {
	if raw == "" {
		return fallback, true
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return fallback, false
	}

	return parsed, true
}
