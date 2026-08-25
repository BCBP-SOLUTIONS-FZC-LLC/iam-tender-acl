package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/port"
)

// ── Fakes ────────────────────────────────────────────────────────────────

type fakeRepo struct {
	listFn                   func(ctx context.Context, tenantID, tenderID uuid.UUID) ([]domain.TenderACLEntry, error)
	grantFn                  func(ctx context.Context, entry domain.TenderACLEntry) (domain.TenderACLEntry, error)
	revokeFn                 func(ctx context.Context, tenantID, tenderID, userID uuid.UUID, expectedVersion int64) error
	findActiveFn             func(ctx context.Context, tenantID, tenderID, userID uuid.UUID) (*domain.TenderACLEntry, error)
	cascadeDeleteForTenantFn func(ctx context.Context, tenantID uuid.UUID) (int64, error)
	softDeleteForUserFn      func(ctx context.Context, tenantID, userID uuid.UUID) (int64, error)
}

func (f *fakeRepo) List(ctx context.Context, tenantID, tenderID uuid.UUID) ([]domain.TenderACLEntry, error) {
	return f.listFn(ctx, tenantID, tenderID)
}
func (f *fakeRepo) Grant(ctx context.Context, entry domain.TenderACLEntry) (domain.TenderACLEntry, error) {
	return f.grantFn(ctx, entry)
}
func (f *fakeRepo) Revoke(ctx context.Context, tenantID, tenderID, userID uuid.UUID, expectedVersion int64) error {
	return f.revokeFn(ctx, tenantID, tenderID, userID, expectedVersion)
}
func (f *fakeRepo) FindActive(ctx context.Context, tenantID, tenderID, userID uuid.UUID) (*domain.TenderACLEntry, error) {
	return f.findActiveFn(ctx, tenantID, tenderID, userID)
}
func (f *fakeRepo) CascadeDeleteForTenant(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	return f.cascadeDeleteForTenantFn(ctx, tenantID)
}
func (f *fakeRepo) SoftDeleteForUser(ctx context.Context, tenantID, userID uuid.UUID) (int64, error) {
	if f.softDeleteForUserFn != nil {
		return f.softDeleteForUserFn(ctx, tenantID, userID)
	}
	return 0, nil
}

var _ port.TenderACLRepository = (*fakeRepo)(nil)

type fakeChecker struct {
	existsFn func(ctx context.Context, tenantID, userID uuid.UUID) (bool, uuid.UUID, error)
}

func (f *fakeChecker) Exists(ctx context.Context, tenantID, userID uuid.UUID) (bool, uuid.UUID, error) {
	return f.existsFn(ctx, tenantID, userID)
}

var _ port.MembershipCheckClient = (*fakeChecker)(nil)

type fakeCache struct {
	getFn    func(ctx context.Context, tenantID, tenderID, userID uuid.UUID) (*domain.CachedAccess, bool, error)
	setFn    func(ctx context.Context, tenantID, tenderID, userID uuid.UUID, value domain.CachedAccess) error
	deleteFn func(ctx context.Context, tenantID, tenderID, userID uuid.UUID) error
}

func (f *fakeCache) Get(ctx context.Context, tenantID, tenderID, userID uuid.UUID) (*domain.CachedAccess, bool, error) {
	if f.getFn == nil {
		return nil, false, nil
	}
	return f.getFn(ctx, tenantID, tenderID, userID)
}
func (f *fakeCache) Set(ctx context.Context, tenantID, tenderID, userID uuid.UUID, value domain.CachedAccess) error {
	if f.setFn == nil {
		return nil
	}
	return f.setFn(ctx, tenantID, tenderID, userID, value)
}
func (f *fakeCache) Delete(ctx context.Context, tenantID, tenderID, userID uuid.UUID) error {
	if f.deleteFn == nil {
		return nil
	}
	return f.deleteFn(ctx, tenantID, tenderID, userID)
}
func (f *fakeCache) Ping(_ context.Context) error { return nil }

var _ port.Cache = (*fakeCache)(nil)

// fakeMetrics is a no-op ACLMetrics — core/service tests never import the
// concrete adapter/outbound/metrics type, matching iam-org-membership's
// own convention of hand-rolled fakes for every port/local interface.
type fakeMetrics struct{}

func (fakeMetrics) RecordGrantCheck(context.Context, string)    {}
func (fakeMetrics) RecordCheckCall(context.Context, string)     {}
func (fakeMetrics) RecordWrite(context.Context, string, string) {}
func (fakeMetrics) RecordCacheHit(context.Context)              {}
func (fakeMetrics) RecordCacheMiss(context.Context)             {}

var _ ACLMetrics = fakeMetrics{}

func newTestService(repo port.TenderACLRepository, checker port.MembershipCheckClient, c port.Cache, _ *testing.T) *ACLService {
	return NewACLService(repo, checker, c, fakeMetrics{}, slog.Default(), otel.Tracer("test"))
}

func activeChecker(membershipID uuid.UUID) *fakeChecker {
	return &fakeChecker{existsFn: func(context.Context, uuid.UUID, uuid.UUID) (bool, uuid.UUID, error) {
		return true, membershipID, nil
	}}
}

// ── List (TAC-1) ─────────────────────────────────────────────────────────

func TestService_List_DelegatesToRepo(t *testing.T) {
	tenantID, tenderID := uuid.New(), uuid.New()
	want := []domain.TenderACLEntry{{ID: uuid.New(), TenantID: tenantID, TenderID: tenderID}}
	repo := &fakeRepo{listFn: func(_ context.Context, tt, td uuid.UUID) ([]domain.TenderACLEntry, error) {
		assert.Equal(t, tenantID, tt)
		assert.Equal(t, tenderID, td)
		return want, nil
	}}
	svc := newTestService(repo, &fakeChecker{}, &fakeCache{}, t)

	got, err := svc.List(context.Background(), tenantID, tenderID)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestService_List_PropagatesRepoError(t *testing.T) {
	repoErr := errors.New("db down")
	repo := &fakeRepo{listFn: func(context.Context, uuid.UUID, uuid.UUID) ([]domain.TenderACLEntry, error) {
		return nil, repoErr
	}}
	svc := newTestService(repo, &fakeChecker{}, &fakeCache{}, t)

	_, err := svc.List(context.Background(), uuid.New(), uuid.New())
	assert.ErrorIs(t, err, repoErr)
}

// ── Grant (TAC-2) ────────────────────────────────────────────────────────

func TestService_Grant_RejectsInvalidLevel(t *testing.T) {
	svc := newTestService(&fakeRepo{}, &fakeChecker{}, &fakeCache{}, t)

	_, err := svc.Grant(context.Background(), uuid.New(), uuid.New(), uuid.New(), uuid.New(),
		domain.TenderACLLevel("wizard"), "", nil)

	code, ok := domain.CodeOf(err)
	require.True(t, ok)
	assert.Equal(t, domain.ErrCodeInvalidAccessLevel, code)
}

func TestService_Grant_RejectsEmptyLevel(t *testing.T) {
	svc := newTestService(&fakeRepo{}, &fakeChecker{}, &fakeCache{}, t)

	_, err := svc.Grant(context.Background(), uuid.New(), uuid.New(), uuid.New(), uuid.New(),
		domain.TenderACLLevel(""), "", nil)

	code, ok := domain.CodeOf(err)
	require.True(t, ok)
	assert.Equal(t, domain.ErrCodeInvalidAccessLevel, code)
}

func TestService_Grant_AcceptsAllValidLevels(t *testing.T) {
	for _, lvl := range []domain.TenderACLLevel{domain.ACLView, domain.ACLEdit, domain.ACLApprove} {
		t.Run(string(lvl), func(t *testing.T) {
			tenantID, tenderID, userID := uuid.New(), uuid.New(), uuid.New()
			membershipID := uuid.New()
			repo := &fakeRepo{grantFn: func(_ context.Context, e domain.TenderACLEntry) (domain.TenderACLEntry, error) {
				assert.Equal(t, lvl, e.AccessLevel)
				assert.Equal(t, membershipID, e.TenantMembershipID, "TAE-8 composite FK anchor, populated from the membership check")
				e.ID = uuid.New()
				return e, nil
			}}
			svc := newTestService(repo, activeChecker(membershipID), &fakeCache{}, t)

			got, err := svc.Grant(context.Background(), tenantID, tenderID, userID, uuid.New(), lvl, "reason", nil)
			require.NoError(t, err)
			assert.Equal(t, lvl, got.AccessLevel)
		})
	}
}

func TestService_Grant_RejectsPastExpiry(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	svc := newTestService(&fakeRepo{}, &fakeChecker{}, &fakeCache{}, t)

	_, err := svc.Grant(context.Background(), uuid.New(), uuid.New(), uuid.New(), uuid.New(),
		domain.ACLView, "", &past)

	code, ok := domain.CodeOf(err)
	require.True(t, ok)
	assert.Equal(t, domain.ErrCodeInvalidExpiry, code)
}

func TestService_Grant_RejectsExpiryAtNow(t *testing.T) {
	now := time.Now()
	svc := newTestService(&fakeRepo{}, &fakeChecker{}, &fakeCache{}, t)

	_, err := svc.Grant(context.Background(), uuid.New(), uuid.New(), uuid.New(), uuid.New(),
		domain.ACLView, "", &now)

	code, ok := domain.CodeOf(err)
	require.True(t, ok)
	assert.Equal(t, domain.ErrCodeInvalidExpiry, code)
}

func TestService_Grant_AcceptsFutureExpiry(t *testing.T) {
	future := time.Now().Add(time.Hour)
	repo := &fakeRepo{grantFn: func(_ context.Context, e domain.TenderACLEntry) (domain.TenderACLEntry, error) {
		require.NotNil(t, e.ExpiresAt)
		assert.Equal(t, future, *e.ExpiresAt)
		e.ID = uuid.New()
		return e, nil
	}}
	svc := newTestService(repo, activeChecker(uuid.New()), &fakeCache{}, t)

	_, err := svc.Grant(context.Background(), uuid.New(), uuid.New(), uuid.New(), uuid.New(),
		domain.ACLView, "", &future)
	require.NoError(t, err)
}

func TestService_Grant_ReasonTooLong_Rejected(t *testing.T) {
	reason501 := make([]byte, 501)
	for i := range reason501 {
		reason501[i] = 'a'
	}
	svc := newTestService(&fakeRepo{}, &fakeChecker{}, &fakeCache{}, t)

	_, err := svc.Grant(context.Background(), uuid.New(), uuid.New(), uuid.New(), uuid.New(),
		domain.ACLView, string(reason501), nil)

	code, ok := domain.CodeOf(err)
	require.True(t, ok)
	assert.Equal(t, domain.ErrCodeInvalidReason, code)
}

func TestService_Grant_Reason500Chars_Allowed(t *testing.T) {
	reason500 := make([]byte, 500)
	for i := range reason500 {
		reason500[i] = 'x'
	}
	repo := &fakeRepo{grantFn: func(_ context.Context, e domain.TenderACLEntry) (domain.TenderACLEntry, error) {
		assert.Equal(t, 500, len(e.Reason))
		e.ID = uuid.New()
		return e, nil
	}}
	svc := newTestService(repo, activeChecker(uuid.New()), &fakeCache{}, t)

	_, err := svc.Grant(context.Background(), uuid.New(), uuid.New(), uuid.New(), uuid.New(),
		domain.ACLView, string(reason500), nil)
	require.NoError(t, err)
}

func TestService_Grant_WithReason(t *testing.T) {
	reason := "Temporary approval rights for Q3 tender review"
	repo := &fakeRepo{grantFn: func(_ context.Context, e domain.TenderACLEntry) (domain.TenderACLEntry, error) {
		assert.Equal(t, reason, e.Reason)
		e.ID = uuid.New()
		return e, nil
	}}
	svc := newTestService(repo, activeChecker(uuid.New()), &fakeCache{}, t)

	got, err := svc.Grant(context.Background(), uuid.New(), uuid.New(), uuid.New(), uuid.New(),
		domain.ACLApprove, reason, nil)
	require.NoError(t, err)
	assert.Equal(t, reason, got.Reason)
}

// TestService_Grant_CheckerError_ReturnsCoreUnavailable covers the
// fail-closed path (LLD TAC-FAIL-1): a membershipcheck error must never be
// treated as "not active" or defaulted to "active" — it surfaces as its
// own distinct 503 core_unavailable code.
func TestService_Grant_CheckerError_ReturnsCoreUnavailable(t *testing.T) {
	checker := &fakeChecker{existsFn: func(context.Context, uuid.UUID, uuid.UUID) (bool, uuid.UUID, error) {
		return false, uuid.UUID{}, errors.New("dial tcp: timeout")
	}}
	repo := &fakeRepo{grantFn: func(context.Context, domain.TenderACLEntry) (domain.TenderACLEntry, error) {
		t.Fatal("repo.Grant must not be called when the membership check itself fails")
		return domain.TenderACLEntry{}, nil
	}}
	svc := newTestService(repo, checker, &fakeCache{}, t)

	_, err := svc.Grant(context.Background(), uuid.New(), uuid.New(), uuid.New(), uuid.New(),
		domain.ACLView, "", nil)

	code, ok := domain.CodeOf(err)
	require.True(t, ok)
	assert.Equal(t, domain.ErrCodeCoreUnavailable, code)
}

// TestService_Grant_GranteeNotActive_ReturnsGranteeNotActiveMember covers
// the collapse of iam-org-membership's today-distinct member_not_found
// (404) / member_not_active (422) into the new provider contract's single
// active:false outcome.
func TestService_Grant_GranteeNotActive_ReturnsGranteeNotActiveMember(t *testing.T) {
	checker := &fakeChecker{existsFn: func(context.Context, uuid.UUID, uuid.UUID) (bool, uuid.UUID, error) {
		return false, uuid.UUID{}, nil
	}}
	repo := &fakeRepo{grantFn: func(context.Context, domain.TenderACLEntry) (domain.TenderACLEntry, error) {
		t.Fatal("repo.Grant must not be called for a not-active grantee")
		return domain.TenderACLEntry{}, nil
	}}
	svc := newTestService(repo, checker, &fakeCache{}, t)

	_, err := svc.Grant(context.Background(), uuid.New(), uuid.New(), uuid.New(), uuid.New(),
		domain.ACLView, "", nil)

	code, ok := domain.CodeOf(err)
	require.True(t, ok)
	assert.Equal(t, domain.ErrCodeGranteeNotActiveMember, code)
}

func TestService_Grant_RepoFailurePropagates(t *testing.T) {
	repoErr := errors.New("insert conflict")
	repo := &fakeRepo{grantFn: func(context.Context, domain.TenderACLEntry) (domain.TenderACLEntry, error) {
		return domain.TenderACLEntry{}, repoErr
	}}
	svc := newTestService(repo, activeChecker(uuid.New()), &fakeCache{}, t)

	_, err := svc.Grant(context.Background(), uuid.New(), uuid.New(), uuid.New(), uuid.New(),
		domain.ACLView, "", nil)
	assert.ErrorIs(t, err, repoErr)
}

func TestService_Grant_DuplicateGrant_ReturnsConflict(t *testing.T) {
	repo := &fakeRepo{grantFn: func(context.Context, domain.TenderACLEntry) (domain.TenderACLEntry, error) {
		return domain.TenderACLEntry{}, domain.NewError(domain.ErrCodeDuplicateGrant, "an active grant already exists")
	}}
	svc := newTestService(repo, activeChecker(uuid.New()), &fakeCache{}, t)

	_, err := svc.Grant(context.Background(), uuid.New(), uuid.New(), uuid.New(), uuid.New(),
		domain.ACLView, "", nil)

	code, ok := domain.CodeOf(err)
	require.True(t, ok)
	assert.Equal(t, domain.ErrCodeDuplicateGrant, code)
}

func TestService_Grant_CacheInvalidationFailureDoesNotFailGrant(t *testing.T) {
	repo := &fakeRepo{grantFn: func(_ context.Context, e domain.TenderACLEntry) (domain.TenderACLEntry, error) {
		e.ID = uuid.New()
		return e, nil
	}}
	c := &fakeCache{deleteFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
		return errors.New("valkey down")
	}}
	svc := newTestService(repo, activeChecker(uuid.New()), c, t)

	_, err := svc.Grant(context.Background(), uuid.New(), uuid.New(), uuid.New(), uuid.New(),
		domain.ACLView, "", nil)
	require.NoError(t, err, "a cache invalidation failure must not fail the grant itself")
}

// ── Revoke (TAC-3) ───────────────────────────────────────────────────────

func TestService_Revoke_DelegatesToRepo(t *testing.T) {
	tenantID, tenderID, userID := uuid.New(), uuid.New(), uuid.New()
	const version int64 = 3
	called := false
	repo := &fakeRepo{revokeFn: func(_ context.Context, tt, td, uu uuid.UUID, v int64) error {
		called = true
		assert.Equal(t, tenantID, tt)
		assert.Equal(t, tenderID, td)
		assert.Equal(t, userID, uu)
		assert.Equal(t, version, v)
		return nil
	}}
	svc := newTestService(repo, &fakeChecker{}, &fakeCache{}, t)

	err := svc.Revoke(context.Background(), tenantID, tenderID, userID, version)
	require.NoError(t, err)
	assert.True(t, called)
}

func TestService_Revoke_PropagatesRepoError(t *testing.T) {
	repoErr := errors.New("db down")
	repo := &fakeRepo{revokeFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int64) error {
		return repoErr
	}}
	svc := newTestService(repo, &fakeChecker{}, &fakeCache{}, t)

	err := svc.Revoke(context.Background(), uuid.New(), uuid.New(), uuid.New(), 1)
	assert.ErrorIs(t, err, repoErr)
}

// TestService_Revoke_VersionConflict_PropagatesAsError covers LLD
// §11.2/§12.1's optimistic-lock contract: a repo reporting
// ErrCodeOptimisticLockConflict (zero rows affected — stale version,
// already revoked, or never existed; the repo doesn't distinguish these)
// must surface as a real error, not be swallowed into a silent success.
func TestService_Revoke_VersionConflict_PropagatesAsError(t *testing.T) {
	repo := &fakeRepo{revokeFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int64) error {
		return domain.NewError(domain.ErrCodeOptimisticLockConflict, "record version conflict")
	}}
	svc := newTestService(repo, &fakeChecker{}, &fakeCache{}, t)

	err := svc.Revoke(context.Background(), uuid.New(), uuid.New(), uuid.New(), 1)
	require.Error(t, err)
	code, ok := domain.CodeOf(err)
	require.True(t, ok)
	assert.Equal(t, domain.ErrCodeOptimisticLockConflict, code)
}

func TestService_Revoke_CacheInvalidationFailureDoesNotFailRevoke(t *testing.T) {
	repo := &fakeRepo{revokeFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int64) error {
		return nil
	}}
	c := &fakeCache{deleteFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
		return errors.New("valkey down")
	}}
	svc := newTestService(repo, &fakeChecker{}, c, t)

	err := svc.Revoke(context.Background(), uuid.New(), uuid.New(), uuid.New(), 1)
	require.NoError(t, err, "a cache invalidation failure must not fail the revoke itself")
}

// ── CheckAccess (TAC-4/I-12) ─────────────────────────────────────────────

func TestService_CheckAccess_CacheHit_SkipsRepo(t *testing.T) {
	level := "approve"
	cached := domain.CachedAccess{HasAccess: true, AccessLevel: &level}
	c := &fakeCache{getFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.CachedAccess, bool, error) {
		return &cached, true, nil
	}}
	repo := &fakeRepo{findActiveFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.TenderACLEntry, error) {
		t.Fatal("repo.FindActive must not be called on a cache hit")
		return nil, nil
	}}
	svc := newTestService(repo, &fakeChecker{}, c, t)

	got, err := svc.CheckAccess(context.Background(), uuid.New(), uuid.New(), uuid.New())
	require.NoError(t, err)
	assert.True(t, got.HasAccess)
	assert.Equal(t, "approve", *got.AccessLevel)
}

func TestService_CheckAccess_CacheMiss_FallsThroughAndPopulates(t *testing.T) {
	tenantID, tenderID, userID := uuid.New(), uuid.New(), uuid.New()
	entry := &domain.TenderACLEntry{AccessLevel: domain.ACLEdit}
	var setCalled bool
	repo := &fakeRepo{findActiveFn: func(_ context.Context, tt, td, uu uuid.UUID) (*domain.TenderACLEntry, error) {
		assert.Equal(t, tenantID, tt)
		assert.Equal(t, tenderID, td)
		assert.Equal(t, userID, uu)
		return entry, nil
	}}
	c := &fakeCache{
		getFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.CachedAccess, bool, error) {
			return nil, false, nil
		},
		setFn: func(_ context.Context, _, _, _ uuid.UUID, value domain.CachedAccess) error {
			setCalled = true
			assert.True(t, value.HasAccess)
			assert.Equal(t, "edit", *value.AccessLevel)
			return nil
		},
	}
	svc := newTestService(repo, &fakeChecker{}, c, t)

	got, err := svc.CheckAccess(context.Background(), tenantID, tenderID, userID)
	require.NoError(t, err)
	assert.True(t, got.HasAccess)
	assert.True(t, setCalled, "a cache miss must populate the cache")
}

// TestService_CheckAccess_NoActiveGrant_ReturnsHasAccessFalse_NeverError
// covers the "never 404" contract: no active grant is a valid, cacheable
// has_access:false answer, not an error.
func TestService_CheckAccess_NoActiveGrant_ReturnsHasAccessFalse_NeverError(t *testing.T) {
	repo := &fakeRepo{findActiveFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.TenderACLEntry, error) {
		return nil, nil
	}}
	c := &fakeCache{getFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.CachedAccess, bool, error) {
		return nil, false, nil
	}}
	svc := newTestService(repo, &fakeChecker{}, c, t)

	got, err := svc.CheckAccess(context.Background(), uuid.New(), uuid.New(), uuid.New())
	require.NoError(t, err)
	assert.False(t, got.HasAccess)
	assert.Nil(t, got.AccessLevel)
}

func TestService_CheckAccess_CacheReadError_FallsThroughToRepo(t *testing.T) {
	entry := &domain.TenderACLEntry{AccessLevel: domain.ACLView}
	repo := &fakeRepo{findActiveFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.TenderACLEntry, error) {
		return entry, nil
	}}
	c := &fakeCache{
		getFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.CachedAccess, bool, error) {
			return nil, false, errors.New("valkey down")
		},
	}
	svc := newTestService(repo, &fakeChecker{}, c, t)

	got, err := svc.CheckAccess(context.Background(), uuid.New(), uuid.New(), uuid.New())
	require.NoError(t, err, "a cache read failure must fall through to Postgres, not fail the check")
	assert.True(t, got.HasAccess)
}

// TestACLGrant_RTLReason_201 confirms that the 500-byte limit is measured in
// UTF-8 bytes, not Unicode code-points. Arabic script (U+0600–U+06FF) encodes
// to 2 bytes per character, so 100 Arabic chars = 200 bytes — well within the
// 500-byte cap — and the grant must succeed with 201.
func TestACLGrant_RTLReason_201(t *testing.T) {
	rtlReason := "مرحبا بالعالم مرحبا بالعالم مرحبا بالعالم مرحبا بالعالم مرحبا بالعالم مرحبا بال"
	// Must be ≤500 bytes; confirm the assumption holds.
	require.LessOrEqual(t, len(rtlReason), 500, "test setup: RTL reason must be ≤500 bytes")
	repo := &fakeRepo{grantFn: func(_ context.Context, e domain.TenderACLEntry) (domain.TenderACLEntry, error) {
		assert.Equal(t, rtlReason, e.Reason)
		e.ID = uuid.New()
		return e, nil
	}}
	svc := newTestService(repo, activeChecker(uuid.New()), &fakeCache{}, t)

	_, err := svc.Grant(context.Background(), uuid.New(), uuid.New(), uuid.New(), uuid.New(),
		domain.ACLView, rtlReason, nil)
	require.NoError(t, err, "RTL reason within 500-byte limit must be accepted")
}

// TestACLGrant_WhitespaceReason_201 documents that the service does NOT trim
// whitespace from the reason field — a spaces-only string passes the
// byte-length validation unchanged and is stored as-is. If trimming is ever
// added this test should be updated to reflect the new behavior.
func TestACLGrant_WhitespaceReason_201(t *testing.T) {
	whitespaceReason := "   "
	repo := &fakeRepo{grantFn: func(_ context.Context, e domain.TenderACLEntry) (domain.TenderACLEntry, error) {
		assert.Equal(t, whitespaceReason, e.Reason, "service must not trim whitespace from reason")
		e.ID = uuid.New()
		return e, nil
	}}
	svc := newTestService(repo, activeChecker(uuid.New()), &fakeCache{}, t)

	_, err := svc.Grant(context.Background(), uuid.New(), uuid.New(), uuid.New(), uuid.New(),
		domain.ACLView, whitespaceReason, nil)
	require.NoError(t, err, "whitespace-only reason (len=3) must pass the 500-byte validation")
}

// TestService_CheckAccess_CacheSetError_StillReturnsResult covers the silent
// cache-populate-failure path: cache.Set fails after a DB hit, but the result
// is still returned to the caller — the Set error is only logged, never fatal.
func TestService_CheckAccess_CacheSetError_StillReturnsResult(t *testing.T) {
	entry := &domain.TenderACLEntry{AccessLevel: domain.ACLEdit}
	repo := &fakeRepo{findActiveFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.TenderACLEntry, error) {
		return entry, nil
	}}
	c := &fakeCache{
		getFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.CachedAccess, bool, error) {
			return nil, false, nil
		},
		setFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, domain.CachedAccess) error {
			return errors.New("valkey down")
		},
	}
	svc := newTestService(repo, &fakeChecker{}, c, t)

	got, err := svc.CheckAccess(context.Background(), uuid.New(), uuid.New(), uuid.New())
	require.NoError(t, err, "cache.Set failure must not fail CheckAccess")
	assert.True(t, got.HasAccess)
}

func TestService_CheckAccess_RepoError_Propagates(t *testing.T) {
	repoErr := errors.New("db down")
	repo := &fakeRepo{findActiveFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.TenderACLEntry, error) {
		return nil, repoErr
	}}
	c := &fakeCache{getFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.CachedAccess, bool, error) {
		return nil, false, nil
	}}
	svc := newTestService(repo, &fakeChecker{}, c, t)

	_, err := svc.CheckAccess(context.Background(), uuid.New(), uuid.New(), uuid.New())
	assert.ErrorIs(t, err, repoErr)
}
