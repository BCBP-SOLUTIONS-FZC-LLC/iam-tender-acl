package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePinger is a Pinger whose Ping result is set per-test.
type fakePinger struct{ err error }

func (f fakePinger) Ping(_ context.Context) error { return f.err }

func readyzCtx() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/readyz", http.NoBody)
	return c, w
}

func TestReadyz_BothHealthy_Returns200(t *testing.T) {
	h := &healthHandlers{postgres: fakePinger{}, cache: fakePinger{}}
	c, w := readyzCtx()

	h.readyz(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "ok", body["status"])
	checks, _ := body["checks"].(map[string]any)
	assert.Equal(t, "ok", checks["postgres"])
	assert.Equal(t, "ok", checks["valkey"])
}

func TestReadyz_PostgresDown_Returns503(t *testing.T) {
	h := &healthHandlers{postgres: fakePinger{err: errors.New("connection refused")}, cache: fakePinger{}}
	c, w := readyzCtx()

	h.readyz(c)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "error", body["status"])
	checks, _ := body["checks"].(map[string]any)
	assert.Equal(t, "error", checks["postgres"])
}

// TestReadyz_ValkeyDown_StaysReady asserts TAC-FAIL-2: Valkey is not on the
// critical path (TAC-4 falls through to Postgres on a cache miss/error), so
// a Valkey failure must be reported as "degraded" without failing
// readiness — matching ARCHITECTURE.md's Cache Strategy section. A
// regression here would pull healthy pods out of rotation on a Valkey blip.
func TestReadyz_ValkeyDown_StaysReady(t *testing.T) {
	h := &healthHandlers{postgres: fakePinger{}, cache: fakePinger{err: errors.New("connection refused")}}
	c, w := readyzCtx()

	h.readyz(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "ok", body["status"])
	checks, _ := body["checks"].(map[string]any)
	assert.Equal(t, "ok", checks["postgres"])
	assert.Equal(t, "degraded", checks["valkey"])
}

func TestReadyz_BothDown_Returns503(t *testing.T) {
	h := &healthHandlers{
		postgres: fakePinger{err: errors.New("connection refused")},
		cache:    fakePinger{err: errors.New("connection refused")},
	}
	c, w := readyzCtx()

	h.readyz(c)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "error", body["status"])
	checks, _ := body["checks"].(map[string]any)
	assert.Equal(t, "error", checks["postgres"])
	assert.Equal(t, "degraded", checks["valkey"])
}
