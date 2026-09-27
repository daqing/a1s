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

// seedReportContainer inserts a container row in the given status and
// returns its id.
func seedReportContainer(t *testing.T, name, status string) int64 {
	t.Helper()

	var id int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`INSERT INTO containers (name, image, status) VALUES ($1, 'nginx', $2) RETURNING id`,
		name, status).Scan(&id); err != nil {
		t.Fatalf("seed container: %v", err)
	}

	t.Cleanup(func() {
		apitest.Exec(t, "DELETE FROM containers WHERE id = $1", id)
	})

	return id
}

// putStatus sends a status report with the test bearer token.
func putStatus(t *testing.T, r interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, containerID int64, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/api/v1/internal/containers/%d/status", containerID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// containerRow fetches status and version of one container row.
func containerRow(t *testing.T, id int64) (string, int64) {
	t.Helper()

	var status string
	var version int64
	if err := repo.CurrentDB().Conn().QueryRow(
		"SELECT status, version FROM containers WHERE id = $1", id).Scan(&status, &version); err != nil {
		t.Fatalf("query container: %v", err)
	}

	return status, version
}

func TestStatusReportAppliesObservedTransitions(t *testing.T) {
	r := newTestEngine(t)

	name := fmt.Sprintf("t37-%d", time.Now().UnixNano())
	scheduled := seedReportContainer(t, name, "scheduled")

	// scheduled -> running
	w := putStatus(t, r, scheduled, `{"status":"running"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"applied":true`) {
		t.Fatalf("expected applied report, got %d %s", w.Code, w.Body.String())
	}

	status, version := containerRow(t, scheduled)
	if status != "running" || version != 1 {
		t.Fatalf("expected running v1, got %q v%d", status, version)
	}

	// running -> failed
	w = putStatus(t, r, scheduled, `{"status":"failed"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"applied":true`) {
		t.Fatalf("expected failed applied, got %d %s", w.Code, w.Body.String())
	}
}

func TestStatusReportSameStatusIsNoop(t *testing.T) {
	r := newTestEngine(t)

	name := fmt.Sprintf("t37-noop-%d", time.Now().UnixNano())
	id := seedReportContainer(t, name, "scheduled")

	w := putStatus(t, r, id, `{"status":"running"}`)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	_, before := containerRow(t, id)

	w = putStatus(t, r, id, `{"status":"running"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"applied":false`) {
		t.Fatalf("expected a no-op, got %d %s", w.Code, w.Body.String())
	}

	_, after := containerRow(t, id)
	if before != after {
		t.Fatalf("a same-status report must not bump the version")
	}
}

func TestStatusReportCannotOverrideDesiredState(t *testing.T) {
	r := newTestEngine(t)

	name := fmt.Sprintf("t37-desired-%d", time.Now().UnixNano())
	id := seedReportContainer(t, name, "stopped")

	// the reporter must not resurrect a desired-stopped container
	w := putStatus(t, r, id, `{"status":"running"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"applied":false`) {
		t.Fatalf("expected a rejected report, got %d %s", w.Code, w.Body.String())
	}

	status, _ := containerRow(t, id)
	if status != "stopped" {
		t.Fatalf("expected the row untouched, got %q", status)
	}
}

func TestStatusReportValidation(t *testing.T) {
	r := newTestEngine(t)

	name := fmt.Sprintf("t37-bad-%d", time.Now().UnixNano())
	id := seedReportContainer(t, name, "scheduled")

	w := putStatus(t, r, id, `{"status":"zombie"}`)
	if w.Code != 400 {
		t.Fatalf("expected 400 for an unmapped status, got %d", w.Code)
	}

	w = putStatus(t, r, id, `{"status":""}`)
	if w.Code != 400 {
		t.Fatalf("expected 400 for an empty status, got %d", w.Code)
	}

	w = putStatus(t, r, 99999999, `{"status":"running"}`)
	if w.Code != 404 {
		t.Fatalf("expected 404 for a missing container, got %d", w.Code)
	}
}
