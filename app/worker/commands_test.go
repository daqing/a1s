package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/daqing/a1s/app/api/apitest"
	"github.com/daqing/a1s/app/api/internal_api"
	"github.com/daqing/airway/lib/repo"
	"github.com/gin-gonic/gin"
)

// TestPollExecutesQueuedCommands seeds a queued command for the worker the
// loop will register as, runs the loop briefly, and asserts the command
// comes out done with the reported result in the database.
func TestPollExecutesQueuedCommands(t *testing.T) {
	dsn := apitestDsn(t)
	if dsn == "" {
		t.Skip("A1S_TEST_DSN not set; skipping database-backed test")
	}

	if _, err := repo.SetupDB(dsn); err != nil {
		t.Fatalf("setup test database: %v", err)
	}

	t.Setenv("A1S_INTERNAL_TOKEN", "t34-token")

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	internal_api.Mount(engine.Group("/api/v1"))

	server := httptest.NewServer(engine)
	defer server.Close()

	name := fmt.Sprintf("t34-poll-%d", time.Now().UnixNano())

	var workerID int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`INSERT INTO workers (name, address) VALUES ($1, '') RETURNING id`, name).Scan(&workerID); err != nil {
		t.Fatalf("seed worker: %v", err)
	}

	var commandID int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`INSERT INTO commands (worker_id, action) VALUES ($1, 'inspect') RETURNING id`,
		workerID).Scan(&commandID); err != nil {
		t.Fatalf("seed command: %v", err)
	}

	t.Cleanup(func() {
		apitest.Exec(t, "DELETE FROM commands WHERE id = $1", commandID)
		apitest.Exec(t, "DELETE FROM workers WHERE id = $1", workerID)
	})

	client := &apiClient{baseURL: server.URL, token: "t34-token"}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	runLoop(ctx, client, name, time.Second, 50*time.Millisecond)

	var status, result string
	if err := repo.CurrentDB().Conn().QueryRow(
		"SELECT status, result::text FROM commands WHERE id = $1", commandID).Scan(&status, &result); err != nil {
		t.Fatalf("query command: %v", err)
	}

	if status != "done" {
		t.Fatalf("expected the command done, got %q", status)
	}

	var decoded struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("decode result %q: %v", result, err)
	}
	// the executor rejects the command in this environment (no containerd on
	// the test host); the channel mechanics under test are fetch + report
	if decoded.OK {
		t.Fatalf("expected a rejected result, got %#v", decoded)
	}
}
