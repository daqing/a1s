package internal_api

import (
	"errors"
	"strconv"

	"github.com/daqing/a1s/app/api/respond"
	"github.com/daqing/a1s/app/models"
	"github.com/daqing/airway/lib/repo"
	buildingsql "github.com/daqing/airway/lib/sql"
	"github.com/gin-gonic/gin"
)

type statusReportRequest struct {
	Status string `json:"status"`
}

// validReportedStatuses are the observed statuses a worker may report.
var validReportedStatuses = map[string]bool{
	models.ContainerRunning: true,
	models.ContainerStopped: true,
	models.ContainerFailed:  true,
}

// reportableFrom are the source statuses an observed report may transition:
// the reporter reflects reality but must not trample desired-state
// transitions (a row the API set to stopped stays stopped) nor touch rows
// the scheduler has not scheduled yet (docs/state-model.md).
var reportableFrom = map[string]bool{
	models.ContainerScheduled: true,
	models.ContainerRunning:   true,
}

// StatusReportAction handles PUT /api/v1/internal/containers/:id/status:
// applies an observed worker report to the container row under the version
// lock. Same-status reports are no-ops; reports that cannot legally apply
// answer 200 with applied=false so the worker does not retry-storm.
func StatusReportAction(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		respond.NotFound(c, "container not found")
		return
	}

	var req statusReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respond.Validation(c, err.Error())
		return
	}

	if !validReportedStatuses[req.Status] {
		respond.Validation(c, "status must be one of running, stopped, failed")
		return
	}

	// FindByID yields (nil, nil) when the row is missing.
	row, err := repo.FindByID[models.Container](buildingsql.IdType(id))
	if err != nil {
		respond.Internal(c, err)
		return
	}

	if row == nil {
		respond.NotFound(c, "container not found")
		return
	}

	if row.Status == req.Status {
		c.JSON(200, gin.H{"id": row.ID, "status": row.Status, "applied": false})
		return
	}

	if !reportableFrom[row.Status] {
		c.JSON(200, gin.H{"id": row.ID, "status": row.Status, "applied": false,
			"reason": "status " + row.Status + " does not accept worker reports"})
		return
	}

	vals := buildingsql.H{"status": req.Status}
	if req.Status == models.ContainerRunning {
		// a successful (re)start clears the failure streak, so
		// on-failure:N policies count consecutive failures only
		vals["restart_count"] = 0
	}

	if _, err := models.UpdateWhereVersion[models.Container](row.ID, row.Version, vals); err != nil {
		if errors.Is(err, models.ErrVersionConflict) {
			respond.Conflict(c, "container changed concurrently, re-read and retry")
			return
		}

		respond.Internal(c, err)
		return
	}

	c.JSON(200, gin.H{"id": row.ID, "status": req.Status, "applied": true})
}
