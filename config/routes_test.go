package config

import (
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRoutesServesHealthOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	Routes(r)

	registered := map[string]bool{}
	for _, route := range r.Routes() {
		registered[route.Method+" "+route.Path] = true
	}

	if !registered["GET /health"] {
		t.Fatalf("expected GET /health to be registered, got %#v", registered)
	}

	dropped := []string{
		"GET /",
		"GET /openapi.json",
		"GET /ws",
		"POST /ws/publish",
		"POST /api/v1/storage",
		"GET /api/v1/storage/*key",
		"DELETE /api/v1/storage/*key",
	}

	for _, route := range dropped {
		if registered[route] {
			t.Fatalf("expected route %s to be dropped, got %#v", route, registered)
		}
	}
}
