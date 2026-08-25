//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/outbound/metrics"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/port"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/service"
)

type stubChecker struct {
	active       bool
	membershipID uuid.UUID
	err          error
}

func (s stubChecker) Exists(context.Context, uuid.UUID, uuid.UUID) (bool, uuid.UUID, error) {
	return s.active, s.membershipID, s.err
}

func testMetrics(t *testing.T) *metrics.Metrics {
	t.Helper()
	return sharedMetrics
}

// TestService_CheckAccess_CacheHit_ThenCacheMissAfterDelete exercises the
// real repo+cache round trip behind service.ACLService.CheckAccess
// (TAC-4): a grant populates Postgres, the first CheckAccess call
// populates Valkey, and Revoke's DELETE-based invalidation (LLD §9) means
// the very next call re-reads Postgres rather than serving a stale cached
// value.
func TestService_CheckAccess_CacheHit_ThenCacheMissAfterDelete(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantID, tenderID, userID := uuid.New(), uuid.New(), uuid.New()
	membershipID := uuid.New()

	svc := service.NewACLService(repo, stubChecker{active: true, membershipID: membershipID}, valkeyCache, testMetrics(t), port.SlogStyleLogger{}, otel.Tracer("test"))

	created, err := svc.Grant(ctx, tenantID, tenderID, userID, uuid.New(), domain.ACLApprove, "", nil)
	require.NoError(t, err)

	// First call: cache miss, falls through to Postgres, populates Valkey.
	result, err := svc.CheckAccess(ctx, tenantID, tenderID, userID)
	require.NoError(t, err)
	assert.True(t, result.HasAccess)
	require.NotNil(t, result.AccessLevel)
	assert.Equal(t, "approve", *result.AccessLevel)

	cachedBeforeRevoke, hit, err := valkeyCache.Get(ctx, tenantID, tenderID, userID)
	require.NoError(t, err)
	require.True(t, hit, "the first CheckAccess call must have populated Valkey")
	assert.True(t, cachedBeforeRevoke.HasAccess)

	require.NoError(t, svc.Revoke(ctx, tenantID, tenderID, userID, created.RecordVersion))

	_, hitAfterRevoke, err := valkeyCache.Get(ctx, tenantID, tenderID, userID)
	require.NoError(t, err)
	assert.False(t, hitAfterRevoke, "Revoke must DELETE the cache entry, not leave a stale has_access:true value")

	result2, err := svc.CheckAccess(ctx, tenantID, tenderID, userID)
	require.NoError(t, err)
	assert.False(t, result2.HasAccess)
}

func TestService_Grant_PersistsMembershipIDFromChecker(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantID, tenderID, userID := uuid.New(), uuid.New(), uuid.New()
	membershipID := uuid.New()

	svc := service.NewACLService(repo, stubChecker{active: true, membershipID: membershipID}, valkeyCache, testMetrics(t), port.SlogStyleLogger{}, otel.Tracer("test"))

	created, err := svc.Grant(ctx, tenantID, tenderID, userID, uuid.New(), domain.ACLView, "", nil)
	require.NoError(t, err)
	assert.Equal(t, membershipID, created.TenantMembershipID)
}

func TestService_Grant_CheckerNotActive_NoRowWritten(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantID, tenderID, userID := uuid.New(), uuid.New(), uuid.New()

	svc := service.NewACLService(repo, stubChecker{active: false}, valkeyCache, testMetrics(t), port.SlogStyleLogger{}, otel.Tracer("test"))

	_, err := svc.Grant(ctx, tenantID, tenderID, userID, uuid.New(), domain.ACLView, "", nil)
	require.Error(t, err)

	var count int
	require.NoError(t, adminPool.QueryRow(ctx, `SELECT count(*) FROM tender_acl_entries WHERE tenant_id = $1`, tenantID).Scan(&count))
	assert.Equal(t, 0, count, "a rejected grant must leave no partial row (TAC-FAIL-1)")
}
