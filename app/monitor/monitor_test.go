package monitor

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/daqing/a1s/app/models"
	"github.com/daqing/airway/lib/repo"
)

func TestParseDuration(t *testing.T) {
	cases := []struct {
		raw      string
		fallback time.Duration
		want     time.Duration
		wantOK   bool
	}{
		{raw: "", fallback: 5 * time.Second, want: 5 * time.Second, wantOK: true},
		{raw: "10s", fallback: 5 * time.Second, want: 10 * time.Second, wantOK: true},
		{raw: "1m30s", fallback: 5 * time.Second, want: 90 * time.Second, wantOK: true},
		{raw: "bogus", fallback: 5 * time.Second, want: 5 * time.Second, wantOK: false},
		{raw: "0s", fallback: 5 * time.Second, want: 5 * time.Second, wantOK: false},
		{raw: "-3s", fallback: 5 * time.Second, want: 5 * time.Second, wantOK: false},
	}

	for _, tc := range cases {
		got, ok := parseDuration(tc.raw, tc.fallback)
		if ok != tc.wantOK || got != tc.want {
			t.Fatalf("parseDuration(%q) = %s, %v; want %s, %v", tc.raw, got, ok, tc.want, tc.wantOK)
		}
	}
}

// setupMonitorDB skips the test without A1S_TEST_DSN and installs the
// global test database.
func setupMonitorDB(t *testing.T) {
	t.Helper()

	dsn := os.Getenv("A1S_TEST_DSN")
	if dsn == "" {
		t.Skip("A1S_TEST_DSN not set; skipping database-backed test")
	}

	if _, err := repo.SetupDB(dsn); err != nil {
		t.Fatalf("setup test database: %v", err)
	}
}

// seedMonitorWorker inserts a worker with the given status and heartbeat
// age, returning its id and version.
func seedMonitorWorker(t *testing.T, name, status string, heartbeatAge time.Duration) (int64, int64) {
	t.Helper()

	var id int64
	var version int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`INSERT INTO workers (name, address, status, last_heartbeat_at)
		 VALUES ($1, '10.0.0.1:9001', $2, now() - $3::interval) RETURNING id, version`,
		name, status, fmt.Sprintf("%d seconds", int(heartbeatAge.Seconds()))).Scan(&id, &version); err != nil {
		t.Fatalf("seed worker: %v", err)
	}

	t.Cleanup(func() {
		repo.CurrentDB().Conn().Exec(`DELETE FROM workers WHERE id = $1`, id)
	})

	return id, version
}

// workerRow reads the current status and version of one worker row.
func workerRow(t *testing.T, id int64) (string, int64) {
	t.Helper()

	var status string
	var version int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT status, version FROM workers WHERE id = $1`, id).Scan(&status, &version); err != nil {
		t.Fatalf("query worker: %v", err)
	}

	return status, version
}

func TestMarkLostWorkersFlipsStale(t *testing.T) {
	setupMonitorDB(t)

	name := fmt.Sprintf("t52-stale-%d", time.Now().UnixNano())
	id, _ := seedMonitorWorker(t, name, "active", time.Hour)

	markLostWorkers(15 * time.Second)

	status, _ := workerRow(t, id)
	if status != models.WorkerLost {
		t.Fatalf("expected the stale worker lost, got %q", status)
	}

	// a second pass is a no-op: lost workers are not candidates anymore
	markLostWorkers(15 * time.Second)

	status, _ = workerRow(t, id)
	if status != models.WorkerLost {
		t.Fatalf("expected the worker to stay lost, got %q", status)
	}
}

func TestMarkLostWorkersKeepsFresh(t *testing.T) {
	setupMonitorDB(t)

	name := fmt.Sprintf("t52-fresh-%d", time.Now().UnixNano())
	id, version := seedMonitorWorker(t, name, "active", 0)

	markLostWorkers(15 * time.Second)

	status, versionAfter := workerRow(t, id)
	if status != models.WorkerActive || versionAfter != version {
		t.Fatalf("expected the fresh worker untouched, got %q v%d", status, versionAfter)
	}
}

func TestMarkLostWorkersConcurrentSingleMark(t *testing.T) {
	setupMonitorDB(t)

	name := fmt.Sprintf("t52-race-%d", time.Now().UnixNano())
	id, version := seedMonitorWorker(t, name, "active", time.Hour)

	// two monitor instances racing the same stale worker
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			markLostWorkers(15 * time.Second)
		}()
	}
	wg.Wait()

	status, versionAfter := workerRow(t, id)
	if status != models.WorkerLost {
		t.Fatalf("expected the worker lost, got %q", status)
	}
	if versionAfter != version+1 {
		t.Fatalf("expected exactly one marking (version %d), got %d", version+1, versionAfter)
	}
}
