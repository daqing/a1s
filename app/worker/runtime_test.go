package worker

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"
	"time"
)

// TestStartExecutorRunsTask exercises the start executor against a real
// containerd: it pulls a small image, creates and starts the container,
// and verifies the task is running. It only works when the client shares
// the filesystem with the daemon (layer application is client-side), so it
// runs on Linux nodes only; see docs/architecture.md for the dev setup.
func TestStartExecutorRunsTask(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("start executor test needs a Linux client sharing the filesystem with containerd")
	}

	addr := envOr("A1S_CONTAINERD_ADDR", "")
	if addr == "" {
		t.Skip("A1S_CONTAINERD_ADDR not set; skipping containerd start test")
	}

	rt := &containerRuntime{
		addr:        addr,
		namespace:   envOr("A1S_CONTAINERD_NAMESPACE", "a1s"),
		snapshotter: envOr("A1S_CONTAINERD_SNAPSHOTTER", "overlayfs"),
		platform:    envOr("A1S_CONTAINERD_PLATFORM", ""),
	}

	payload, err := json.Marshal(map[string]any{
		"name":  "t35-start",
		"image": "docker.io/library/busybox:latest",
		"command": "sleep",
		"args":  []string{"300"},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if cd, err := rt.cdClient(cleanupCtx); err == nil {
			rt.removeExisting(cleanupCtx, cd, "t35-start")
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	result := rt.execute(ctx, command{Action: "start", Payload: payload})
	if !result.OK {
		t.Fatalf("start failed: %s", result.Error)
	}

	cd, err := rt.cdClient(context.Background())
	if err != nil {
		t.Fatalf("connect for verification: %v", err)
	}

	container, err := cd.LoadContainer(context.Background(), "t35-start")
	if err != nil {
		t.Fatalf("load container: %v", err)
	}

	task, err := container.Task(context.Background(), nil)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}

	status, err := task.Status(context.Background())
	if err != nil {
		t.Fatalf("task status: %v", err)
	}

	if status.Status != "running" {
		t.Fatalf("expected a running task, got %q", status.Status)
	}
}
