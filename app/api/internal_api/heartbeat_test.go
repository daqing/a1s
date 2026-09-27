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
	"github.com/gin-gonic/gin"
)

const testToken = "test-token"

func newTestEngine(t *testing.T) *gin.Engine {
	t.Setenv("A1S_INTERNAL_TOKEN", testToken)

	return apitest.Setup(t, func(e *gin.Engine) {
		Mount(e.Group("/api/v1"))
	})
}

// postHeartbeat sends a heartbeat request with the given Authorization
// header value ("" sends none).
func postHeartbeat(t *testing.T, r *gin.Engine, bearer string, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/heartbeat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestHeartbeatRequiresBearerToken(t *testing.T) {
	r := newTestEngine(t)

	w := postHeartbeat(t, r, "", `{"name":"t32-guard"}`)
	if w.Code != 401 {
		t.Fatalf("expected 401 without a token, got %d %s", w.Code, w.Body.String())
	}

	// the rejection must happen before the handler runs
	var exists bool
	if err := repo.CurrentDB().Conn().QueryRow(
		"SELECT EXISTS(SELECT 1 FROM workers WHERE name = 't32-guard')").Scan(&exists); err != nil {
		t.Fatalf("query worker: %v", err)
	}
	if exists {
		t.Fatalf("rejected request must not upsert the worker row")
	}
}

func TestHeartbeatRejectsWrongToken(t *testing.T) {
	r := newTestEngine(t)

	w := postHeartbeat(t, r, "Bearer wrong-token", `{"name":"w1"}`)
	if w.Code != 401 {
		t.Fatalf("expected 401 for a wrong token, got %d %s", w.Code, w.Body.String())
	}
}

func TestHeartbeatClosedWithoutServerToken(t *testing.T) {
	t.Setenv("A1S_INTERNAL_TOKEN", "")
	r := apitest.Setup(t, func(e *gin.Engine) {
		Mount(e.Group("/api/v1"))
	})

	w := postHeartbeat(t, r, "Bearer whatever", `{"name":"w1"}`)
	if w.Code != 401 {
		t.Fatalf("expected 401 with no server-side token, got %d %s", w.Code, w.Body.String())
	}
}

func TestHeartbeatRegistersNewWorker(t *testing.T) {
	r := newTestEngine(t)

	name := fmt.Sprintf("t32-new-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		apitest.Exec(t, "DELETE FROM workers WHERE name = $1", name)
	})

	w := postHeartbeat(t, r, "Bearer "+testToken,
		fmt.Sprintf(`{"name":%q,"address":"10.0.0.9:9001"}`, name))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"active"`) {
		t.Fatalf("expected 200 active, got %d %s", w.Code, w.Body.String())
	}

	var status string
	if err := repo.CurrentDB().Conn().QueryRow(
		"SELECT status FROM workers WHERE name = $1", name).Scan(&status); err != nil {
		t.Fatalf("query worker: %v", err)
	}
	if status != "active" {
		t.Fatalf("expected active row, got %q", status)
	}
}

func TestHeartbeatRefreshesExistingWorker(t *testing.T) {
	r := newTestEngine(t)

	name := fmt.Sprintf("t32-old-%d", time.Now().UnixNano())
	apitest.Exec(t, `INSERT INTO workers (name, address, status, last_heartbeat_at)
		VALUES ($1, '10.0.0.8:9001', 'active', now() - interval '1 hour')`, name)
	t.Cleanup(func() {
		apitest.Exec(t, "DELETE FROM workers WHERE name = $1", name)
	})

	w := postHeartbeat(t, r, "Bearer "+testToken, fmt.Sprintf(`{"name":%q}`, name))
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d %s", w.Code, w.Body.String())
	}

	var fresh bool
	var version int
	if err := repo.CurrentDB().Conn().QueryRow(
		"SELECT last_heartbeat_at > now() - interval '5 minutes', version FROM workers WHERE name = $1",
		name).Scan(&fresh, &version); err != nil {
		t.Fatalf("query worker: %v", err)
	}

	if !fresh {
		t.Fatalf("expected last_heartbeat_at refreshed")
	}
	if version != 1 {
		t.Fatalf("expected version 1 after the heartbeat, got %d", version)
	}
}

func TestHeartbeatReregistersLostWorker(t *testing.T) {
	r := newTestEngine(t)

	name := fmt.Sprintf("t32-lost-%d", time.Now().UnixNano())
	apitest.Exec(t, `INSERT INTO workers (name, address, status, last_heartbeat_at)
		VALUES ($1, '10.0.0.7:9001', 'lost', now() - interval '1 hour')`, name)
	t.Cleanup(func() {
		apitest.Exec(t, "DELETE FROM workers WHERE name = $1", name)
	})

	w := postHeartbeat(t, r, "Bearer "+testToken, fmt.Sprintf(`{"name":%q}`, name))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"active"`) {
		t.Fatalf("expected the lost worker to return active, got %d %s", w.Code, w.Body.String())
	}

	var status string
	if err := repo.CurrentDB().Conn().QueryRow(
		"SELECT status FROM workers WHERE name = $1", name).Scan(&status); err != nil {
		t.Fatalf("query worker: %v", err)
	}
	if status != "active" {
		t.Fatalf("expected active row, got %q", status)
	}
}

func TestHeartbeatRequiresName(t *testing.T) {
	r := newTestEngine(t)

	w := postHeartbeat(t, r, "Bearer "+testToken, `{"address":"10.0.0.1"}`)
	if w.Code != 400 {
		t.Fatalf("expected 400 for a missing name, got %d %s", w.Code, w.Body.String())
	}
}
