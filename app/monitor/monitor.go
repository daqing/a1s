// Package monitor implements the health monitor process: it marks lost
// workers, requeues their containers and enforces restart policies
// (docs/ROADMAP.md, Phase 5).
package monitor

import (
	"context"
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

// runLoop checks heartbeat freshness on every tick until ctx is canceled.
func runLoop(ctx context.Context, timeout, interval time.Duration) {
	check(timeout)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check(timeout)
		}
	}
}

// check counts active workers whose heartbeat is older than the timeout.
// Marking them lost lands in T5.2 — the loop only observes for now.
func check(timeout time.Duration) {
	cutoff := time.Now().Add(-timeout)

	b := buildingsql.SelectColumns("count(*)").
		From("workers").
		Where(buildingsql.AllOf(
			buildingsql.Eq("status", models.WorkerActive),
			buildingsql.Lt("last_heartbeat_at", cutoff),
		))

	stale, err := repo.Count(repo.CurrentDB(), b)
	if err != nil {
		log.Printf("check failed: %v", err)
		return
	}

	if stale > 0 {
		log.Printf("%d active worker(s) past the heartbeat timeout (marking not implemented yet)", stale)
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
