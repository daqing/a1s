// Package apitest wires database-backed handler tests: it skips the test
// unless A1S_TEST_DSN points at a test PostgreSQL, then provides an engine
// with the full route table and helpers for JSON requests and raw SQL.
package apitest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/daqing/airway/lib/repo"
	"github.com/gin-gonic/gin"
)

// Setup skips the test without A1S_TEST_DSN, otherwise installs the global
// database and returns a fresh engine with the routes registered by the
// caller (injecting them keeps this package free of import cycles with the
// endpoint packages under test).
func Setup(t *testing.T, register func(*gin.Engine)) *gin.Engine {
	t.Helper()

	dsn := os.Getenv("A1S_TEST_DSN")
	if dsn == "" {
		t.Skip("A1S_TEST_DSN not set; skipping database-backed test")
	}

	if _, err := repo.SetupDB(dsn); err != nil {
		t.Fatalf("setup test database: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	register(r)

	return r
}

// DoJSON performs a JSON request against the engine and decodes a non-empty
// response body into out (when non-nil).
func DoJSON(t *testing.T, r *gin.Engine, method, path string, body any, out any) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(b)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if out != nil && w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("decode response %q: %v", w.Body.String(), err)
		}
	}

	return w
}

// Exec runs a raw SQL statement against the test database.
func Exec(t *testing.T, query string, args ...any) {
	t.Helper()

	if _, err := repo.CurrentDB().Conn().Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// ErrorEnvelope mirrors the shared error response (docs/api.md).
type ErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
