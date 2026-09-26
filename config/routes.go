package config

import (
	"github.com/gin-gonic/gin"

	"github.com/daqing/a1s/app/api/health_api"
	"github.com/daqing/airway/lib/plugin"
)

// Phase 0 trim: the scaffold's home page, WebSocket, storage API, and
// OpenAPI document routes are no longer registered — until the REST API
// lands, the HTTP surface is the health check plus plugin mounts. The
// OpenAPI document is planned to return in Phase 6 (see docs/ROADMAP.md);
// the unregistered scaffold packages themselves are removed by task T0.3.
func Routes(r *gin.Engine) {
	PublicRoutes(r)
	HealthRoutes(r)
}

// PublicRoutes registers the user-facing routes. When a URL_PREFIX is
// configured these answer only under the prefix; see App.Handler.
func PublicRoutes(r *gin.Engine) {
	plugin.MountAll(r)
}

// HealthRoutes registers the internal health-check route. It stays reachable at
// the unprefixed root (for load-balancer probes) even when the public routes
// are served under a URL_PREFIX.
func HealthRoutes(r *gin.Engine) {
	health_api.Routes(r)
}
