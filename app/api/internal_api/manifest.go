package internal_api

import (
	"log"
	"strconv"

	"github.com/daqing/a1s/app/api/respond"
	"github.com/daqing/a1s/app/models"
	"github.com/daqing/airway/lib/repo"
	buildingsql "github.com/daqing/airway/lib/sql"
	"github.com/gin-gonic/gin"
)

type manifestEntry struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

type manifestRequest struct {
	Containers []manifestEntry `json:"containers"`
}

// ManifestAction handles PUT /api/v1/internal/workers/:id/manifest: the
// worker reports every a1s-labeled container it actually has, and the API
// repairs drift between that reality and the desired state:
//
//   - rows scheduled/running on this worker missing from the manifest are
//     re-queued to pending (the runtime vanished behind the system's back)
//   - rows stopped on this worker whose runtime still runs get a fresh stop
//     command (the original one was lost)
func ManifestAction(c *gin.Context) {
	workerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || workerID <= 0 {
		respond.NotFound(c, "worker not found")
		return
	}

	var req manifestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respond.Validation(c, err.Error())
		return
	}

	reported := make(map[int64]string, len(req.Containers))
	for _, entry := range req.Containers {
		reported[entry.ID] = entry.Status
	}

	assigned, err := repo.Find[models.Container](repo.CurrentDB(),
		buildingsql.SelectColumns("id", "version", "status").
			From("containers").
			Where(buildingsql.FieldEq(buildingsql.FieldFor(models.Container{}, "worker_id"), workerID)))
	if err != nil {
		respond.Internal(c, err)
		return
	}

	repaired := 0

	for _, row := range assigned {
		status, known := reported[row.ID]

		switch {
		case !known && (row.Status == models.ContainerScheduled || row.Status == models.ContainerRunning):
			// the runtime is gone: re-queue the row for rescheduling
			if reconcileMissing(row) {
				repaired++
				log.Printf("reconcile: container %d missing on worker %d, requeued", row.ID, workerID)
			}

		case known && row.Status == models.ContainerStopped && status == models.ContainerRunning:
			// the desired state is stopped but the runtime keeps running:
			// re-issue the stop command (the original was lost)
			queued, err := models.QueueStopCommand(row.ID, workerID)
			if err != nil {
				log.Printf("reconcile: stop command for container %d failed: %v", row.ID, err)
				continue
			}

			if queued {
				repaired++
				log.Printf("reconcile: container %d stopped but running on worker %d, stop re-issued", row.ID, workerID)
			}
		}
	}

	// second pass over the manifest itself: a running runtime whose row says
	// stopped (the row may belong to a dead worker, invisible to the
	// assigned-rows pass above) still needs its stop
	for _, entry := range req.Containers {
		if entry.Status != models.ContainerRunning {
			continue
		}

		if _, known := reported[entry.ID]; !known {
			continue
		}

		// FindByID yields (nil, nil) when the row is missing (ghost cleanup
		// happens worker-side on the status 404).
		row, err := repo.FindByID[models.Container](buildingsql.IdType(entry.ID))
		if err != nil {
			respond.Internal(c, err)
			return
		}

		if row == nil || row.Status != models.ContainerStopped {
			continue
		}

		queued, err := models.QueueStopCommand(row.ID, workerID)
		if err != nil {
			log.Printf("reconcile: cross-worker stop for container %d failed: %v", row.ID, err)
			continue
		}

		if queued {
			repaired++
			log.Printf("reconcile: container %d stopped on worker %d but running here, stop re-issued", row.ID, workerID)
		}
	}

	c.JSON(200, gin.H{"worker_id": workerID, "repaired": repaired})
}

// reconcileMissing resets one drifted row to pending, version-guarded. Rows
// with an in-flight start command are skipped: their runtime may
// legitimately not exist yet.
func reconcileMissing(row *models.Container) bool {
	inFlight, err := models.HasInFlightStart(row.ID)
	if err != nil {
		log.Printf("reconcile: in-flight check for container %d failed: %v", row.ID, err)
		return false
	}

	if inFlight {
		log.Printf("reconcile: container %d has an in-flight start command, not missing", row.ID)
		return false
	}

	var t models.Container

	affected, err := repo.UpdateAffected(repo.CurrentDB(),
		buildingsql.UpdateTable(buildingsql.TableFor(t)).Set(buildingsql.H{
			"status":     models.ContainerPending,
			"worker_id":  nil,
			"version":    buildingsql.Op(buildingsql.Column("version"), "+", 1),
			"updated_at": buildingsql.Func("now"),
		}).Where(buildingsql.AllOf(
			buildingsql.FieldEq(buildingsql.FieldFor(t, "id"), row.ID),
			buildingsql.FieldEq(buildingsql.FieldFor(t, "version"), row.Version),
		)))
	if err != nil {
		log.Printf("reconcile: container %d requeue failed: %v", row.ID, err)
		return false
	}

	return affected == 1
}
