package worker

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/daqing/a1s/app/api/apitest"
	"github.com/daqing/a1s/app/api/internal_api"
	"github.com/daqing/a1s/internal/testdb"
	"github.com/daqing/airway/lib/repo"
	"github.com/gin-gonic/gin"
)

// TestHeartbeatLoopRegistersAndAdvances runs the worker loop against an
// in-process API backed by the test database: the first beat registers the
// worker, later beats bump the version.
func TestHeartbeatLoopRegistersAndAdvances(t *testing.T) {
	dsn := apitestDsn(t)
	if dsn == "" {
		t.Skip("A1S_TEST_DSN not set; skipping database-backed test")
	}

	if _, err := repo.SetupDB(dsn); err != nil {
		t.Fatalf("setup test database: %v", err)
	}

	t.Setenv("A1S_INTERNAL_TOKEN", "t33-token")

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	internal_api.Mount(engine.Group("/api/v1"))

	server := httptest.NewServer(engine)
	defer server.Close()

	name := fmt.Sprintf("t33-loop-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		apitest.Exec(t, "DELETE FROM workers WHERE name = $1", name)
	})

	client := &apiClient{baseURL: server.URL, token: "t33-token"}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(400 * time.Millisecond)
		cancel()
	}()

	runLoop(ctx, client, name, 100*time.Millisecond, 50*time.Millisecond)

	var version int
	var fresh bool
	if err := repo.CurrentDB().Conn().QueryRow(
		"SELECT version, last_heartbeat_at > now() - interval '5 seconds' FROM workers WHERE name = $1",
		name).Scan(&version, &fresh); err != nil {
		t.Fatalf("query worker: %v", err)
	}

	if version < 3 {
		t.Fatalf("expected at least 3 beats recorded (version >= 3), got %d", version)
	}
	if !fresh {
		t.Fatalf("expected a fresh last_heartbeat_at")
	}
}

// TestLoopSurvivesTransientErrors points the loop at a dead API address and
// asserts it keeps running (no panic, no exit) until the context ends.
func TestLoopSurvivesTransientErrors(t *testing.T) {
	client := &apiClient{baseURL: "http://127.0.0.1:1", token: "t33-token"}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(250 * time.Millisecond)
		cancel()
	}()

	runLoop(ctx, client, "t33-offline", 50*time.Millisecond, 50*time.Millisecond)
	// reaching here means the loop survived the failed beats
}

// apitestDsn returns the worker package's private test database DSN,
// skipping the caller's test when A1S_TEST_DSN is unset.
func apitestDsn(t *testing.T) string {
	return testdb.DSN(t, "worker")
}
