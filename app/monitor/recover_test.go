package monitor

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/daqing/a1s/app/models"
	"github.com/daqing/airway/lib/repo"
)

// TestRecoverAllConcurrentSingleEffects drives two monitor instances
// through one worker-loss event concurrently and asserts every recovery
// effect happened exactly once: the worker marked lost, its runtime
// container migrated, and its failed container requeued.
func TestRecoverAllConcurrentSingleEffects(t *testing.T) {
	setupMonitorDB(t)

	suffix := fmt.Sprintf("%d", timeNowUnix())

	// the worker that will be lost
	lostID := seedLostFixtureWorker(t, "t55-lost-"+suffix, "active")
	quarantineMonitorWorkers(t, lostID)

	// its runtime container (will migrate) and its failed container (will
	// requeue), both on the doomed worker
	var runtimeVersion int64
	runtimeID := seedLostFixtureContainer(t, "t55-runtime-"+suffix, "running", &lostID)
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT version FROM containers WHERE id = $1`, runtimeID).Scan(&runtimeVersion); err != nil {
		t.Fatalf("query runtime version: %v", err)
	}

	var failedVersion int64
	failedID := seedLostFixtureContainer(t, "t55-failed-"+suffix, "failed", &lostID)
	if _, err := repo.CurrentDB().Conn().Exec(
		`UPDATE containers SET restart_policy = 'on-failure' WHERE id = $1`, failedID); err != nil {
		t.Fatalf("set restart policy: %v", err)
	}
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT version FROM containers WHERE id = $1`, failedID).Scan(&failedVersion); err != nil {
		t.Fatalf("query failed version: %v", err)
	}

	t.Cleanup(func() {
		repo.CurrentDB().Conn().Exec(
			`DELETE FROM containers WHERE id IN ($1, $2)`, runtimeID, failedID)
		repo.CurrentDB().Conn().Exec(`DELETE FROM workers WHERE id = $1`, lostID)
	})

	// the worker-loss event: its heartbeats go stale
	if _, err := repo.CurrentDB().Conn().Exec(
		`UPDATE workers SET last_heartbeat_at = now() - interval '1 hour' WHERE id = $1`, lostID); err != nil {
		t.Fatalf("age the heartbeat: %v", err)
	}

	// two monitor instances run the full recovery pass concurrently
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			recoverAll(15 * time.Second)
		}()
	}
	wg.Wait()

	// worker marked lost exactly once
	var workerStatus string
	var workerVersion int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT status, version FROM workers WHERE id = $1`, lostID).Scan(&workerStatus, &workerVersion); err != nil {
		t.Fatalf("query worker: %v", err)
	}
	if workerStatus != models.WorkerLost {
		t.Fatalf("expected the worker lost, got %q", workerStatus)
	}
	if workerVersion != 1 {
		t.Fatalf("expected exactly one marking (version 1), got %d", workerVersion)
	}

	// runtime container migrated exactly once
	var runtimeStatus string
	var runtimeVersionAfter int64
	var workerSet bool
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT status, version, worker_id IS NOT NULL FROM containers WHERE id = $1`,
		runtimeID).Scan(&runtimeStatus, &runtimeVersionAfter, &workerSet); err != nil {
		t.Fatalf("query runtime container: %v", err)
	}
	if runtimeStatus != models.ContainerPending || workerSet {
		t.Fatalf("expected the runtime container pending with no worker, got %q worker=%v", runtimeStatus, workerSet)
	}
	if runtimeVersionAfter != runtimeVersion+1 {
		t.Fatalf("expected exactly one migration (version %d), got %d", runtimeVersion+1, runtimeVersionAfter)
	}

	// failed container requeued exactly once
	var failedStatus string
	var failedCount int64
	var failedVersionAfter int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT status, restart_count, version FROM containers WHERE id = $1`,
		failedID).Scan(&failedStatus, &failedCount, &failedVersionAfter); err != nil {
		t.Fatalf("query failed container: %v", err)
	}
	if failedStatus != models.ContainerPending {
		t.Fatalf("expected the failed container pending, got %q", failedStatus)
	}
	if failedCount != 1 {
		t.Fatalf("expected restart_count 1 after a single requeue, got %d", failedCount)
	}
	if failedVersionAfter != failedVersion+1 {
		t.Fatalf("expected exactly one requeue (version %d), got %d", failedVersion+1, failedVersionAfter)
	}
}
