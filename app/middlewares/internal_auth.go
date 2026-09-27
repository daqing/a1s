// Package middlewares holds shared Gin middlewares.
package middlewares

import (
	"crypto/subtle"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// InternalAuth guards the /api/v1/internal group with the shared bearer
// token from A1S_INTERNAL_TOKEN. The token is read per request so tests can
// set it without restarting. When the server has no token configured — or
// the request carries none — every internal request is rejected with 401.
func InternalAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := os.Getenv("A1S_INTERNAL_TOKEN")
		if token == "" {
			c.AbortWithStatusJSON(401, gin.H{
				"error": gin.H{"code": "unauthorized", "message": "internal token is not configured"},
			})
			return
		}

		auth := c.GetHeader("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) ||
			subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, prefix)), []byte(token)) != 1 {
			c.AbortWithStatusJSON(401, gin.H{
				"error": gin.H{"code": "unauthorized", "message": "invalid or missing bearer token"},
			})
			return
		}

		c.Next()
	}
}
