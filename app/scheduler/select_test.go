package scheduler

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/daqing/airway/lib/repo"
)

// seedSelectionFixtures inserts workers and their container load, returning
// the worker ids keyed by role. It also quarantines every other worker in
// the database (marked lost, prior statuses restored on cleanup) so the
// selection sees only the fixture workers.
func seedSelectionFixtures(t *testing.T) (busyID, idleID, lostID int64) {
	t.Helper()

	suffix := time.Now().UnixNano()
	seedWorker := func(name string) int64 {
		var id int64
		if err := repo.CurrentDB().Conn().QueryRow(
			`INSERT INTO workers (name, address) VALUES ($1, '10.0.0.1:9001') RETURNING id`,
			name).Scan(&id); err != nil {
			t.Fatalf("seed worker: %v", err)
		}
		return id
	}

	busyID = seedWorker(fmt.Sprintf("t42-busy-%d", suffix))
	idleID = seedWorker(fmt.Sprintf("t42-idle-%d", suffix))
	lostID = seedWorker(fmt.Sprintf("t42-lost-%d", suffix))

	t.Cleanup(func() {
		repo.CurrentDB().Conn().Exec(
			`DELETE FROM containers WHERE worker_id IN ($1, $2, $3)`, busyID, idleID, lostID)
		repo.CurrentDB().Conn().Exec(
			`DELETE FROM workers WHERE id IN ($1, $2, $3)`, busyID, idleID, lostID)
	})

	// quarantine: remember the prior status of every other worker, park them
	// as lost for the duration of the test, restore afterwards
	type prior struct {
		id     int64
		status string
	}
	var others []prior
	rows, err := repo.CurrentDB().Conn().Query(
		`SELECT id, status FROM workers WHERE id NOT IN ($1, $2, $3)`, busyID, idleID, lostID)
	if err != nil {
		t.Fatalf("list other workers: %v", err)
	}
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

	apitestExec(t,
		`UPDATE workers SET status = 'lost' WHERE id = $1`, lostID)

	// two running containers on the busy worker, none on the idle one
	apitestExec(t,
		`INSERT INTO containers (name, image, status, worker_id)
		 SELECT 't42-load-' || g || '-`+fmt.Sprint(suffix)+`', 'nginx', 'running', $1
		 FROM generate_series(1, 2) g`, busyID)

	// a stopped container on the idle worker must not count as load
	apitestExec(t,
		`INSERT INTO containers (name, image, status, worker_id)
		 VALUES ('t42-stopped-`+fmt.Sprint(suffix)+`', 'nginx', 'stopped', $1)`, idleID)

	return busyID, idleID, lostID
}

func apitestExec(t *testing.T, query string, args ...any) {
	t.Helper()

	if _, err := repo.CurrentDB().Conn().Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func TestSelectWorkerPicksLeastLoadedActive(t *testing.T) {
	if os.Getenv("A1S_TEST_DSN") == "" {
		t.Skip("A1S_TEST_DSN not set; skipping database-backed test")
	}

	if _, err := repo.SetupDB(os.Getenv("A1S_TEST_DSN")); err != nil {
		t.Fatalf("setup test database: %v", err)
	}

	busyID, idleID, _ := seedSelectionFixtures(t)

	chosen, err := selectWorker()
	if err != nil {
		t.Fatalf("selectWorker: %v", err)
	}

	if chosen == nil {
		t.Fatalf("expected a worker, got nil")
	}

	if chosen.ID != idleID {
		t.Fatalf("expected the idle worker %d, got %d (busy=%d)", idleID, chosen.ID, busyID)
	}
}

func TestSelectWorkerSkipsLost(t *testing.T) {
	if os.Getenv("A1S_TEST_DSN") == "" {
		t.Skip("A1S_TEST_DSN not set; skipping database-backed test")
	}

	if _, err := repo.SetupDB(os.Getenv("A1S_TEST_DSN")); err != nil {
		t.Fatalf("setup test database: %v", err)
	}

	_, _, lostID := seedSelectionFixtures(t)

	// mark every active worker lost except one with no load: the selection
	// must never return a lost worker
	chosen, err := selectWorker()
	if err != nil {
		t.Fatalf("selectWorker: %v", err)
	}

	if chosen != nil && chosen.ID == lostID {
		t.Fatalf("the lost worker must never be selected")
	}
}
