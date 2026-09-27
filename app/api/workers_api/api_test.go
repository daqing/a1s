package workers_api

import (
	"fmt"
	"testing"
	"time"

	"github.com/daqing/a1s/app/api/apitest"
	"github.com/gin-gonic/gin"
)

type apiWorker struct {
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	Address         string  `json:"address"`
	Status          string  `json:"status"`
	LastHeartbeatAt *string `json:"last_heartbeat_at"`
	Version         int64   `json:"version"`
}

func newTestEngine(t *testing.T) *gin.Engine {
	return apitest.Setup(t, func(e *gin.Engine) {
		Routes(e.Group("/api/v1"))
	})
}

func TestListWorkersHeartbeatOrder(t *testing.T) {
	r := newTestEngine(t)

	suffix := time.Now().UnixNano()
	fresh := fmt.Sprintf("t26-fresh-%d", suffix)
	stale := fmt.Sprintf("t26-stale-%d", suffix)

	apitest.Exec(t, `INSERT INTO workers (name, address, status, last_heartbeat_at)
		VALUES ($1, '10.0.0.2:9001', 'active', now())`, fresh)
	t.Cleanup(func() {
		apitest.Exec(t, "DELETE FROM workers WHERE name IN ($1, $2)", fresh, stale)
	})
	apitest.Exec(t, `INSERT INTO workers (name, address, status, last_heartbeat_at)
		VALUES ($1, '10.0.0.3:9001', 'lost', NULL)`, stale)

	type listResponse struct {
		Workers []apiWorker `json:"workers"`
	}

	var got listResponse
	w := apitest.DoJSON(t, r, "GET", "/api/v1/workers", nil, &got)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	pos := map[string]int{}
	for idx, wk := range got.Workers {
		pos[wk.Name] = idx
	}

	freshPos, okFresh := pos[fresh]
	stalePos, okStale := pos[stale]
	if !okFresh || !okStale {
		t.Fatalf("expected both seeded workers in the list, got %#v", pos)
	}
	if freshPos >= stalePos {
		t.Fatalf("expected fresh heartbeat first: %s (at %d) must precede %s (at %d)",
			fresh, freshPos, stale, stalePos)
	}

	for _, wk := range got.Workers {
		if wk.Name == fresh {
			if wk.Status != "active" || wk.LastHeartbeatAt == nil {
				t.Fatalf("unexpected fresh worker row: %#v", wk)
			}
		}
		if wk.Name == stale {
			if wk.Status != "lost" || wk.LastHeartbeatAt != nil {
				t.Fatalf("unexpected stale worker row: %#v", wk)
			}
		}
	}
}
