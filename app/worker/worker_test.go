package worker

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestContainerdConnectivity proves the containerd client reaches a real
// daemon: it lists namespaces, creates one, and lists again. It skips
// unless A1S_CONTAINERD_ADDR is set, e.g.
// A1S_CONTAINERD_ADDR=tcp://127.0.0.1:60001 with the a1s-containerd dev
// container running (see docs/architecture.md).
func TestContainerdConnectivity(t *testing.T) {
	addr := os.Getenv("A1S_CONTAINERD_ADDR")
	if addr == "" {
		t.Skip("A1S_CONTAINERD_ADDR not set; skipping containerd connectivity test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := Dial(ctx, addr)
	if err != nil {
		t.Fatalf("connect to containerd at %s: %v", addr, err)
	}
	defer client.Close()

	before, err := client.NamespaceService().List(ctx)
	if err != nil {
		t.Fatalf("list namespaces: %v", err)
	}
	t.Logf("namespaces before: %v", before)

	const spike = "a1s-spike"
	if err := client.NamespaceService().Create(ctx, spike, nil); err != nil {
		t.Fatalf("create namespace %q: %v", spike, err)
	}

	after, err := client.NamespaceService().List(ctx)
	if err != nil {
		t.Fatalf("list namespaces: %v", err)
	}
	t.Logf("namespaces after: %v", after)

	found := false
	for _, ns := range after {
		if ns == spike {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected %q among namespaces, got %v", spike, after)
	}
}
