package port

import (
	"context"

	"github.com/google/uuid"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
)

// Cache is the outbound port for the Valkey-backed TAC-4 result cache (LLD
// §9), implemented by internal/adapter/outbound/valkey.Cache.
type Cache interface {
	Get(ctx context.Context, tenantID, tenderID, userID uuid.UUID) (*domain.CachedAccess, bool, error)
	Set(ctx context.Context, tenantID, tenderID, userID uuid.UUID, value domain.CachedAccess) error
	// Delete evicts the cache entry. Invalidation is always a DELETE, never
	// an update-in-place (LLD §9) — a crash mid-write can never leave a
	// stale has_access:true value being served.
	Delete(ctx context.Context, tenantID, tenderID, userID uuid.UUID) error
	// Ping verifies connectivity, for use by readiness probes.
	Ping(ctx context.Context) error
}
