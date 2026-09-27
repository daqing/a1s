// Package internal_api hosts the worker-facing endpoints under
// /api/v1/internal, guarded by the shared bearer token (docs/api.md).
package internal_api

import (
	"github.com/daqing/a1s/app/middlewares"
	"github.com/gin-gonic/gin"
)

// Routes registers the internal endpoints on the passed group, which must
// already live under /api/v1/internal.
func Routes(g *gin.RouterGroup) {
	g.POST("/heartbeat", HeartbeatAction)
	g.GET("/workers/:id/commands", ListCommandsAction)
	g.POST("/commands/:id/result", CommandResultAction)
	g.PUT("/containers/:id/status", StatusReportAction)
	g.PUT("/workers/:id/manifest", ManifestAction)
}

// Mount registers the internal group under the /api/v1 group with the
// bearer-token middleware applied.
func Mount(v1 *gin.RouterGroup) {
	internal := v1.Group("/internal")
	internal.Use(middlewares.InternalAuth())
	Routes(internal)
}
