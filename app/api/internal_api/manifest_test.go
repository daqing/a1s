package internal_api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/daqing/a1s/app/api/apitest"
	"github.com/daqing/airway/lib/repo"
)

// putManifest sends the worker's reality snapshot with the test token.
func putManifest(t *testing.T, r interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, workerID int64, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/api/v1/internal/workers/%d/manifest", workerID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// manifestFixture seeds an active worker with containers in the given
// statuses, assigned to it.
func manifestFixture(t *testing.T, statuses ...string) (workerID int64, ids []int64) {
	t.Helper()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	workerID = seedWorker(t, "t61-worker-"+suffix)

	for i, status := range statuses {
		ids = append(ids, seedReportContainer(t, fmt.Sprintf("t61-c%d-%s-%s", i, status, suffix), status))

		if _, err := repo.CurrentDB().Conn().Exec(
			`UPDATE containers SET worker_id = $2 WHERE id = $1`, ids[i], workerID); err != nil {
			t.Fatalf("assign container: %v", err)
		}
	}

	t.Cleanup(func() {
		apitest.Exec(t, `DELETE FROM commands WHERE worker_id = $1`, workerID)
		apitest.Exec(t, `DELETE FROM containers WHERE worker_id = $1`, workerID)
		apitest.Exec(t, `DELETE FROM workers WHERE id = $1`, workerID)
	})

	return workerID, ids
}

func TestManifestRequeuesMissingContainers(t *testing.T) {
	r := newTestEngine(t)

	workerID, ids := manifestFixture(t, "running", "scheduled")

	// the worker reports only the first container: the second is missing
	body := fmt.Sprintf(`{"containers":[{"id":%d,"status":"running"}]}`, ids[0])
	w := putManifest(t, r, workerID, body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"repaired":1`) {
		t.Fatalf("expected one repair, got %d %s", w.Code, w.Body.String())
	}

	var status string
	var workerSet bool
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT status, worker_id IS NOT NULL FROM containers WHERE id = $1`,
		ids[1]).Scan(&status, &workerSet); err != nil {
		t.Fatalf("query missing container: %v", err)
	}

	if status != "pending" || workerSet {
		t.Fatalf("expected the missing container requeued to pending, got %q worker=%v", status, workerSet)
	}

	// the reported container is untouched
	status, _ = containerRow(t, ids[0])
	if status != "running" {
		t.Fatalf("expected the reported container untouched, got %q", status)
	}
}

func TestManifestReissuesLostStopCommands(t *testing.T) {
	r := newTestEngine(t)

	// a stopped row whose runtime still runs
	workerID, ids := manifestFixture(t, "stopped")
	body := fmt.Sprintf(`{"containers":[{"id":%d,"status":"running"}]}`, ids[0])
	w := putManifest(t, r, workerID, body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"repaired":1`) {
		t.Fatalf("expected one repair, got %d %s", w.Code, w.Body.String())
	}

	var action string
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT action FROM commands WHERE container_id = $1 AND status = 'queued'`, ids[0]).Scan(&action); err != nil {
		t.Fatalf("query stop command: %v", err)
	}
	if action != "stop" {
		t.Fatalf("expected a queued stop command, got %q", action)
	}

	// a second manifest pass must not queue a duplicate
	w = putManifest(t, r, workerID, body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"repaired":0`) {
		t.Fatalf("expected no duplicate repair, got %d %s", w.Code, w.Body.String())
	}
}

func TestManifestConsistentStateRepairsNothing(t *testing.T) {
	r := newTestEngine(t)

	workerID, ids := manifestFixture(t, "running", "stopped")

	body := fmt.Sprintf(`{"containers":[{"id":%d,"status":"running"},{"id":%d,"status":"stopped"}]}`,
		ids[0], ids[1])
	w := putManifest(t, r, workerID, body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"repaired":0`) {
		t.Fatalf("expected no repairs, got %d %s", w.Code, w.Body.String())
	}
}

func TestManifestSkipsRowsWithInFlightStart(t *testing.T) {
	r := newTestEngine(t)

	// a scheduled row whose start command is still queued: the runtime may
	// legitimately not exist yet, so the reconcile pass must leave it alone
	workerID, ids := manifestFixture(t, "scheduled")

	if _, err := repo.CurrentDB().Conn().Exec(
		`INSERT INTO commands (worker_id, container_id, action)
		 VALUES ($1, $2, 'start')`, workerID, ids[0]); err != nil {
		t.Fatalf("seed in-flight start command: %v", err)
	}

	w := putManifest(t, r, workerID, `{"containers":[]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"repaired":0`) {
		t.Fatalf("expected no repairs for an in-flight start, got %d %s", w.Code, w.Body.String())
	}

	status, _ := containerRow(t, ids[0])
	if status != "scheduled" {
		t.Fatalf("expected the row untouched, got %q", status)
	}
}
