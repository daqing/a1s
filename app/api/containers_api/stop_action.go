package containers_api

import (
	"errors"
	"fmt"
	"log"
	"strconv"

	"github.com/daqing/a1s/app/api/respond"
	"github.com/daqing/a1s/app/models"
	"github.com/daqing/airway/lib/repo"
	buildingsql "github.com/daqing/airway/lib/sql"
	"github.com/gin-gonic/gin"
)

// StopAction handles POST /api/v1/containers/:id/stop: the desired-state
// transition scheduled|running → stopped, guarded by the version
// (docs/api.md).
func StopAction(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		respond.NotFound(c, "container not found")
		return
	}

	// FindByID yields (nil, nil) when the row is missing.
	row, err := repo.FindByID[models.Container](buildingsql.IdType(id))
	if err != nil {
		respond.Internal(c, err)
		return
	}

	if row == nil {
		respond.NotFound(c, fmt.Sprintf("container %d not found", id))
		return
	}

	if row.Status != models.ContainerScheduled && row.Status != models.ContainerRunning {
		respond.Conflict(c, fmt.Sprintf("container %d is %s, not stoppable", id, row.Status))
		return
	}

	_, err = models.UpdateWhereVersion[models.Container](row.ID, row.Version, buildingsql.H{
		"status": models.ContainerStopped,
	})
	if err != nil {
		if errors.Is(err, models.ErrVersionConflict) {
			respond.Conflict(c, fmt.Sprintf("container %d changed concurrently, re-read and retry", id))
			return
		}

		respond.Internal(c, err)
		return
	}

	// the row carries the desired state; the owning worker learns about the
	// stop through the command channel and kills the runtime task
	if row.WorkerID != nil {
		if _, err := models.QueueStopCommand(row.ID, *row.WorkerID); err != nil {
			// the reconcile pass re-issues lost stop commands, so a failed
			// queue must not fail the API call
			log.Printf("queue stop command for container %d: %v", row.ID, err)
		}
	}

	updated, err := repo.FindByID[models.Container](buildingsql.IdType(id))
	if err != nil {
		respond.Internal(c, err)
		return
	}

	if updated == nil {
		respond.NotFound(c, fmt.Sprintf("container %d not found", id))
		return
	}

	c.JSON(200, toContainerJSON(updated))
}
