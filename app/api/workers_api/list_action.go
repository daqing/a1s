package workers_api

import (
	"time"

	"github.com/daqing/a1s/app/api/respond"
	"github.com/daqing/a1s/app/models"
	"github.com/daqing/airway/lib/repo"
	buildingsql "github.com/daqing/airway/lib/sql"
	"github.com/gin-gonic/gin"
)

// ListAction handles GET /api/v1/workers: most recently heartbeating first
// (docs/api.md).
func ListAction(c *gin.Context) {
	var t models.Worker
	b := buildingsql.All(t).OrderBy("last_heartbeat_at DESC NULLS LAST")

	rows, err := repo.Find[models.Worker](repo.CurrentDB(), b)
	if err != nil {
		respond.Internal(c, err)
		return
	}

	out := make([]workerJSON, 0, len(rows))
	for _, row := range rows {
		out = append(out, toWorkerJSON(row))
	}

	c.JSON(200, gin.H{"workers": out})
}

// workerJSON is the wire representation of a worker (docs/api.md).
type workerJSON struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	Address         string     `json:"address"`
	Status          string     `json:"status"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	Version         int64      `json:"version"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func toWorkerJSON(w *models.Worker) workerJSON {
	return workerJSON{
		ID:              w.ID,
		Name:            w.Name,
		Address:         w.Address,
		Status:          w.Status,
		LastHeartbeatAt: utcPtr(w.LastHeartbeatAt),
		Version:         w.Version,
		CreatedAt:       w.CreatedAt.UTC(),
		UpdatedAt:       w.UpdatedAt.UTC(),
	}
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	u := t.UTC()
	return &u
}
