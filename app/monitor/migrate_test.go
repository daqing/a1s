package monitor

import (
	"fmt"
	"sync"
	"testing"

	"github.com/daqing/a1s/app/models"
	"github.com/daqing/airway/lib/repo"
)

// seedLostFixtureWorker inserts a worker with the given status.
func seedLostFixtureWorker(t *testing.T, name, status string) int64 {
	t.Helper()

	var id int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`INSERT INTO workers (name, address, status) VALUES ($1, '10.0.0.1:9001', $2) RETURNING id`,
		name, status).Scan(&id); err != nil {
		t.Fatalf("seed worker: %v", err)
	}

	return id
}

// seedLostFixtureContainer inserts a container in the given status on a
// worker.
func seedLostFixtureContainer(t *testing.T, name, status string, workerID *int64) int64 {
	t.Helper()

	var id int64
	if workerID == nil {
		if err := repo.CurrentDB().Conn().QueryRow(
			`INSERT INTO containers (name, image, status) VALUES ($1, 'nginx', $2) RETURNING id`,
			name, status).Scan(&id); err != nil {
			t.Fatalf("seed container: %v", err)
		}
		return id
	}

	if err := repo.CurrentDB().Conn().QueryRow(
		`INSERT INTO containers (name, image, status, worker_id) VALUES ($1, 'nginx', $2, $3) RETURNING id`,
		name, status, *workerID).Scan(&id); err != nil {
		t.Fatalf("seed container: %v", err)
	}

	return id
}

// quarantineMonitorWorkers parks every worker not in keep as lost,
// restoring prior statuses on cleanup (shared dev database isolation).
func quarantineMonitorWorkers(t *testing.T, keep ...int64) {
	t.Helper()

	query := `SELECT id, status FROM workers WHERE true`
	args := []any{}
	for _, id := range keep {
		query += fmt.Sprintf(" AND id <> $%d", len(args)+1)
		args = append(args, id)
	}

	type prior struct {
		id     int64
		status string
	}

	rows, err := repo.CurrentDB().Conn().Query(query, args...)
	if err != nil {
		t.Fatalf("list other workers: %v", err)
	}

	var others []prior
	for rows.Next() {
		var p prior
		if err := rows.Scan(&p.id, &p.status); err != nil {
			rows.Close()
			t.Fatalf("scan other worker: %v", err)
		}
		others = append(others, p)
	}
	rows.Close()

	for _, p := range others {
		if _, err := repo.CurrentDB().Conn().Exec(`UPDATE workers SET status = 'lost' WHERE id = $1`, p.id); err != nil {
			t.Fatalf("quarantine worker: %v", err)
		}
	}

	t.Cleanup(func() {
		for _, p := range others {
			repo.CurrentDB().Conn().Exec(`UPDATE workers SET status = $2 WHERE id = $1`, p.id, p.status)
		}
	})
}

// seedOrphanFixture inserts a lost worker and an active worker, plus one
// container per state on each. It returns the container ids on the lost
// worker keyed by state and the active worker's running container id.
func seedOrphanFixture(t *testing.T) (orphaned map[string]int64, survivorRunning int64) {
	t.Helper()

	setupMonitorDB(t)

	suffix := fmt.Sprintf("%d", timeNowUnix())
	lostID := seedLostFixtureWorker(t, "t53-lost-"+suffix, "lost")
	survivorID := seedLostFixtureWorker(t, "t53-survivor-"+suffix, "active")
	quarantineMonitorWorkers(t, lostID, survivorID)

	orphaned = map[string]int64{}
	for _, status := range []string{"scheduled", "running", "stopped", "failed"} {
		orphaned[status] = seedLostFixtureContainer(t, "t53-orphan-"+status+"-"+suffix, status, &lostID)
	}

	survivorRunning = seedLostFixtureContainer(t, "t53-surviving-"+suffix, "running", &survivorID)

	t.Cleanup(func() {
		repo.CurrentDB().Conn().Exec(
			`DELETE FROM containers WHERE worker_id IN ($1, $2) OR id = $3`,
			lostID, survivorID, survivorRunning)
		repo.CurrentDB().Conn().Exec(
			`DELETE FROM workers WHERE id IN ($1, $2)`, lostID, survivorID)
	})

	return orphaned, survivorRunning
}

func TestMigrateResetsRuntimeContainersOfLostWorkers(t *testing.T) {
	orphaned, survivorRunning := seedOrphanFixture(t)

	migrateOffLostWorkers()

	// scheduled and running containers return to pending with no worker
	for _, status := range []string{"scheduled", "running"} {
		var statusNow string
		var workerSet bool
		var scheduledAtSet bool
		if err := repo.CurrentDB().Conn().QueryRow(
			`SELECT status, worker_id IS NOT NULL, scheduled_at IS NOT NULL FROM containers WHERE id = $1`,
			orphaned[status]).Scan(&statusNow, &workerSet, &scheduledAtSet); err != nil {
			t.Fatalf("query orphan container: %v", err)
		}

		if statusNow != models.ContainerPending || workerSet || scheduledAtSet {
			t.Fatalf("expected the %s container reset to pending, got %q worker=%v scheduled_at=%v",
				status, statusNow, workerSet, scheduledAtSet)
		}
	}

	// desired states on the lost worker are left alone
	for _, status := range []string{"stopped", "failed"} {
		var statusNow string
		if err := repo.CurrentDB().Conn().QueryRow(
			`SELECT status FROM containers WHERE id = $1`, orphaned[status]).Scan(&statusNow); err != nil {
			t.Fatalf("query desired container: %v", err)
		}

		if statusNow != status {
			t.Fatalf("expected the %s container untouched, got %q", status, statusNow)
		}
	}

	// the surviving worker's container is not migrated
	var statusNow string
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT status FROM containers WHERE id = $1`, survivorRunning).Scan(&statusNow); err != nil {
		t.Fatalf("query surviving container: %v", err)
	}

	if statusNow != models.ContainerRunning {
		t.Fatalf("expected the surviving container running, got %q", statusNow)
	}
}

func TestMigrateConcurrentSingleMigration(t *testing.T) {
	orphaned, _ := seedOrphanFixture(t)

	var versionBefore int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT version FROM containers WHERE id = $1`, orphaned["running"]).Scan(&versionBefore); err != nil {
		t.Fatalf("query version: %v", err)
	}

	// two monitor instances racing the same orphaned container
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			migrateOffLostWorkers()
		}()
	}
	wg.Wait()

	var statusNow string
	var versionAfter int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT status, version FROM containers WHERE id = $1`,
		orphaned["running"]).Scan(&statusNow, &versionAfter); err != nil {
		t.Fatalf("query container: %v", err)
	}

	if statusNow != models.ContainerPending {
		t.Fatalf("expected the container pending, got %q", statusNow)
	}
	if versionAfter != versionBefore+1 {
		t.Fatalf("expected exactly one migration (version %d), got %d", versionBefore+1, versionAfter)
	}
}
