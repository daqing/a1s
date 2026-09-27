package workers_api

import "github.com/gin-gonic/gin"

// Routes registers the public worker endpoints on the /api/v1 group
// (see docs/api.md).
func Routes(g *gin.RouterGroup) {
	g.GET("/workers", ListAction)
}
