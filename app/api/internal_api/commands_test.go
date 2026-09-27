package internal_api

import (
	"encoding/json"
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

// seedWorker inserts a worker row and returns its id.
func seedWorker(t *testing.T, name string) int64 {
	t.Helper()

	var id int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`INSERT INTO workers (name, address) VALUES ($1, '10.0.0.1:9001') RETURNING id`,
		name).Scan(&id); err != nil {
		t.Fatalf("seed worker: %v", err)
	}

	t.Cleanup(func() {
		apitest.Exec(t, "DELETE FROM commands WHERE worker_id = $1", id)
		apitest.Exec(t, "DELETE FROM workers WHERE id = $1", id)
	})

	return id
}

// seedCommand inserts a queued command for the worker.
func seedCommand(t *testing.T, workerID int64, action string) int64 {
	t.Helper()

	var id int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`INSERT INTO commands (worker_id, action, payload) VALUES ($1, $2, '{"x":1}'::jsonb) RETURNING id`,
		workerID, action).Scan(&id); err != nil {
		t.Fatalf("seed command: %v", err)
	}

	return id
}

func commandStatus(t *testing.T, id int64) (string, string) {
	t.Helper()

	var status, result string
	if err := repo.CurrentDB().Conn().QueryRow(
		"SELECT status, result::text FROM commands WHERE id = $1", id).Scan(&status, &result); err != nil {
		t.Fatalf("query command: %v", err)
	}

	return status, result
}

func getCommands(t *testing.T, r *gin.Engine, workerID int64) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/internal/workers/%d/commands", workerID), nil)
	req.Header.Set("Authorization", "Bearer "+testToken)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func postResult(t *testing.T, r *gin.Engine, commandID int64, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/v1/internal/commands/%d/result", commandID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestFetchCommandsDeliversQueuedInFIFO(t *testing.T) {
	r := newTestEngine(t)

	workerID := seedWorker(t, fmt.Sprintf("t34-cmd-%d", time.Now().UnixNano()))
	first := seedCommand(t, workerID, "start")
	second := seedCommand(t, workerID, "stop")

	w := getCommands(t, r, workerID)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d %s", w.Code, w.Body.String())
	}

	var got struct {
		Commands []struct {
			ID     int64           `json:"id"`
			Action string          `json:"action"`
			Payload json.RawMessage `json:"payload"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(got.Commands) != 2 {
		t.Fatalf("expected 2 commands, got %d: %s", len(got.Commands), w.Body.String())
	}
	if got.Commands[0].ID != first || got.Commands[1].ID != second {
		t.Fatalf("expected FIFO order [%d %d], got [%d %d]", first, second, got.Commands[0].ID, got.Commands[1].ID)
	}
	if got.Commands[0].Action != "start" || string(got.Commands[0].Payload) != `{"x":1}` {
		t.Fatalf("unexpected first command: %s", w.Body.String())
	}

	if status, _ := commandStatus(t, first); status != "delivered" {
		t.Fatalf("expected delivered after fetch, got %q", status)
	}

	w = getCommands(t, r, workerID)
	var again struct {
		Commands []json.RawMessage `json:"commands"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &again); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(again.Commands) != 0 {
		t.Fatalf("expected an empty second fetch, got %s", w.Body.String())
	}
}

func TestCommandResultMarksDone(t *testing.T) {
	r := newTestEngine(t)

	workerID := seedWorker(t, fmt.Sprintf("t34-res-%d", time.Now().UnixNano()))
	commandID := seedCommand(t, workerID, "inspect")

	w := postResult(t, r, commandID, `{"ok":true,"detail":{"state":"running"}}`)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d %s", w.Code, w.Body.String())
	}

	status, result := commandStatus(t, commandID)
	if status != "done" {
		t.Fatalf("expected done, got %q", status)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("decode result %q: %v", result, err)
	}
	if decoded["ok"] != true {
		t.Fatalf("expected ok true, got %v", decoded["ok"])
	}
	if detail, ok := decoded["detail"].(map[string]any); !ok || detail["state"] != "running" {
		t.Fatalf("expected detail carried through, got %v", decoded["detail"])
	}

	w = postResult(t, r, commandID, `{"ok":true}`)
	if w.Code != 409 {
		t.Fatalf("expected 409 on a repeated result, got %d %s", w.Code, w.Body.String())
	}
}

func TestCommandResultUnknownCommand(t *testing.T) {
	r := newTestEngine(t)

	w := postResult(t, r, 99999999, `{"ok":true}`)
	if w.Code != 404 {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestCommandResultAcceptsFailure(t *testing.T) {
	r := newTestEngine(t)

	workerID := seedWorker(t, fmt.Sprintf("t34-fail-%d", time.Now().UnixNano()))
	commandID := seedCommand(t, workerID, "start")

	w := postResult(t, r, commandID, `{"ok":false,"error":"start failed"}`)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d %s", w.Code, w.Body.String())
	}

	status, result := commandStatus(t, commandID)
	if status != "done" {
		t.Fatalf("expected done, got %q", status)
	}
	if !strings.Contains(result, "start failed") {
		t.Fatalf("expected the error text in the result, got %q", result)
	}
}
