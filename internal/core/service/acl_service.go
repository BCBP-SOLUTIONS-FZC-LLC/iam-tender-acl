// Package service implements this service's business logic (TAC-1..TAC-4)
// against the ports declared in internal/core/port — no direct dependency
// on Postgres, Valkey, or any outbound HTTP client. Concrete adapters are
// injected by cmd/tender-acl's composition root.
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/port"
)

// maxReasonLength enforces the LLD §7.2.1 `reason` CHECK — an intentional
// addition beyond iam-org-membership's original schema, which left the
// column an unbounded text field.
const maxReasonLength = 500

// ACLMetrics is the minimal slice of *metrics.Metrics
// (internal/adapter/outbound/metrics) ACLService needs, kept local so
// core/service never imports an adapter package directly.
type ACLMetrics interface {
	RecordGrantCheck(ctx context.Context, status string)
	RecordCheckCall(ctx context.Context, status string)
	RecordWrite(ctx context.Context, op, result string)
	RecordCacheHit(ctx context.Context)
	RecordCacheMiss(ctx context.Context)
}

// ACLService implements TAC-1..TAC-4, near-verbatim from
// iam-org-membership's (now-deleted) internal/core/service/
// tender_acl_service.go, with Grant repointed at
// port.MembershipCheckClient in place of the original in-process
// s.memberships.FindByUserID call (LLD §7.6.2).
type ACLService struct {
	repo    port.TenderACLRepository
	checker port.MembershipCheckClient
	cache   port.Cache
	metrics ACLMetrics
	logger  port.SlogStyleLogger
	tracer  trace.Tracer
}

// NewACLService wires an ACLService from its ports.
func NewACLService(repo port.TenderACLRepository, checker port.MembershipCheckClient, cache port.Cache, metrics ACLMetrics, logger port.SlogStyleLogger, tracer trace.Tracer) *ACLService {
	return &ACLService{repo: repo, checker: checker, cache: cache, metrics: metrics, logger: logger, tracer: tracer}
}

// List implements TAC-1.
func (s *ACLService) List(ctx context.Context, tenantID, tenderID uuid.UUID) ([]domain.TenderACLEntry, error) {
	ctx, span := s.tracer.Start(ctx, "service.ACLService.List")
	defer span.End()

	entries, err := s.repo.List(ctx, tenantID, tenderID)
	if err != nil {
		return nil, fmt.Errorf("acl: list: %w", err)
	}
	return entries, nil
}

// Grant implements TAC-2. The grant-time membership check
// (s.checker.Exists) is this service's one synchronous outbound dependency
// (LLD §7.6.2) — it blocks only new grants; TAC-1/TAC-3/TAC-4 never call
// it (TAC-D3).
func (s *ACLService) Grant(
	ctx context.Context,
	tenantID, tenderID, userID, grantedBy uuid.UUID,
	level domain.TenderACLLevel,
	reason string,
	expiresAt *time.Time,
) (domain.TenderACLEntry, error) {
	ctx, span := s.tracer.Start(ctx, "service.ACLService.Grant")
	defer span.End()

	if !level.Valid() {
		return domain.TenderACLEntry{}, domain.NewError(domain.ErrCodeInvalidAccessLevel, "access_level must be one of view, edit, approve")
	}
	if len(reason) > maxReasonLength {
		return domain.TenderACLEntry{}, domain.NewError(domain.ErrCodeInvalidReason, "reason must be 500 characters or fewer")
	}
	if expiresAt != nil && !expiresAt.After(time.Now()) {
		return domain.TenderACLEntry{}, domain.NewError(domain.ErrCodeInvalidExpiry, "expires_at must be in the future")
	}

	active, membershipID, err := s.checker.Exists(ctx, tenantID, userID)
	if err != nil {
		s.metrics.RecordGrantCheck(ctx, "unavailable")
		return domain.TenderACLEntry{}, domain.NewError(domain.ErrCodeCoreUnavailable, "membership check unavailable: "+err.Error())
	}
	if !active {
		s.metrics.RecordGrantCheck(ctx, "not_active")
		// Collapses iam-org-membership's today-distinct
		// member_not_found(404)/member_not_active(422) into one code: the
		// new provider contract only distinguishes active/not-active.
		return domain.TenderACLEntry{}, domain.NewError(domain.ErrCodeGranteeNotActiveMember, "grantee does not hold an active tenant membership")
	}
	s.metrics.RecordGrantCheck(ctx, "active")

	entry := domain.TenderACLEntry{
		TenantID:           tenantID,
		TenderID:           tenderID,
		UserID:             userID,
		TenantMembershipID: membershipID,
		AccessLevel:        level,
		GrantedBy:          grantedBy,
		Reason:             reason,
		ExpiresAt:          expiresAt,
	}

	created, err := s.repo.Grant(ctx, entry)
	if err != nil {
		s.metrics.RecordWrite(ctx, "grant", "error")
		return domain.TenderACLEntry{}, err
	}
	s.metrics.RecordWrite(ctx, "grant", "success")

	if err := s.cache.Delete(ctx, tenantID, tenderID, userID); err != nil {
		s.logger.WarnContext(ctx, "acl check cache invalidation failed after grant", "error", err.Error())
	}

	return created, nil
}

// Revoke implements TAC-3: a soft-delete optimistic-locked on the caller's
// last-read record_version (LLD §11.2/§12.1) — a version mismatch,
// including one caused by the row already being revoked since it was last
// read, surfaces as domain.ErrCodeOptimisticLockConflict (409) rather than
// a silent no-op, matching the LLD's sequence diagram exactly.
func (s *ACLService) Revoke(ctx context.Context, tenantID, tenderID, userID uuid.UUID, expectedVersion int64) error {
	ctx, span := s.tracer.Start(ctx, "service.ACLService.Revoke")
	defer span.End()

	if err := s.repo.Revoke(ctx, tenantID, tenderID, userID, expectedVersion); err != nil {
		s.metrics.RecordWrite(ctx, "revoke", "error")
		return fmt.Errorf("acl: revoke: %w", err)
	}
	s.metrics.RecordWrite(ctx, "revoke", "success")

	// LLD §9: DELETE, not update, so a process crash mid-write can never
	// leave a stale has_access:true value being served.
	if err := s.cache.Delete(ctx, tenantID, tenderID, userID); err != nil {
		s.logger.WarnContext(ctx, "acl check cache invalidation failed after revoke", "error", err.Error())
	}
	return nil
}

// CheckAccess implements TAC-4/I-12. Never returns a not-found error — no
// active grant is a valid, cacheable answer (has_access:false), not a 404.
func (s *ACLService) CheckAccess(ctx context.Context, tenantID, tenderID, userID uuid.UUID) (domain.CachedAccess, error) {
	ctx, span := s.tracer.Start(ctx, "service.ACLService.CheckAccess")
	defer span.End()

	if cached, hit, err := s.cache.Get(ctx, tenantID, tenderID, userID); err != nil {
		// A Valkey error is a distinct failure mode (LLD §9.4) from an
		// ordinary miss — logged, but counted in neither cache metric.
		s.logger.WarnContext(ctx, "acl check cache read failed, falling back to postgres", "error", err.Error())
	} else if hit {
		s.metrics.RecordCacheHit(ctx)
		s.metrics.RecordCheckCall(ctx, statusFor(cached.HasAccess))
		return *cached, nil
	} else {
		s.metrics.RecordCacheMiss(ctx)
	}

	entry, err := s.repo.FindActive(ctx, tenantID, tenderID, userID)
	if err != nil {
		return domain.CachedAccess{}, fmt.Errorf("acl: check access: %w", err)
	}

	var result domain.CachedAccess
	if entry != nil {
		level := string(entry.AccessLevel)
		result = domain.CachedAccess{HasAccess: true, AccessLevel: &level, ExpiresAt: entry.ExpiresAt}
	} else {
		result = domain.CachedAccess{HasAccess: false}
	}
	s.metrics.RecordCheckCall(ctx, statusFor(result.HasAccess))

	if err := s.cache.Set(ctx, tenantID, tenderID, userID, result); err != nil {
		s.logger.WarnContext(ctx, "acl check cache populate failed", "error", err.Error())
	}
	return result, nil
}

func statusFor(hasAccess bool) string {
	if hasAccess {
		return "has_access"
	}
	return "no_access"
}
