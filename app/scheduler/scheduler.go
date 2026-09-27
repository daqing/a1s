// Package scheduler implements the scheduling loop process: it watches for
// pending containers and assigns them to workers (Phase 4).
package scheduler

import (
	"context"
	"encoding/json"
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
	buildingsql "github.com/daqing/airway/lib/sql"
	"github.com/daqing/airway/lib/utils"
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

// runLoop schedules pending containers on every tick until ctx is canceled.
func runLoop(ctx context.Context, interval time.Duration) {
	scheduleRound()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scheduleRound()
		}
	}
}

// scheduleRound assigns pending containers to workers. Each assignment goes
// through the version lock, so concurrent scheduler instances can never
// double-assign; a lost race is skipped and the container waits for the
// next round.
func scheduleRound() {
	pending, err := pendingContainers()
	if err != nil {
		log.Printf("schedule round failed: %v", err)
		return
	}

	if len(pending) == 0 {
		return
	}

	for _, c := range pending {
		// re-select per container: every assignment changes the load
		worker, err := selectWorker()
		if err != nil {
			log.Printf("schedule round failed: %v", err)
			return
		}

		if worker == nil {
			log.Printf("%d pending container(s) waiting, but no active worker is registered", len(pending))
			return
		}

		assigned, err := assign(c, worker)
		if err != nil {
			log.Printf("assign container %d failed: %v", c.ID, err)
			continue
		}

		if !assigned {
			log.Printf("container %d: lost the assignment race, skipping", c.ID)
			continue
		}

		if err := queueStartCommand(c, worker); err != nil {
			// the row is scheduled but its start command is missing;
			// reconciliation (Phase 6) requeues such strays
			log.Printf("container %d assigned to worker %d but queueing start failed: %v", c.ID, worker.ID, err)
			continue
		}

		log.Printf("container %d (%s) scheduled on worker %d (%s)", c.ID, c.Name, worker.ID, worker.Name)
	}
}

// assign flips one pending container to scheduled on the given worker: the
// version and the worker_id IS NULL guards together make double assignment
// impossible; rows-affected = 0 means another scheduler instance won.
func assign(c *models.Container, worker *models.Worker) (bool, error) {
	var t models.Container

	b := buildingsql.UpdateTable(buildingsql.TableFor(t)).Set(buildingsql.H{
		"worker_id":    worker.ID,
		"status":       models.ContainerScheduled,
		"scheduled_at": buildingsql.Func("now"),
		"version":      buildingsql.Op(buildingsql.Column("version"), "+", 1),
		"updated_at":   buildingsql.Func("now"),
	}).Where(buildingsql.AllOf(
		buildingsql.FieldEq(buildingsql.FieldFor(t, "id"), c.ID),
		buildingsql.FieldEq(buildingsql.FieldFor(t, "version"), c.Version),
		buildingsql.IsNull("worker_id"),
	))

	affected, err := repo.UpdateAffected(repo.CurrentDB(), b)
	if err != nil {
		return false, err
	}

	return affected == 1, nil
}

// queueStartCommand enqueues the start command the winning worker executes.
func queueStartCommand(c *models.Container, worker *models.Worker) error {
	payload, err := json.Marshal(map[string]any{
		"id":      c.ID,
		"image":   c.Image,
		"name":    c.Name,
		"command": c.Command,
		"args":    c.Args,
		"env":     c.Env,
	})
	if err != nil {
		return err
	}

	_, err = repo.CreateFrom[models.Command](buildingsql.H{
		"worker_id":    worker.ID,
		"container_id": c.ID,
		"action":       models.CommandStart,
		"payload":      models.JSONB(payload),
	})

	return err
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
