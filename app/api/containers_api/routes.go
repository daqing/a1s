package containers_api

import "github.com/gin-gonic/gin"

// Routes registers the public container endpoints on the /api/v1 group
// (see docs/api.md).
func Routes(g *gin.RouterGroup) {
	g.POST("/containers", CreateAction)
}
