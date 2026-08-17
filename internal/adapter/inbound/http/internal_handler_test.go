package http

import (
	"context"
	"encoding/json"
	"net/http"
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
