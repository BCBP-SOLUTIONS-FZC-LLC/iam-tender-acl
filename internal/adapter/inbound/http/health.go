package http

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Pinger is satisfied by any dependency this adapter must check for
// readiness (*pgcommon.Pool, port.Cache both already implement it).
type Pinger interface {
	Ping(ctx context.Context) error
}

type healthHandlers struct {
	postgres Pinger
	cache    Pinger
}

// healthz is a pure liveness check: if the process can answer HTTP at all,
// it reports ok. It never inspects dependencies.
//
// @Summary   Liveness check
// @Tags      infra
// @Produce   json
// @Success   200  {object}  map[string]string
// @Router    /healthz [get]
func (h *healthHandlers) healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// readyz checks Postgres and Valkey, and reports 503 if either is
// unreachable. It deliberately does NOT check membershipcheck (LLD
// TAC-FAIL-1/TAC-FAIL-3): Core's availability only affects TAC-2, never
// this service's own liveness/readiness posture.
//
// @Summary   Readiness check
// @Tags      infra
// @Produce   json
// @Success   200  {object}  map[string]interface{}
// @Failure   503  {object}  map[string]interface{}
// @Router    /readyz [get]
func (h *healthHandlers) readyz(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	healthy := true
	checks := gin.H{}

	if err := h.postgres.Ping(ctx); err != nil {
		checks["postgres"] = "error"
		healthy = false
	} else {
		checks["postgres"] = "ok"
	}

	if err := h.cache.Ping(ctx); err != nil {
		checks["valkey"] = "error"
		healthy = false
	} else {
		checks["valkey"] = "ok"
	}

	status := http.StatusOK
	overall := "ok"
	if !healthy {
		status = http.StatusServiceUnavailable
		overall = "error"
	}
	c.JSON(status, gin.H{"status": overall, "checks": checks})
}
