package http

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
)

// TAC-4/I-12: mesh-only, no RBAC/JWT check at all — these tests
// deliberately call CheckAccess with NO RequestContext to prove the
// mTLS-only trust boundary (LLD §8.2/§13.2).

func TestInternalHandler_CheckAccess_HasAccessTrue(t *testing.T) {
	entry := &domain.TenderACLEntry{AccessLevel: domain.ACLApprove}
	repo := &fakeRepo{findActiveFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.TenderACLEntry, error) {
		return entry, nil
	}}
	h := newTestHandler(repo, &fakeChecker{}, &fakeCache{}, t)

	c, w := buildCtx(http.MethodGet, "/", ``, nil)
	setParams(c, "id", uuid.New().String(), "tender_id", uuid.New().String(), "user_id", uuid.New().String())
	h.CheckAccess(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp CheckAccessResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.HasAccess)
	require.NotNil(t, resp.AccessLevel)
	assert.Equal(t, "approve", *resp.AccessLevel)
}

// TestInternalHandler_CheckAccess_NoActiveGrant_ReturnsHasAccessFalse_Not404
// is the "never 404" contract check: no active grant is 200 has_access:false.
func TestInternalHandler_CheckAccess_NoActiveGrant_ReturnsHasAccessFalse_Not404(t *testing.T) {
	repo := &fakeRepo{findActiveFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.TenderACLEntry, error) {
		return nil, nil
	}}
	h := newTestHandler(repo, &fakeChecker{}, &fakeCache{}, t)

	c, w := buildCtx(http.MethodGet, "/", ``, nil)
	setParams(c, "id", uuid.New().String(), "tender_id", uuid.New().String(), "user_id", uuid.New().String())
	h.CheckAccess(c)

	assert.Equal(t, http.StatusOK, w.Code, "no active grant must be 200 has_access:false, never 404")
	var resp CheckAccessResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.False(t, resp.HasAccess)
	assert.Nil(t, resp.AccessLevel)
}

func TestInternalHandler_CheckAccess_InvalidUserID_Returns400(t *testing.T) {
	h := newTestHandler(&fakeRepo{}, &fakeChecker{}, &fakeCache{}, t)
	c, w := buildCtx(http.MethodGet, "/", ``, nil)
	setParams(c, "id", uuid.New().String(), "tender_id", uuid.New().String(), "user_id", "bad-uuid")
	h.CheckAccess(c)
	assertErrorCode(t, w, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
}

// TestInternalHandler_CheckAccess_NoRoleCheckRequired proves TAC-4 never
// consults roles/tenant-match at all — a request with a RequestContext for
// a totally different tenant and no roles still succeeds, unlike TAC-1/2/3.
func TestInternalHandler_CheckAccess_NoRoleCheckRequired(t *testing.T) {
	repo := &fakeRepo{findActiveFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.TenderACLEntry, error) {
		return nil, nil
	}}
	h := newTestHandler(repo, &fakeChecker{}, &fakeCache{}, t)

	c, w := buildCtx(http.MethodGet, "/", ``, plainMemberCtx(uuid.New()))
	setParams(c, "id", uuid.New().String(), "tender_id", uuid.New().String(), "user_id", uuid.New().String())
	h.CheckAccess(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestInternalHandler_CheckAccess_InvalidTenantID_Returns400(t *testing.T) {
	h := newTestHandler(&fakeRepo{}, &fakeChecker{}, &fakeCache{}, t)
	c, w := buildCtx(http.MethodGet, "/", ``, nil)
	setParams(c, "id", "not-a-uuid", "tender_id", uuid.New().String(), "user_id", uuid.New().String())
	h.CheckAccess(c)
	assertErrorCode(t, w, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
}

func TestInternalHandler_CheckAccess_InvalidTenderID_Returns400(t *testing.T) {
	h := newTestHandler(&fakeRepo{}, &fakeChecker{}, &fakeCache{}, t)
	c, w := buildCtx(http.MethodGet, "/", ``, nil)
	setParams(c, "id", uuid.New().String(), "tender_id", "not-a-uuid", "user_id", uuid.New().String())
	h.CheckAccess(c)
	assertErrorCode(t, w, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
}

// ── CONC-CHECK-CACHE-01: thundering herd on cache miss ───────────────────────

// TestACLCheckAccess_ThunderingHerd covers CONC-CHECK-CACHE-01: many
// goroutines call CheckAccess simultaneously on a cold cache (cache.Get
// always misses). All goroutines must receive the correct 200 has_access:true
// answer. cache.Set races are benign (last write wins, identical value).
func TestACLCheckAccess_ThunderingHerd(t *testing.T) {
	const workers = 20
	entry := &domain.TenderACLEntry{AccessLevel: domain.ACLView}
	var findCalls atomic.Int32
	repo := &fakeRepo{findActiveFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.TenderACLEntry, error) {
		findCalls.Add(1)
		return entry, nil
	}}
	cache := &fakeCache{getFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.CachedAccess, bool, error) {
		return nil, false, nil // always miss
	}}
	h := newTestHandler(repo, &fakeChecker{}, cache, t)
	tenant, tender, user := uuid.New(), uuid.New(), uuid.New()

	var successes atomic.Int32
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, w := buildCtx(http.MethodGet, "/", ``, nil)
			setParams(c, "id", tenant.String(), "tender_id", tender.String(), "user_id", user.String())
			h.CheckAccess(c)
			if w.Code == http.StatusOK {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(workers), successes.Load(), "all goroutines must receive 200 has_access:true")
	assert.Equal(t, int32(workers), findCalls.Load(), "cache miss → all workers fall through to repo")
}

func TestInternalHandler_CheckAccess_UnwrappedRepoError_Returns500(t *testing.T) {
	repo := &fakeRepo{findActiveFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.TenderACLEntry, error) {
		return nil, assert.AnError
	}}
	h := newTestHandler(repo, &fakeChecker{}, &fakeCache{}, t)
	c, w := buildCtx(http.MethodGet, "/", ``, nil)
	setParams(c, "id", uuid.New().String(), "tender_id", uuid.New().String(), "user_id", uuid.New().String())
	h.CheckAccess(c)
	// An unrecognized (non-*domain.Error) failure maps to
	// internal_server_error via respondACLError's default branch — this
	// repo error isn't wrapped in a domain.Error, so it correctly
	// surfaces as 500 here, distinct from the dependency_unavailable code
	// a real Postgres outage would map through the repository layer.
	assertErrorCode(t, w, http.StatusInternalServerError, domain.ErrCodeInternal)
}
