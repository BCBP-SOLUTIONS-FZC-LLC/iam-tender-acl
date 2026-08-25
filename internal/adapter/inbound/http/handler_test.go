package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/port"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/service"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// buildCtx constructs a *gin.Context carrying rc (if non-nil) in its
// request context, mirroring iam-org-membership's own handler-test helper
// so these tests read the same way.
func buildCtx(method, path, body string, rc *RequestContext) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if rc != nil {
		req = req.WithContext(WithContext(req.Context(), rc))
	}
	c.Request = req
	return c, w
}

func setParams(c *gin.Context, kv ...string) {
	for i := 0; i+1 < len(kv); i += 2 {
		c.Params = append(c.Params, gin.Param{Key: kv[i], Value: kv[i+1]})
	}
}

func assertErrorCode(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	assert.Equal(t, status, w.Code)
	var body ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, code, body.Error)
}

func tenderAdminCtx(tenantID uuid.UUID) *RequestContext {
	return &RequestContext{TenantID: tenantID.String(), UserID: uuid.New().String(), Roles: []string{"tender_admin"}}
}
func tenantAdminCtx(tenantID uuid.UUID) *RequestContext {
	return &RequestContext{TenantID: tenantID.String(), UserID: uuid.New().String(), Roles: []string{"tenant_admin"}}
}
func tenantOwnerCtx(tenantID uuid.UUID) *RequestContext {
	return &RequestContext{TenantID: tenantID.String(), UserID: uuid.New().String(), Roles: []string{"tenant_owner"}}
}
func plainMemberCtx(tenantID uuid.UUID) *RequestContext {
	return &RequestContext{TenantID: tenantID.String(), UserID: uuid.New().String(), Roles: []string{"member"}}
}

// ── Fakes (mirrors internal/core/service's, but this package can't import
// unexported test-only fakes across packages, so a minimal local set is
// declared here — the same pattern iam-org-membership's own http-adapter
// test files use). ────────────────────────────────────────────────────────

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

func activeChecker(membershipID uuid.UUID) *fakeChecker {
	return &fakeChecker{existsFn: func(context.Context, uuid.UUID, uuid.UUID) (bool, uuid.UUID, error) {
		return true, membershipID, nil
	}}
}

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

type fakeMetrics struct{}

func (fakeMetrics) RecordGrantCheck(context.Context, string)    {}
func (fakeMetrics) RecordCheckCall(context.Context, string)     {}
func (fakeMetrics) RecordWrite(context.Context, string, string) {}
func (fakeMetrics) RecordCacheHit(context.Context)              {}
func (fakeMetrics) RecordCacheMiss(context.Context)             {}

func newTestHandler(repo port.TenderACLRepository, checker port.MembershipCheckClient, c port.Cache, t *testing.T) *Handler {
	t.Helper()
	svc := service.NewACLService(repo, checker, c, fakeMetrics{}, port.SlogStyleLogger{}, otel.Tracer("test"))
	return NewHandler(svc)
}

func emptyRepo() *fakeRepo {
	return &fakeRepo{
		listFn: func(context.Context, uuid.UUID, uuid.UUID) ([]domain.TenderACLEntry, error) {
			return []domain.TenderACLEntry{}, nil
		},
		grantFn: func(_ context.Context, e domain.TenderACLEntry) (domain.TenderACLEntry, error) {
			e.ID = uuid.New()
			return e, nil
		},
		revokeFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int64) error {
			return nil
		},
	}
}

// ── List (TAC-1) ─────────────────────────────────────────────────────────

func TestHandler_List_PlainMember_Returns403(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodGet, "/", ``, plainMemberCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.List(c)
	assertErrorCode(t, w, http.StatusForbidden, domain.ErrCodeInsufficientRole)
}

func TestHandler_List_TenderAdmin_Returns200(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodGet, "/", ``, tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.List(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandler_List_CrossTenant_Returns403(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	c, w := buildCtx(http.MethodGet, "/", ``, tenderAdminCtx(uuid.New()))
	pathTenant := uuid.New()
	setParams(c, "id", pathTenant.String(), "tender_id", uuid.New().String())
	h.List(c)
	assertErrorCode(t, w, http.StatusForbidden, domain.ErrCodeInsufficientRole)
}

func TestHandler_List_InvalidTenderID_Returns400(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodGet, "/", ``, tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", "not-a-uuid")
	h.List(c)
	assertErrorCode(t, w, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
}

func TestHandler_List_InvalidTenantID_Returns400(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	c, w := buildCtx(http.MethodGet, "/", ``, tenderAdminCtx(uuid.New()))
	setParams(c, "id", "not-a-uuid", "tender_id", uuid.New().String())
	h.List(c)
	assertErrorCode(t, w, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
}

// ── Grant (TAC-2) ────────────────────────────────────────────────────────

func grantBody(userID uuid.UUID) string {
	return `{"user_id":"` + userID.String() + `","access_level":"view"}`
}

func TestHandler_Grant_InvalidTenantID_Returns400(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodPost, "/", grantBody(uuid.New()), tenantOwnerCtx(tenant))
	setParams(c, "id", "bad-uuid", "tender_id", uuid.New().String())
	h.Grant(c)
	assertErrorCode(t, w, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
}

func TestHandler_Grant_InvalidTenderID_Returns400(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodPost, "/", grantBody(uuid.New()), tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", "not-a-uuid")
	h.Grant(c)
	assertErrorCode(t, w, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
}

func TestHandler_Grant_PlainMember_Returns403(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodPost, "/", grantBody(uuid.New()), plainMemberCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.Grant(c)
	assertErrorCode(t, w, http.StatusForbidden, domain.ErrCodeInsufficientRole)
}

func TestHandler_Grant_TenderAdmin_Returns201(t *testing.T) {
	h := newTestHandler(emptyRepo(), activeChecker(uuid.New()), &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodPost, "/", grantBody(uuid.New()), tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.Grant(c)
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestHandler_Grant_TenantAdmin_Returns201(t *testing.T) {
	h := newTestHandler(emptyRepo(), activeChecker(uuid.New()), &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodPost, "/", grantBody(uuid.New()), tenantAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.Grant(c)
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestHandler_Grant_TenantOwner_Returns201(t *testing.T) {
	h := newTestHandler(emptyRepo(), activeChecker(uuid.New()), &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodPost, "/", grantBody(uuid.New()), tenantOwnerCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.Grant(c)
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestHandler_Grant_InvalidJSONBody_Returns400(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodPost, "/", `{not json`, tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.Grant(c)
	assertErrorCode(t, w, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
}

func TestHandler_Grant_InvalidUserIDInBody_Returns400(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	body := `{"user_id":"not-a-uuid","access_level":"view"}`
	c, w := buildCtx(http.MethodPost, "/", body, tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.Grant(c)
	assertErrorCode(t, w, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
}

// TestHandler_Grant_InvalidUserIDInContext_Returns401 exercises the
// grantedBy-parse branch: role/tenant checks pass, but the gateway-injected
// x-user-id header itself isn't a valid UUID.
func TestHandler_Grant_InvalidUserIDInContext_Returns401(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	rc := &RequestContext{TenantID: tenant.String(), UserID: "not-a-uuid", Roles: []string{"tender_admin"}}
	c, w := buildCtx(http.MethodPost, "/", grantBody(uuid.New()), rc)
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.Grant(c)
	assertErrorCode(t, w, http.StatusUnauthorized, domain.ErrCodeUnauthorized)
}

func TestHandler_Grant_InvalidAccessLevel_Returns422(t *testing.T) {
	h := newTestHandler(emptyRepo(), activeChecker(uuid.New()), &fakeCache{}, t)
	tenant := uuid.New()
	body := `{"user_id":"` + uuid.New().String() + `","access_level":"wizard"}`
	c, w := buildCtx(http.MethodPost, "/", body, tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.Grant(c)
	assertErrorCode(t, w, http.StatusUnprocessableEntity, domain.ErrCodeInvalidAccessLevel)
}

func TestHandler_Grant_GranteeNotActive_Returns422(t *testing.T) {
	checker := &fakeChecker{existsFn: func(context.Context, uuid.UUID, uuid.UUID) (bool, uuid.UUID, error) {
		return false, uuid.UUID{}, nil
	}}
	h := newTestHandler(emptyRepo(), checker, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodPost, "/", grantBody(uuid.New()), tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.Grant(c)
	assertErrorCode(t, w, http.StatusUnprocessableEntity, domain.ErrCodeGranteeNotActiveMember)
}

func TestHandler_Grant_CoreUnavailable_Returns503(t *testing.T) {
	checker := &fakeChecker{existsFn: func(context.Context, uuid.UUID, uuid.UUID) (bool, uuid.UUID, error) {
		return false, uuid.UUID{}, assert.AnError
	}}
	h := newTestHandler(emptyRepo(), checker, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodPost, "/", grantBody(uuid.New()), tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.Grant(c)
	assertErrorCode(t, w, http.StatusServiceUnavailable, domain.ErrCodeCoreUnavailable)
}

func TestHandler_Grant_DuplicateGrant_Returns409(t *testing.T) {
	repo := emptyRepo()
	repo.grantFn = func(context.Context, domain.TenderACLEntry) (domain.TenderACLEntry, error) {
		return domain.TenderACLEntry{}, domain.NewError(domain.ErrCodeDuplicateGrant, "duplicate")
	}
	h := newTestHandler(repo, activeChecker(uuid.New()), &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodPost, "/", grantBody(uuid.New()), tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.Grant(c)
	assertErrorCode(t, w, http.StatusConflict, domain.ErrCodeDuplicateGrant)
}

// ── Revoke (TAC-3) ───────────────────────────────────────────────────────

func revokeBody(version int64) string {
	return `{"record_version":` + strconv.FormatInt(version, 10) + `}`
}

func TestHandler_Revoke_InvalidTenantID_Returns400(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodDelete, "/", revokeBody(1), tenantOwnerCtx(tenant))
	setParams(c, "id", "bad-uuid", "tender_id", uuid.New().String(), "user_id", uuid.New().String())
	h.Revoke(c)
	assertErrorCode(t, w, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
}

func TestHandler_Revoke_InvalidTenderID_Returns400(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodDelete, "/", revokeBody(1), tenantOwnerCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", "bad-uuid", "user_id", uuid.New().String())
	h.Revoke(c)
	assertErrorCode(t, w, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
}

func TestHandler_Revoke_InvalidUserID_Returns400(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodDelete, "/", revokeBody(1), tenantOwnerCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String(), "user_id", "bad-uuid")
	h.Revoke(c)
	assertErrorCode(t, w, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
}

func TestHandler_Revoke_PlainMember_Returns403(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodDelete, "/", revokeBody(1), plainMemberCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String(), "user_id", uuid.New().String())
	h.Revoke(c)
	assertErrorCode(t, w, http.StatusForbidden, domain.ErrCodeInsufficientRole)
}

func TestHandler_Revoke_CrossTenant_Returns403(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	c, w := buildCtx(http.MethodDelete, "/", revokeBody(1), tenantOwnerCtx(uuid.New()))
	pathTenant := uuid.New()
	setParams(c, "id", pathTenant.String(), "tender_id", uuid.New().String(), "user_id", uuid.New().String())
	h.Revoke(c)
	assertErrorCode(t, w, http.StatusForbidden, domain.ErrCodeInsufficientRole)
}

func TestHandler_Revoke_MissingRecordVersion_Returns400(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodDelete, "/", `{}`, tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String(), "user_id", uuid.New().String())
	h.Revoke(c)
	assertErrorCode(t, w, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
}

func TestHandler_Revoke_TenderAdmin_Returns204(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, _ := buildCtx(http.MethodDelete, "/", revokeBody(1), tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String(), "user_id", uuid.New().String())
	h.Revoke(c)
	// c.Writer.Status(), not the raw httptest recorder code: c.Status()
	// with no body write is only flushed to the recorder by gin.Engine's
	// own ServeHTTP, which this direct handler call bypasses.
	assert.Equal(t, http.StatusNoContent, c.Writer.Status())
}

// TestACLGrant_RecordVersion_InitiallyZero verifies the PEND-RECORDVERSION-01
// contract: a freshly-created ACL entry starts with record_version=0. The
// caller must echo this value back in subsequent Revoke calls (LLD §8.4/
// §11.2). The DB trigger bumps the version on every UPDATE, so a fresh row's
// version is always 0.
func TestACLGrant_RecordVersion_InitiallyZero(t *testing.T) {
	h := newTestHandler(emptyRepo(), activeChecker(uuid.New()), &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodPost, "/", grantBody(uuid.New()), tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.Grant(c)
	require.Equal(t, http.StatusCreated, w.Code)
	var body ACLResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, int64(0), body.RecordVersion, "fresh grant must have record_version=0 (LLD §8.4)")
}

// TestACLGrant_NoContentType verifies that gin's ShouldBindJSON does NOT
// inspect the Content-Type header — a valid JSON body without Content-Type
// still binds correctly and produces 201. This is gin's documented behavior
// (ShouldBindJSON always parses as JSON regardless of Content-Type).
func TestACLGrant_NoContentType(t *testing.T) {
	h := newTestHandler(emptyRepo(), activeChecker(uuid.New()), &fakeCache{}, t)
	tenant := uuid.New()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	// Deliberately omit Content-Type header.
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(grantBody(uuid.New())))
	rc := tenderAdminCtx(tenant)
	req = req.WithContext(WithContext(req.Context(), rc))
	c.Request = req
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.Grant(c)
	assert.Equal(t, http.StatusCreated, w.Code)
}

// TestHandler_Revoke_VersionConflict_Returns409 covers the optimistic-lock
// contract at the handler layer (LLD §11.2/§12.1): a repo reporting zero
// rows affected — whether from a stale version or an already-revoked row —
// surfaces as 409 optimistic_lock_conflict, not a silent 204.
func TestHandler_Revoke_VersionConflict_Returns409(t *testing.T) {
	repo := emptyRepo()
	repo.revokeFn = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int64) error {
		return domain.NewError(domain.ErrCodeOptimisticLockConflict, "record version conflict")
	}
	h := newTestHandler(repo, &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodDelete, "/", revokeBody(1), tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String(), "user_id", uuid.New().String())
	h.Revoke(c)
	assertErrorCode(t, w, http.StatusConflict, domain.ErrCodeOptimisticLockConflict)
}

// ── respondACLError — all switch branches ─────────────────────────────────────

func callRespondACLError(t *testing.T, err error) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	respondACLError(c, err)
	return w
}

func TestRespondACLError_NoDomainCode_Returns500(t *testing.T) {
	w := callRespondACLError(t, errors.New("plain error"))
	assertErrorCode(t, w, http.StatusInternalServerError, domain.ErrCodeInternal)
}

func TestRespondACLError_InvalidRequest_400(t *testing.T) {
	w := callRespondACLError(t, domain.NewError(domain.ErrCodeInvalidRequest, "bad"))
	assertErrorCode(t, w, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
}

func TestRespondACLError_Unauthorized_401(t *testing.T) {
	w := callRespondACLError(t, domain.NewError(domain.ErrCodeUnauthorized, "unauth"))
	assertErrorCode(t, w, http.StatusUnauthorized, domain.ErrCodeUnauthorized)
}

func TestRespondACLError_InsufficientRole_403(t *testing.T) {
	w := callRespondACLError(t, domain.NewError(domain.ErrCodeInsufficientRole, "role"))
	assertErrorCode(t, w, http.StatusForbidden, domain.ErrCodeInsufficientRole)
}

func TestRespondACLError_InvalidReason_422(t *testing.T) {
	w := callRespondACLError(t, domain.NewError(domain.ErrCodeInvalidReason, "reason"))
	assertErrorCode(t, w, http.StatusUnprocessableEntity, domain.ErrCodeInvalidReason)
}

func TestRespondACLError_InvalidExpiry_422(t *testing.T) {
	w := callRespondACLError(t, domain.NewError(domain.ErrCodeInvalidExpiry, "expiry"))
	assertErrorCode(t, w, http.StatusUnprocessableEntity, domain.ErrCodeInvalidExpiry)
}

func TestRespondACLError_DependencyUnavailable_503(t *testing.T) {
	w := callRespondACLError(t, domain.NewError(domain.ErrCodeDependencyUnavailable, "dep"))
	assertErrorCode(t, w, http.StatusServiceUnavailable, domain.ErrCodeDependencyUnavailable)
}

func TestRespondACLError_UnknownCode_500(t *testing.T) {
	// A domain error whose code matches no switch case → default 500.
	w := callRespondACLError(t, domain.NewError("unknown_future_code", "unknown"))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ── List service error ────────────────────────────────────────────────────────

func TestHandler_List_ServiceError_PropagatesError(t *testing.T) {
	repo := emptyRepo()
	repo.listFn = func(context.Context, uuid.UUID, uuid.UUID) ([]domain.TenderACLEntry, error) {
		return nil, domain.NewError(domain.ErrCodeDependencyUnavailable, "db down")
	}
	h := newTestHandler(repo, &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodGet, "/", "", tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.List(c)
	assertErrorCode(t, w, http.StatusServiceUnavailable, domain.ErrCodeDependencyUnavailable)
}

// ── toACLResponses empty slice ────────────────────────────────────────────────

func TestToACLResponses_EmptySlice_ReturnsEmptySliceNotNil(t *testing.T) {
	result := toACLResponses([]domain.TenderACLEntry{})
	require.NotNil(t, result)
	assert.Len(t, result, 0)
}

// ── TAC2-DEP-02: membership check timeout → 503 ───────────────────────────────

// TestACLGrant_MembershipTimeout_503 covers TAC2-DEP-02: the membership
// checker returns context.DeadlineExceeded (simulating the 300ms
// MEMBERSHIP_CHECK_TIMEOUT_MS firing) → service maps to ErrCodeCoreUnavailable
// → handler returns 503. repo.Grant is never called (fail-closed).
func TestACLGrant_MembershipTimeout_503(t *testing.T) {
	checker := &fakeChecker{existsFn: func(_ context.Context, _, _ uuid.UUID) (bool, uuid.UUID, error) {
		return false, uuid.Nil, context.DeadlineExceeded
	}}
	h := newTestHandler(emptyRepo(), checker, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodPost, "/", grantBody(uuid.New()), tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String())
	h.Grant(c)
	assertErrorCode(t, w, http.StatusServiceUnavailable, domain.ErrCodeCoreUnavailable)
}

// ── CONC-GRANT-01: concurrent grants → one 201, one 409 ─────────────────────

// TestACLGrant_Concurrent_OneSucceedsOneFails covers CONC-GRANT-01: two
// goroutines race to grant the same (tenant, tender, user). The mock repo
// serializes via a mutex — first caller succeeds, second gets DuplicateGrant.
// Exactly one 201 and one 409 must be observed (mirrors DB unique-index
// serialization on uq_tae_active_entry).
func TestACLGrant_Concurrent_OneSucceedsOneFails(t *testing.T) {
	var mu sync.Mutex
	grantCalls := 0
	repo := &fakeRepo{
		grantFn: func(_ context.Context, e domain.TenderACLEntry) (domain.TenderACLEntry, error) {
			mu.Lock()
			defer mu.Unlock()
			if grantCalls > 0 {
				return domain.TenderACLEntry{}, domain.NewError(domain.ErrCodeDuplicateGrant, "duplicate grant")
			}
			grantCalls++
			e.ID = uuid.New()
			return e, nil
		},
		listFn:   func(context.Context, uuid.UUID, uuid.UUID) ([]domain.TenderACLEntry, error) { return nil, nil },
		revokeFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int64) error { return nil },
	}
	tenant, tender, user := uuid.New(), uuid.New(), uuid.New()
	checker := activeChecker(uuid.New())

	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			h := newTestHandler(repo, checker, &fakeCache{}, t)
			c, w := buildCtx(http.MethodPost, "/", grantBody(user), tenderAdminCtx(tenant))
			setParams(c, "id", tenant.String(), "tender_id", tender.String())
			h.Grant(c)
			codes[idx] = w.Code
		}(i)
	}
	wg.Wait()

	assert.ElementsMatch(t, []int{http.StatusCreated, http.StatusConflict}, codes)
}

// ── CONC-REVOKE-01: concurrent revokes → both 204 ───────────────────────────

// TestACLRevoke_Concurrent_BothSucceed covers CONC-REVOKE-01: two goroutines
// revoke the same entry simultaneously. The repo returns nil for both (the
// real DB UPDATE is idempotent — second hits 0 rows, service returns nil).
// Both must receive 204 No Content.
// Note: c.Status(204) only sets gin's internal status — use c.Writer.Status()
// not w.Code, because no body write flushes the header to the recorder.
func TestACLRevoke_Concurrent_BothSucceed(t *testing.T) {
	repo := &fakeRepo{
		revokeFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int64) error { return nil },
		listFn:   func(context.Context, uuid.UUID, uuid.UUID) ([]domain.TenderACLEntry, error) { return nil, nil },
	}
	tenant, tender, user := uuid.New(), uuid.New(), uuid.New()

	statuses := make([]int, 2)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			h := newTestHandler(repo, &fakeChecker{}, &fakeCache{}, t)
			c, _ := buildCtx(http.MethodDelete, "/", revokeBody(1), tenderAdminCtx(tenant))
			setParams(c, "id", tenant.String(), "tender_id", tender.String(), "user_id", user.String())
			h.Revoke(c)
			statuses[idx] = c.Writer.Status()
		}(i)
	}
	wg.Wait()

	assert.Equal(t, http.StatusNoContent, statuses[0])
	assert.Equal(t, http.StatusNoContent, statuses[1])
}

// ── CONC-GRANT-REVOKE-01: concurrent grant + revoke → no orphan ─────────────

// TestACLGrant_Revoke_Concurrent covers CONC-GRANT-REVOKE-01: one goroutine
// grants, one revokes the same entry simultaneously. The mock repo succeeds
// for both operations. The test verifies no deadlock and both operations
// return valid status codes (grant 201, revoke 204).
func TestACLGrant_Revoke_Concurrent(t *testing.T) {
	repo := &fakeRepo{
		grantFn: func(_ context.Context, e domain.TenderACLEntry) (domain.TenderACLEntry, error) {
			e.ID = uuid.New()
			return e, nil
		},
		revokeFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int64) error { return nil },
		listFn:   func(context.Context, uuid.UUID, uuid.UUID) ([]domain.TenderACLEntry, error) { return nil, nil },
	}
	tenant, tender, user := uuid.New(), uuid.New(), uuid.New()
	checker := activeChecker(uuid.New())

	var wg sync.WaitGroup
	var grantCode, revokeCode int

	wg.Add(1)
	go func() {
		defer wg.Done()
		h := newTestHandler(repo, checker, &fakeCache{}, t)
		c, w := buildCtx(http.MethodPost, "/", grantBody(user), tenderAdminCtx(tenant))
		setParams(c, "id", tenant.String(), "tender_id", tender.String())
		h.Grant(c)
		grantCode = w.Code
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		h := newTestHandler(repo, &fakeChecker{}, &fakeCache{}, t)
		c, _ := buildCtx(http.MethodDelete, "/", revokeBody(1), tenderAdminCtx(tenant))
		setParams(c, "id", tenant.String(), "tender_id", tender.String(), "user_id", user.String())
		h.Revoke(c)
		revokeCode = c.Writer.Status() // 204 with no body: use gin's internal status
	}()

	wg.Wait()

	assert.Equal(t, http.StatusCreated, grantCode)
	assert.Equal(t, http.StatusNoContent, revokeCode)
}
