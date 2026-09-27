package internal_api

import (
	"errors"
	"time"

	"github.com/daqing/a1s/app/api/respond"
	"github.com/daqing/a1s/app/models"
	"github.com/daqing/airway/lib/repo"
	"github.com/daqing/airway/lib/validation"
	buildingsql "github.com/daqing/airway/lib/sql"
	"github.com/gin-gonic/gin"
)

type heartbeatRequest struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// HeartbeatAction handles POST /api/v1/internal/heartbeat: upserts the
// worker row by name and refreshes last_heartbeat_at. The worker reports
// itself active; a lost worker heartbeating again is its explicit
// re-registration (docs/state-model.md).
func HeartbeatAction(c *gin.Context) {
	var req heartbeatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respond.Validation(c, err.Error())
		return
	}

	if err := validation.Do("name", req.Name, "required"); err != nil {
		respond.Validation(c, "name is required")
		return
	}

	row, err := upsertHeartbeat(req.Name, req.Address)
	if err != nil {
		if errors.Is(err, models.ErrVersionConflict) {
			respond.Conflict(c, "heartbeat lost a version race, retry")
			return
		}

		respond.Internal(c, err)
		return
	}

	c.JSON(200, gin.H{
		"id":     row.ID,
		"name":   row.Name,
		"status": row.Status,
	})
}

// upsertHeartbeat refreshes the worker row under the version lock; a
// conflict (monitor racing, or a concurrent first heartbeat) is retried
// once on a fresh read.
func upsertHeartbeat(name, address string) (*models.Worker, error) {
	now := time.Now().UTC()

	for attempt := 0; attempt < 2; attempt++ {
		existing, err := repo.FindOneBy[models.Worker](buildingsql.H{"name": name})
		if err != nil {
			return nil, err
		}

		if existing == nil {
			created, err := repo.CreateFrom[models.Worker](buildingsql.H{
				"name":              name,
				"address":           address,
				"status":            models.WorkerActive,
				"last_heartbeat_at": now,
			})
			if err != nil {
				if nameExists(name) {
					continue // lost the first-heartbeat race; take the update path
				}

				return nil, err
			}

			return created, nil
		}

		if _, err := models.UpdateWhereVersion[models.Worker](existing.ID, existing.Version, buildingsql.H{
			"status":            models.WorkerActive,
			"last_heartbeat_at": now,
		}); err != nil {
			if errors.Is(err, models.ErrVersionConflict) {
				continue // raced with the monitor or another heartbeat
			}

			return nil, err
		}

		return &models.Worker{
			ID:              existing.ID,
			Name:            existing.Name,
			Address:         existing.Address,
			Status:          models.WorkerActive,
			LastHeartbeatAt: &now,
		}, nil
	}

	return nil, models.ErrVersionConflict
}

func nameExists(name string) bool {
	exists, err := repo.ExistsWhere[models.Worker](buildingsql.H{"name": name})
	return err == nil && exists
}
