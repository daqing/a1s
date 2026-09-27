// Package scheduler implements the scheduling loop process: it watches for
// pending containers and assigns them to workers (Phase 4).
package scheduler

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

// defaultInterval matches A1S_SCHEDULER_INTERVAL in .env.example.
const defaultInterval = 3 * time.Second

// Main runs the scheduler process; it returns the process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("scheduler", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: a1s scheduler")
		return 2
	}

	interval := defaultInterval
	if raw := os.Getenv("A1S_SCHEDULER_INTERVAL"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			log.Printf("invalid A1S_SCHEDULER_INTERVAL %q, using %s", raw, defaultInterval)
		} else {
			interval = parsed
		}
	}

	if dsn := utils.GetEnvMulti("A1S_DSN", "AIRWAY_DSN", "DSN"); dsn != "" {
		if _, err := repo.SetupDB(dsn); err != nil {
			log.Printf("database setup failed: %v", err)
			return 3
		}
	} else {
		log.Println("A1S_DSN is not set; the scheduler reads system state from PostgreSQL")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("scheduler: interval=%s", interval)

	runLoop(ctx, interval)

	log.Printf("scheduler shutting down")
	return 0
}

// runLoop observes pending containers on every tick until ctx is canceled.
func runLoop(ctx context.Context, interval time.Duration) {
	observe()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			observe()
		}
	}
}

// observe logs the containers waiting for assignment. Assignment itself
// lands in T4.2/T4.3.
func observe() ([]*models.Container, error) {
	pending, err := pendingContainers()
	if err != nil {
		log.Printf("observe failed: %v", err)
		return nil, err
	}

	active, err := activeWorkers()
	if err != nil {
		log.Printf("observe failed: %v", err)
		return nil, err
	}

	if len(pending) == 0 {
		return nil, nil
	}

	if active == 0 {
		log.Printf("%d pending container(s) waiting, but no active worker is registered", len(pending))
		return pending, nil
	}

	for _, c := range pending {
		log.Printf("pending container %d (%s, image %s) ready for assignment", c.ID, c.Name, c.Image)
	}

	return pending, nil
}

// pendingContainers lists unassigned containers in FIFO order.
func pendingContainers() ([]*models.Container, error) {
	var t models.Container
	b := buildingsql.All(t).
		Where(buildingsql.AllOf(
			buildingsql.FieldEq(buildingsql.FieldFor(t, "status"), models.ContainerPending),
			buildingsql.IsNull("worker_id"),
		)).
		OrderBy("id ASC")

	return repo.Find[models.Container](repo.CurrentDB(), b)
}

// activeWorkers counts workers with a fresh registration.
func activeWorkers() (int64, error) {
	b := buildingsql.SelectColumns("count(*)").
		From("workers").
		Where(buildingsql.Eq("status", models.WorkerActive))

	return repo.Count(repo.CurrentDB(), b)
}
