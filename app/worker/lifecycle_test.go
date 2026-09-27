package worker

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"github.com/daqing/a1s/app/models"
)

func TestMapTaskStatus(t *testing.T) {
	cases := []struct {
		state    string
		exitCode uint32
		want     string
	}{
		{"running", 0, models.ContainerRunning},
		{"RUNNING", 0, models.ContainerRunning},
		{"stopped", 0, models.ContainerStopped},
		{"STOPPED", 137, models.ContainerFailed},
		{"paused", 0, models.ContainerStopped},
		{"created", 0, models.ContainerStopped},
	}

	for _, tc := range cases {
		if got := mapTaskStatus(tc.state, tc.exitCode); got != tc.want {
			t.Fatalf("mapTaskStatus(%q, %d) = %q, want %q", tc.state, tc.exitCode, got, tc.want)
		}
	}
}

// TestLifecycleExecutors drives the full container lifecycle against a real
// containerd: start, inspect running, stop, inspect stopped, remove,
// inspect missing. It only works when the client shares the filesystem with
// the daemon (layer application is client-side), so it runs on Linux nodes
// only; see docs/architecture.md for the dev setup.
func TestLifecycleExecutors(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("lifecycle test needs a Linux client sharing the filesystem with containerd")
	}

	addr := envOr("A1S_CONTAINERD_ADDR", "")
	if addr == "" {
		t.Skip("A1S_CONTAINERD_ADDR not set; skipping containerd lifecycle test")
	}

	rt := &containerRuntime{
		addr:        addr,
		namespace:   envOr("A1S_CONTAINERD_NAMESPACE", "a1s"),
		snapshotter: envOr("A1S_CONTAINERD_SNAPSHOTTER", "overlayfs"),
		platform:    envOr("A1S_CONTAINERD_PLATFORM", ""),
	}

	const name = "t36-lifecycle"

	startPayload, err := json.Marshal(map[string]any{
		"name":    name,
		"image":   "docker.io/library/busybox:latest",
		"command": "sleep",
		"args":    []string{"600"},
	})
	if err != nil {
		t.Fatalf("marshal start payload: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if cd, err := rt.cdClient(cleanupCtx); err == nil {
			rt.removeExisting(cleanupCtx, cd, name)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	run := func(action string, payload json.RawMessage) map[string]any {
		t.Helper()

		result := rt.execute(ctx, command{Action: action, Payload: payload})
		if !result.OK {
			t.Fatalf("%s failed: %s", action, result.Error)
		}

		var detail map[string]any
		if err := json.Unmarshal(result.Detail, &detail); err != nil {
			t.Fatalf("decode %s detail %q: %v", action, result.Detail, err)
		}

		return detail
	}

	run("start", startPayload)

	running := run("inspect", json.RawMessage(`{"name":"`+name+`"}`))
	if running["status"] != models.ContainerRunning {
		t.Fatalf("expected running after start, got %#v", running)
	}

	stopped := run("stop", json.RawMessage(`{"name":"`+name+`"}`))
	if stopped["state"] != "stopped" {
		t.Fatalf("expected stopped state, got %#v", stopped)
	}

	afterStop := run("inspect", json.RawMessage(`{"name":"`+name+`"}`))
	if afterStop["status"] != models.ContainerStopped {
		t.Fatalf("expected stopped after stop, got %#v", afterStop)
	}

	run("stop", json.RawMessage(`{"name":"`+name+`"}`)) // idempotent

	removed := run("remove", json.RawMessage(`{"name":"`+name+`"}`))
	if removed["state"] != "removed" {
		t.Fatalf("expected removed state, got %#v", removed)
	}

	missing := run("inspect", json.RawMessage(`{"name":"`+name+`"}`))
	if missing["exists"] != false {
		t.Fatalf("expected missing after remove, got %#v", missing)
	}
}
