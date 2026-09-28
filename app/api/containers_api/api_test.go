package containers_api

import (
	"fmt"
	"testing"
	"time"

	"github.com/daqing/a1s/app/api/apitest"
	"github.com/gin-gonic/gin"
)

// apiContainer mirrors the wire representation returned by the API.
type apiContainer struct {
	ID            int64             `json:"id"`
	Name          string            `json:"name"`
	Image         string            `json:"image"`
	Args          []string          `json:"args"`
	Env           map[string]string `json:"env"`
	Status        string            `json:"status"`
	WorkerID      *int64            `json:"worker_id"`
	RestartPolicy string            `json:"restart_policy"`
	Version       int64             `json:"version"`
}

// newTestEngine builds an engine carrying the container API routes.
func newTestEngine(t *testing.T) *gin.Engine {
	return apitest.Setup(t, func(e *gin.Engine) {
		Routes(e.Group("/api/v1"))
	})
}

// uniqueName builds a collision-free container name for the current test.
func uniqueName(base string) string {
	return fmt.Sprintf("t26-%s-%d", base, time.Now().UnixNano())
}

// createContainer creates a container and registers its cleanup; it fails
// the test when the API does not answer 201.
func createContainer(t *testing.T, r *gin.Engine, body map[string]any) apiContainer {
	t.Helper()

	var created apiContainer
	w := apitest.DoJSON(t, r, "POST", "/api/v1/containers", body, &created)
	if w.Code != 201 {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	t.Cleanup(func() {
		apitest.Exec(t, "DELETE FROM containers WHERE id = $1", created.ID)
	})

	return created
}

func TestCreateContainerHappyPath(t *testing.T) {
	r := newTestEngine(t)

	name := uniqueName("happy")
	body := map[string]any{
		"name":           name,
		"image":          "nginx",
		"args":           []string{"-g", "daemon off;"},
		"env":            map[string]string{"FOO": "bar"},
		"restart_policy": "on-failure:3",
	}

	created := createContainer(t, r, body)

	if created.Name != name || created.Image != "nginx" {
		t.Fatalf("unexpected created row: %#v", created)
	}
	if created.Status != "pending" {
		t.Fatalf("expected pending status, got %q", created.Status)
	}
	if created.WorkerID != nil {
		t.Fatalf("expected nil worker_id, got %v", *created.WorkerID)
	}
	if created.Version != 0 {
		t.Fatalf("expected version 0, got %d", created.Version)
	}
	if len(created.Args) != 2 || created.Args[1] != "daemon off;" {
		t.Fatalf("args round-trip failed: %#v", created.Args)
	}
	if created.Env["FOO"] != "bar" {
		t.Fatalf("env round-trip failed: %#v", created.Env)
	}
	if created.RestartPolicy != "on-failure:3" {
		t.Fatalf("expected restart_policy on-failure:3, got %q", created.RestartPolicy)
	}
}

func TestCreateContainerGeneratesName(t *testing.T) {
	r := newTestEngine(t)

	created := createContainer(t, r, map[string]any{"image": "library/redis:7"})
	if len(created.Name) == 0 || created.Name[:6] != "redis-" {
		t.Fatalf("expected generated redis- slug name, got %q", created.Name)
	}
}

func TestCreateContainerValidationErrors(t *testing.T) {
	r := newTestEngine(t)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"missing image", map[string]any{"name": "x"}},
		{"blank image", map[string]any{"image": ""}},
		{"bad policy", map[string]any{"image": "nginx", "restart_policy": "sometimes"}},
		{"bad name", map[string]any{"name": "-lead", "image": "nginx"}},
		{"non-string env", map[string]any{"image": "nginx", "env": map[string]any{"K": 1}}},
	}

	for _, tc := range cases {
		var got apitest.ErrorEnvelope
		w := apitest.DoJSON(t, r, "POST", "/api/v1/containers", tc.body, &got)
		if w.Code != 400 || got.Error.Code != "validation_error" {
			t.Fatalf("%s: expected 400 validation_error, got %d %s", tc.name, w.Code, w.Body.String())
		}
	}
}

func TestCreateContainerNameConflict(t *testing.T) {
	r := newTestEngine(t)

	name := uniqueName("dup")
	createContainer(t, r, map[string]any{"name": name, "image": "nginx"})

	var got apitest.ErrorEnvelope
	w := apitest.DoJSON(t, r, "POST", "/api/v1/containers",
		map[string]any{"name": name, "image": "nginx"}, &got)
	if w.Code != 409 || got.Error.Code != "conflict" {
		t.Fatalf("expected 409 conflict, got %d %s", w.Code, w.Body.String())
	}
}

func TestListContainersNewestFirstWithFilter(t *testing.T) {
	r := newTestEngine(t)

	older := createContainer(t, r, map[string]any{"name": uniqueName("list-old"), "image": "nginx"})
	newer := createContainer(t, r, map[string]any{"name": uniqueName("list-new"), "image": "nginx"})

	type listResponse struct {
		Containers []apiContainer `json:"containers"`
	}

	var all listResponse
	w := apitest.DoJSON(t, r, "GET", "/api/v1/containers", nil, &all)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	pos := map[string]int{}
	for idx, c := range all.Containers {
		pos[c.Name] = idx
	}

	if pos[newer.Name] >= pos[older.Name] {
		t.Fatalf("expected newest first: %s (at %d) must precede %s (at %d)",
			newer.Name, pos[newer.Name], older.Name, pos[older.Name])
	}

	var filtered listResponse
	w = apitest.DoJSON(t, r, "GET", "/api/v1/containers?status=pending", nil, &filtered)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if _, ok := pos[newer.Name]; ok {
		found := false
		for _, c := range filtered.Containers {
			if c.Name == newer.Name {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected %s in the pending-filtered list", newer.Name)
		}
	}

	var got apitest.ErrorEnvelope
	w = apitest.DoJSON(t, r, "GET", "/api/v1/containers?status=zombie", nil, &got)
	if w.Code != 400 || got.Error.Code != "validation_error" {
		t.Fatalf("expected 400 validation_error for unknown status, got %d %s", w.Code, w.Body.String())
	}
}

func TestInspectContainer(t *testing.T) {
	r := newTestEngine(t)

	created := createContainer(t, r, map[string]any{"name": uniqueName("inspect"), "image": "nginx"})

	var got apiContainer
	w := apitest.DoJSON(t, r, "GET", fmt.Sprintf("/api/v1/containers/%d", created.ID), nil, &got)
	if w.Code != 200 || got.Name != created.Name || got.ID != created.ID {
		t.Fatalf("expected 200 with the created row, got %d %s", w.Code, w.Body.String())
	}

	var errResp apitest.ErrorEnvelope
	w = apitest.DoJSON(t, r, "GET", "/api/v1/containers/99999999", nil, &errResp)
	if w.Code != 404 || errResp.Error.Code != "not_found" {
		t.Fatalf("expected 404 not_found, got %d %s", w.Code, w.Body.String())
	}

	w = apitest.DoJSON(t, r, "GET", "/api/v1/containers/abc", nil, &errResp)
	if w.Code != 404 {
		t.Fatalf("expected 404 for a non-numeric id, got %d", w.Code)
	}
}

func TestStopContainer(t *testing.T) {
	r := newTestEngine(t)

	created := createContainer(t, r, map[string]any{"name": uniqueName("stop"), "image": "nginx"})

	// pending is not stoppable
	var errResp apitest.ErrorEnvelope
	w := apitest.DoJSON(t, r, "POST", fmt.Sprintf("/api/v1/containers/%d/stop", created.ID), nil, &errResp)
	if w.Code != 409 || errResp.Error.Code != "conflict" {
		t.Fatalf("expected 409 for a pending container, got %d %s", w.Code, w.Body.String())
	}

	// simulate the worker report that flips the container to running
	apitest.Exec(t, "UPDATE containers SET status = 'running' WHERE id = $1", created.ID)

	var stopped apiContainer
	w = apitest.DoJSON(t, r, "POST", fmt.Sprintf("/api/v1/containers/%d/stop", created.ID), nil, &stopped)
	if w.Code != 200 || stopped.Status != "stopped" {
		t.Fatalf("expected 200 stopped, got %d %s", w.Code, w.Body.String())
	}
	if stopped.Version != created.Version+1 {
		t.Fatalf("expected version %d after the transition, got %d", created.Version+1, stopped.Version)
	}

	// a repeated stop is idempotent: same answer, no version bump
	var again apiContainer
	w = apitest.DoJSON(t, r, "POST", fmt.Sprintf("/api/v1/containers/%d/stop", created.ID), nil, &again)
	if w.Code != 200 || again.Status != "stopped" {
		t.Fatalf("expected 200 stopped on the repeated stop, got %d %s", w.Code, w.Body.String())
	}
	if again.Version != stopped.Version {
		t.Fatalf("the repeated stop must not bump the version")
	}

	// a pending container is genuinely not stoppable
	pending := createContainer(t, r, map[string]any{"name": uniqueName("stop-pending"), "image": "nginx"})
	w = apitest.DoJSON(t, r, "POST", fmt.Sprintf("/api/v1/containers/%d/stop", pending.ID), nil, &errResp)
	if w.Code != 409 {
		t.Fatalf("expected 409 for a pending container, got %d", w.Code)
	}
}

func TestRemoveContainer(t *testing.T) {
	r := newTestEngine(t)

	created := createContainer(t, r, map[string]any{"name": uniqueName("rm"), "image": "nginx"})
	path := fmt.Sprintf("/api/v1/containers/%d", created.ID)

	w := apitest.DoJSON(t, r, "DELETE", path, nil, nil)
	if w.Code != 204 {
		t.Fatalf("expected 204, got %d", w.Code)
	}

	var errResp apitest.ErrorEnvelope
	w = apitest.DoJSON(t, r, "DELETE", path, nil, &errResp)
	if w.Code != 404 || errResp.Error.Code != "not_found" {
		t.Fatalf("expected 404 on repeated delete, got %d %s", w.Code, w.Body.String())
	}

	w = apitest.DoJSON(t, r, "POST", path+"/stop", nil, &errResp)
	if w.Code != 404 {
		t.Fatalf("expected 404 stopping a removed container, got %d", w.Code)
	}
}
