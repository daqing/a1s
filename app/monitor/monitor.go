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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/daqing/a1s/app/models"
	"github.com/daqing/airway/lib/repo"
	buildingsql "github.com/daqing/airway/lib/sql"
	"github.com/daqing/airway/lib/utils"
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

// runLoop recovers the system on every tick until ctx is canceled.
func runLoop(ctx context.Context, timeout, interval time.Duration) {
	recoverAll(timeout)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			recoverAll(timeout)
		}
	}
}

// recoverAll is one full monitor pass: mark stale workers lost, migrate
// their runtime containers back to pending, and requeue failed containers
// per policy. Every transition is version-guarded with conflicts skipped,
// so two monitor instances running the same pass produce single effects —
// asserted by TestRecoverAllConcurrentSingleEffects.
func recoverAll(timeout time.Duration) {
	markLostWorkers(timeout)
	migrateOffLostWorkers()
	restartFailedContainers()
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
			// a worker that never heartbeated (NULL) counts as stale too
			buildingsql.AnyOf(
				buildingsql.IsNull("last_heartbeat_at"),
				buildingsql.Lt("last_heartbeat_at", cutoff),
			),
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

// restartProbe is the slim projection the failed-container query scans.
type restartProbe struct {
	ID            int64  `db:"id"`
	Version       int64  `db:"version"`
	RestartPolicy string `db:"restart_policy"`
	RestartCount  int64  `db:"restart_count"`
}

// restartFailedContainers requeues failed containers whose restart policy
// calls for it. Each requeue resets the container to pending with no
// worker, so the normal scheduling pipeline handles the restart; the
// version lock keeps concurrent monitors from double-requeuing.
func restartFailedContainers() {
	probes, err := repo.Find[restartProbe](repo.CurrentDB(),
		buildingsql.SelectColumns("id", "version", "restart_policy", "restart_count").
			From("containers").
			Where(buildingsql.Eq("status", models.ContainerFailed)))
	if err != nil {
		log.Printf("restart: query failed containers: %v", err)
		return
	}

	for _, probe := range probes {
		if !shouldRestart(probe.RestartPolicy, probe.RestartCount) {
			continue
		}

		var t models.Container

		affected, err := repo.UpdateAffected(repo.CurrentDB(),
			buildingsql.UpdateTable(buildingsql.TableFor(t)).Set(buildingsql.H{
				"status":        models.ContainerPending,
				"worker_id":     nil,
				"scheduled_at":  nil,
				"restart_count": probe.RestartCount + 1,
				"version":       buildingsql.Op(buildingsql.Column("version"), "+", 1),
				"updated_at":    buildingsql.Func("now"),
			}).Where(buildingsql.AllOf(
				buildingsql.FieldEq(buildingsql.FieldFor(t, "id"), probe.ID),
				buildingsql.FieldEq(buildingsql.FieldFor(t, "version"), probe.Version),
			)))
		if err != nil {
			log.Printf("restart: container %d failed: %v", probe.ID, err)
			continue
		}

		if affected == 0 {
			continue // another monitor instance requeued it first
		}

		log.Printf("container %d failed (policy %s), requeued for restart #%d",
			probe.ID, probe.RestartPolicy, probe.RestartCount+1)
	}
}

// shouldRestart decides whether a failed container restarts under its
// policy. Restart counts only gate on-failure's optional :N suffix.
func shouldRestart(policy string, restartCount int64) bool {
	base, suffix, hasSuffix := strings.Cut(policy, ":")

	switch base {
	case "always", "unless-stopped":
		return true
	case "on-failure":
		if !hasSuffix {
			return true
		}

		max, err := strconv.ParseInt(suffix, 10, 64)
		if err != nil {
			return true // validated at creation; stay permissive
		}

		return restartCount < max
	default: // "no"
		return false
	}
}

// migrateProbe is the slim projection the orphaned-container query scans.
type migrateProbe struct {
	ID      int64 `db:"id"`
	Version int64 `db:"version"`
}

// migrateOffLostWorkers resets the scheduled and running containers of lost
// workers back to pending with no worker, so the scheduler reschedules them
// onto survivors. Desired states are left alone: a stopped or failed
// container on a lost worker must not be restarted by the takeover. Each
// reset is version-guarded, so concurrent monitors never double-migrate.
func migrateOffLostWorkers() {
	lost, err := repo.Find[workerProbe](repo.CurrentDB(),
		buildingsql.SelectColumns("id", "version").
			From("workers").
			Where(buildingsql.Eq("status", models.WorkerLost)))
	if err != nil {
		log.Printf("migrate: query lost workers failed: %v", err)
		return
	}

	if len(lost) == 0 {
		return
	}

	lostIDs := make([]int64, 0, len(lost))
	for _, w := range lost {
		lostIDs = append(lostIDs, w.ID)
	}

	orphaned, err := repo.Find[migrateProbe](repo.CurrentDB(),
		buildingsql.SelectColumns("id", "version").
			From("containers").
			Where(buildingsql.AllOf(
				buildingsql.In("worker_id", lostIDs),
				buildingsql.In("status", []string{models.ContainerScheduled, models.ContainerRunning}),
			)))
	if err != nil {
		log.Printf("migrate: query containers failed: %v", err)
		return
	}

	for _, probe := range orphaned {
		var t models.Container

		affected, err := repo.UpdateAffected(repo.CurrentDB(),
			buildingsql.UpdateTable(buildingsql.TableFor(t)).Set(buildingsql.H{
				"status":       models.ContainerPending,
				"worker_id":    nil,
				"scheduled_at": nil,
				"version":      buildingsql.Op(buildingsql.Column("version"), "+", 1),
				"updated_at":   buildingsql.Func("now"),
			}).Where(buildingsql.AllOf(
				buildingsql.FieldEq(buildingsql.FieldFor(t, "id"), probe.ID),
				buildingsql.FieldEq(buildingsql.FieldFor(t, "version"), probe.Version),
			)))
		if err != nil {
			log.Printf("migrate: container %d failed: %v", probe.ID, err)
			continue
		}

		if affected == 0 {
			continue // another monitor instance migrated it first
		}

		log.Printf("container %d orphaned by a lost worker, reset to pending", probe.ID)
	}
}
