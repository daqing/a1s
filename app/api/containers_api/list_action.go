package containers_api

import (
	"fmt"

	"github.com/daqing/a1s/app/api/respond"
	"github.com/daqing/a1s/app/models"
	"github.com/daqing/airway/lib/repo"
	buildingsql "github.com/daqing/airway/lib/sql"
	"github.com/gin-gonic/gin"
)

var containerStatuses = map[string]bool{
	models.ContainerPending:   true,
	models.ContainerScheduled: true,
	models.ContainerRunning:   true,
	models.ContainerStopped:   true,
	models.ContainerFailed:    true,
	models.ContainerLost:      true,
}

// ListAction handles GET /api/v1/containers: newest first, optional status
// filter (docs/api.md).
func ListAction(c *gin.Context) {
	var t models.Container
	b := buildingsql.All(t).OrderBy("id DESC")

	if status := c.Query("status"); status != "" {
		if !containerStatuses[status] {
			respond.Validation(c, fmt.Sprintf("unknown status %q", status))
			return
		}

		b = b.Where(buildingsql.FieldEq(buildingsql.FieldFor(t, "status"), status))
	}

	rows, err := repo.Find[models.Container](repo.CurrentDB(), b)
	if err != nil {
		respond.Internal(c, err)
		return
	}

	out := make([]containerJSON, 0, len(rows))
	for _, row := range rows {
		out = append(out, toContainerJSON(row))
	}

	c.JSON(200, gin.H{"containers": out})
}
