package internal_api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/daqing/a1s/app/api/respond"
	"github.com/daqing/a1s/app/models"
	"github.com/daqing/airway/lib/repo"
	buildingsql "github.com/daqing/airway/lib/sql"
	"github.com/gin-gonic/gin"
)

type commandJSON struct {
	ID          int64        `json:"id"`
	Action      string       `json:"action"`
	ContainerID *int64       `json:"container_id"`
	Payload     models.JSONB `json:"payload"`
}

func toCommandJSON(c *models.Command) commandJSON {
	return commandJSON{
		ID:          c.ID,
		Action:      c.Action,
		ContainerID: c.ContainerID,
		Payload:     c.Payload,
	}
}

// ListCommandsAction handles GET /api/v1/internal/workers/:id/commands:
// hands out the worker's queued commands in FIFO order and marks each
// delivered. A command that loses the delivered race stays queued for the
// next poll.
func ListCommandsAction(c *gin.Context) {
	workerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || workerID <= 0 {
		respond.NotFound(c, "worker not found")
		return
	}

	var t models.Command
	b := buildingsql.All(t).
		Where(buildingsql.AllOf(
			buildingsql.FieldEq(buildingsql.FieldFor(t, "worker_id"), workerID),
			buildingsql.FieldEq(buildingsql.FieldFor(t, "status"), models.CommandQueued),
		)).
		OrderBy("id ASC")

	rows, err := repo.Find[models.Command](repo.CurrentDB(), b)
	if err != nil {
		respond.Internal(c, err)
		return
	}

	out := make([]commandJSON, 0, len(rows))
	for _, row := range rows {
		if _, err := models.UpdateWhereVersion[models.Command](row.ID, row.Version, buildingsql.H{
			"status": models.CommandDelivered,
		}); err != nil {
			if errors.Is(err, models.ErrVersionConflict) {
				continue
			}

			respond.Internal(c, err)
			return
		}

		out = append(out, toCommandJSON(row))
	}

	c.JSON(200, gin.H{"commands": out})
}

type commandResultRequest struct {
	OK     bool            `json:"ok"`
	Error  string          `json:"error"`
	Detail json.RawMessage `json:"detail"`
}

// CommandResultAction handles POST /api/v1/internal/commands/:id/result:
// marks the command done and stores the reported result verbatim.
func CommandResultAction(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		respond.NotFound(c, "command not found")
		return
	}

	var req commandResultRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respond.Validation(c, err.Error())
		return
	}

	result := map[string]any{"ok": req.OK}
	if req.Error != "" {
		result["error"] = req.Error
	}
	if len(req.Detail) > 0 {
		result["detail"] = req.Detail
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		respond.Internal(c, err)
		return
	}

	// FindByID yields (nil, nil) when the row is missing.
	row, err := repo.FindByID[models.Command](buildingsql.IdType(id))
	if err != nil {
		respond.Internal(c, err)
		return
	}

	if row == nil {
		respond.NotFound(c, fmt.Sprintf("command %d not found", id))
		return
	}

	if row.Status == models.CommandDone {
		respond.Conflict(c, fmt.Sprintf("command %d is already done", id))
		return
	}

	if _, err := models.UpdateWhereVersion[models.Command](row.ID, row.Version, buildingsql.H{
		"status": models.CommandDone,
		"result": models.JSONB(encoded),
	}); err != nil {
		if errors.Is(err, models.ErrVersionConflict) {
			respond.Conflict(c, fmt.Sprintf("command %d changed concurrently, re-read and retry", id))
			return
		}

		respond.Internal(c, err)
		return
	}

	c.JSON(200, gin.H{"id": row.ID, "status": models.CommandDone})
}
