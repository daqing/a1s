package respond

import (
	"github.com/gin-gonic/gin"
)

// Error writes the shared error envelope defined in docs/api.md.
func Error(c *gin.Context, status int, code string, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"code":    code,
			"message": message,
		},
	})
}

// Validation writes a 400 validation_error response.
func Validation(c *gin.Context, message string) {
	Error(c, 400, "validation_error", message)
}

// NotFound writes a 404 not_found response.
func NotFound(c *gin.Context, message string) {
	Error(c, 404, "not_found", message)
}

// Conflict writes a 409 conflict response.
func Conflict(c *gin.Context, message string) {
	Error(c, 409, "conflict", message)
}

// Internal writes a 500 internal_error response.
func Internal(c *gin.Context, err error) {
	Error(c, 500, "internal_error", err.Error())
}
