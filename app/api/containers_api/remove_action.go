package containers_api

import (
	"fmt"
	"strconv"

	"github.com/daqing/a1s/app/api/respond"
	"github.com/daqing/a1s/app/models"
	"github.com/daqing/airway/lib/repo"
	buildingsql "github.com/daqing/airway/lib/sql"
	"github.com/gin-gonic/gin"
)

// RemoveAction handles DELETE /api/v1/containers/:id: the row is removed
// outright, so a repeated DELETE is a 404 (docs/api.md). Every other actor
// re-reads on conflict, so no version guard is needed here.
func RemoveAction(c *gin.Context) {
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

	if err := repo.DeleteByID[models.Container](buildingsql.IdType(id)); err != nil {
		respond.Internal(c, err)
		return
	}

	c.Status(204)
}
