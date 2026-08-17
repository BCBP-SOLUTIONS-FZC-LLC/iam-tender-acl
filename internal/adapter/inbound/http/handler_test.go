package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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
	revokeFn                 func(ctx context.Context, tenantID, tenderID, userID uuid.UUID) (bool, error)
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
func (f *fakeRepo) Revoke(ctx context.Context, tenantID, tenderID, userID uuid.UUID) (bool, error) {
	return f.revokeFn(ctx, tenantID, tenderID, userID)
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

func newTestHandler(repo port.TenderACLRepository, checker port.MembershipCheckClient, c port.Cache, t *testing.T) *Handler {
	t.Helper()
	svc := service.NewACLService(repo, checker, c, fakeMetrics{}, slog.Default(), otel.Tracer("test"))
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
		revokeFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (bool, error) {
			return true, nil
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

func TestHandler_Revoke_InvalidTenantID_Returns400(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodDelete, "/", ``, tenantOwnerCtx(tenant))
	setParams(c, "id", "bad-uuid", "tender_id", uuid.New().String(), "user_id", uuid.New().String())
	h.Revoke(c)
	assertErrorCode(t, w, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
}

func TestHandler_Revoke_InvalidTenderID_Returns400(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodDelete, "/", ``, tenantOwnerCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", "bad-uuid", "user_id", uuid.New().String())
	h.Revoke(c)
	assertErrorCode(t, w, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
}

func TestHandler_Revoke_InvalidUserID_Returns400(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodDelete, "/", ``, tenantOwnerCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String(), "user_id", "bad-uuid")
	h.Revoke(c)
	assertErrorCode(t, w, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
}

func TestHandler_Revoke_PlainMember_Returns403(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, w := buildCtx(http.MethodDelete, "/", ``, plainMemberCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String(), "user_id", uuid.New().String())
	h.Revoke(c)
	assertErrorCode(t, w, http.StatusForbidden, domain.ErrCodeInsufficientRole)
}

func TestHandler_Revoke_CrossTenant_Returns403(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	c, w := buildCtx(http.MethodDelete, "/", ``, tenantOwnerCtx(uuid.New()))
	pathTenant := uuid.New()
	setParams(c, "id", pathTenant.String(), "tender_id", uuid.New().String(), "user_id", uuid.New().String())
	h.Revoke(c)
	assertErrorCode(t, w, http.StatusForbidden, domain.ErrCodeInsufficientRole)
}

func TestHandler_Revoke_TenderAdmin_Returns204(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, _ := buildCtx(http.MethodDelete, "/", ``, tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String(), "user_id", uuid.New().String())
	h.Revoke(c)
	// c.Writer.Status(), not the raw httptest recorder code: c.Status()
	// with no body write is only flushed to the recorder by gin.Engine's
	// own ServeHTTP, which this direct handler call bypasses.
	assert.Equal(t, http.StatusNoContent, c.Writer.Status())
}

// TestHandler_Revoke_AlreadyRevoked_StillReturns204 covers the "idempotent
// in effect" contract at the handler layer: a repo reporting found=false
// (nothing to revoke) is still a successful 204, not a 404 (LLD §12.2 — no
// optimistic-lock/not-found distinction on Revoke, see
// IMPLEMENTATION_GAP_ANALYSIS.md).
func TestHandler_Revoke_AlreadyRevoked_StillReturns204(t *testing.T) {
	repo := emptyRepo()
	repo.revokeFn = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (bool, error) {
		return false, nil
	}
	h := newTestHandler(repo, &fakeChecker{}, &fakeCache{}, t)
	tenant := uuid.New()
	c, _ := buildCtx(http.MethodDelete, "/", ``, tenderAdminCtx(tenant))
	setParams(c, "id", tenant.String(), "tender_id", uuid.New().String(), "user_id", uuid.New().String())
	h.Revoke(c)
	assert.Equal(t, http.StatusNoContent, c.Writer.Status())
}
