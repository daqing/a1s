package scheduler

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/daqing/airway/lib/repo"
)

func setupTestDB(t *testing.T) {
	t.Helper()

	dsn := os.Getenv("A1S_TEST_DSN")
	if dsn == "" {
		t.Skip("A1S_TEST_DSN not set; skipping database-backed test")
	}

	if _, err := repo.SetupDB(dsn); err != nil {
		t.Fatalf("setup test database: %v", err)
	}
}

// seedWorkerRow inserts a worker (status active unless told otherwise).
func seedWorkerRow(t *testing.T, name, status string) int64 {
	t.Helper()

	var id int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`INSERT INTO workers (name, address, status) VALUES ($1, '10.0.0.1:9001', $2) RETURNING id`,
		name, status).Scan(&id); err != nil {
		t.Fatalf("seed worker: %v", err)
	}

	return id
}

// seedContainerRow inserts a container row in the given status.
func seedContainerRow(t *testing.T, name, image, status string) int64 {
	t.Helper()

	var id int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`INSERT INTO containers (name, image, status) VALUES ($1, $2, $3) RETURNING id`,
		name, image, status).Scan(&id); err != nil {
		t.Fatalf("seed container: %v", err)
	}

	return id
}

// quarantineOtherWorkers parks every worker not in keep as lost, restoring
// the prior statuses on cleanup, so selection tests see only their fixtures
// even against a shared development database.
func quarantineOtherWorkers(t *testing.T, keep ...int64) {
	t.Helper()

	type prior struct {
		id     int64
		status string
	}

	query := `SELECT id, status FROM workers WHERE true`
	args := []any{}
	for _, id := range keep {
		query += fmt.Sprintf(" AND id <> $%d", len(args)+1)
		args = append(args, id)
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
		apitestExec(t, `UPDATE workers SET status = 'lost' WHERE id = $1`, p.id)
	}

	t.Cleanup(func() {
		for _, p := range others {
			repo.CurrentDB().Conn().Exec(`UPDATE workers SET status = $2 WHERE id = $1`, p.id, p.status)
		}
	})
}

func TestScheduleRoundAssignsPendingContainer(t *testing.T) {
	setupTestDB(t)

	suffix := time.Now().UnixNano()
	workerID := seedWorkerRow(t, fmt.Sprintf("t43-worker-%d", suffix), "active")
	containerID := seedContainerRow(t, fmt.Sprintf("t43-assign-%d", suffix), "nginx", "pending")

	t.Cleanup(func() {
		apitestExec(t, `DELETE FROM commands WHERE container_id = $1`, containerID)
		apitestExec(t, `DELETE FROM containers WHERE id = $1`, containerID)
		apitestExec(t, `DELETE FROM workers WHERE id = $1`, workerID)
	})
	quarantineOtherWorkers(t, workerID)

	scheduleRound()

	var status string
	var workerIDAssigned int64
	var scheduledAtValid bool
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT status, worker_id, scheduled_at IS NOT NULL FROM containers WHERE id = $1`,
		containerID).Scan(&status, &workerIDAssigned, &scheduledAtValid); err != nil {
		t.Fatalf("query container: %v", err)
	}

	if status != "scheduled" || workerIDAssigned != workerID || !scheduledAtValid {
		t.Fatalf("expected scheduled on worker %d with scheduled_at, got %q worker %d", workerID, status, workerIDAssigned)
	}

	var commands int
	var payloadValid bool
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT count(*), bool_or(payload->>'id' = $2 AND action = 'start')
		 FROM commands WHERE container_id = $1`, containerID, fmt.Sprint(containerID)).Scan(&commands, &payloadValid); err != nil {
		t.Fatalf("query commands: %v", err)
	}

	if commands != 1 || !payloadValid {
		t.Fatalf("expected exactly one start command with the container id in its payload, got %d (%v)", commands, payloadValid)
	}
}

func TestScheduleRoundRaceSingleWinner(t *testing.T) {
	setupTestDB(t)

	suffix := time.Now().UnixNano()
	workerID := seedWorkerRow(t, fmt.Sprintf("t43-race-worker-%d", suffix), "active")
	containerID := seedContainerRow(t, fmt.Sprintf("t43-race-%d", suffix), "nginx", "pending")

	t.Cleanup(func() {
		apitestExec(t, `DELETE FROM commands WHERE container_id = $1`, containerID)
		apitestExec(t, `DELETE FROM containers WHERE id = $1`, containerID)
		apitestExec(t, `DELETE FROM workers WHERE id = $1`, workerID)
	})
	quarantineOtherWorkers(t, workerID)

	// two scheduler instances racing the same pending row
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			scheduleRound()
		}()
	}
	wg.Wait()

	var status string
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT status FROM containers WHERE id = $1`, containerID).Scan(&status); err != nil {
		t.Fatalf("query container: %v", err)
	}
	if status != "scheduled" {
		t.Fatalf("expected the container scheduled, got %q", status)
	}

	var commands int
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT count(*) FROM commands WHERE container_id = $1`, containerID).Scan(&commands); err != nil {
		t.Fatalf("query commands: %v", err)
	}

	if commands != 1 {
		t.Fatalf("expected exactly one queued start command, got %d", commands)
	}
}

func TestScheduleRoundNoActiveWorkers(t *testing.T) {
	setupTestDB(t)

	suffix := time.Now().UnixNano()
	containerID := seedContainerRow(t, fmt.Sprintf("t43-noworker-%d", suffix), "nginx", "pending")

	t.Cleanup(func() {
		apitestExec(t, `DELETE FROM containers WHERE id = $1`, containerID)
	})
	quarantineOtherWorkers(t)

	scheduleRound()

	var status string
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT status FROM containers WHERE id = $1`, containerID).Scan(&status); err != nil {
		t.Fatalf("query container: %v", err)
	}

	if status != "pending" {
		t.Fatalf("expected the container to stay pending without workers, got %q", status)
	}
}
