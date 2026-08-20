package http

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	pgcommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/pgcommon"
)

// Pinger is satisfied by any dependency this adapter must check for
// readiness with a plain liveness probe (port.Cache already implements it).
type Pinger interface {
	Ping(ctx context.Context) error
}

// PostgresHealth is satisfied by *pgcommon.Pool. Used for the Postgres check
// specifically instead of the plain Pinger: pgcommon.Pool.Health's own doc
// comment recommends exposing it at /healthz or /readyz precisely because it
// carries pool connection stats (total/idle/acquired/max/utilization)
// alongside the liveness ping — a bare up/down bool discards exactly the
// data that would help diagnose pool exhaustion during an incident.
type PostgresHealth interface {
	Health(ctx context.Context) pgcommon.HealthStatus
}

type healthHandlers struct {
	postgres PostgresHealth
	cache    Pinger
}

// readyz checks Postgres and Valkey. Postgres is on the critical path for
// every route and reports 503 if unreachable. Valkey is not: TAC-4 falls
// through to Postgres on a cache miss or Valkey error (higher latency, not
// an outage — TAC-FAIL-2), so a Valkey failure is reported as "degraded"
// without flipping overall readiness — matching ARCHITECTURE.md's Cache
// Strategy section. It deliberately does NOT check membershipcheck (LLD
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

	hs := h.postgres.Health(ctx)
	if hs.Healthy {
		checks["postgres"] = "ok"
	} else {
		checks["postgres"] = "error"
		healthy = false
	}
	checks["postgres_pool"] = gin.H{
		"total_conns":    hs.TotalConns,
		"idle_conns":     hs.IdleConns,
		"acquired_conns": hs.AcquiredConns,
		"max_conns":      hs.MaxConns,
		"utilization":    hs.Utilization,
	}

	if err := h.cache.Ping(ctx); err != nil {
		checks["valkey"] = "degraded"
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
