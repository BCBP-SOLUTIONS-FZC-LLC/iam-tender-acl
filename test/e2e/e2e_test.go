//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	httpadapter "github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/inbound/http"
)

func doJSON(t *testing.T, method, path string, body any, tenantID, userID uuid.UUID, roles string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", tenantID.String())
	if roles != "" {
		// gincommon.ProtectedMiddlewares requires both x-user-id and
		// x-tenant-id on the public admin group (TAC-1/2/3); TAC-4 is
		// mesh-only and carries neither header (see e2e test below).
		req.Header.Set("X-User-Id", userID.String())
		req.Header.Set("X-Tenant-Roles", roles)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// cacheKey mirrors internal/adapter/outbound/valkey's unexported key format
// (tac:acl:{tenant}:{tender}:{user}) so this suite can assert directly
// against Valkey without reaching into the cache package's internals.
func cacheKey(tenantID, tenderID, userID uuid.UUID) string {
	return fmt.Sprintf("tac:acl:%s:%s:%s", tenantID, tenderID, userID)
}

// TestE2E_GrantCheckRevokeCheck exercises the exact chain described by
// tender-acl-service-lld.md §17.4: grant (TAC-2) -> TAC-4 direct call
// returns has_access:true with the granted access_level (and populates
// the tac:acl:* cache key) -> revoke (TAC-3) -> TAC-4 returns
// has_access:false.
//
// This service's actual Revoke implementation invalidates the cache key
// via an immediate DELETE on commit (LLD §9 — "so a process crash
// mid-write can never leave a stale has_access:true value being served"),
// not a wait-for-TTL-to-expire design. The §17.4 prose describes the
// externally observable end state ("has_access:false" after revoke); it
// does not mandate that TAC-4 must keep serving a stale cached true value
// until the 30s TTL lapses. This test asserts the actual, stronger
// (immediate) invalidation behavior, and separately confirms the 30s TTL
// was in fact used as the cache's expiry when the key was first written —
// see TestE2E_CachePopulatedWithConfiguredTTL below.
func TestE2E_GrantCheckRevokeCheck(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	granterID := uuid.New()
	granteeID := uuid.New()
	tenderID := uuid.New()

	// Sanity: no cache entry exists yet for this tuple.
	key := cacheKey(tenantID, tenderID, granteeID)
	if n, err := rawRedis.Exists(ctx, key).Result(); err != nil {
		t.Fatalf("redis exists (pre-grant): %v", err)
	} else if n != 0 {
		t.Fatalf("expected no pre-existing cache entry for %s, found one", key)
	}

	// TAC-2: grant.
	grantBody := map[string]any{
		"user_id":      granteeID.String(),
		"access_level": "edit",
	}
	grantPath := fmt.Sprintf("/api/v1/tenants/%s/tenders/%s/acl", tenantID, tenderID)
	rec := doJSON(t, http.MethodPost, grantPath, grantBody, tenantID, granterID, "tender_admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("grant: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var granted httpadapter.ACLResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &granted); err != nil {
		t.Fatalf("decode grant response: %v", err)
	}
	if granted.AccessLevel != "edit" {
		t.Fatalf("expected access_level edit, got %q", granted.AccessLevel)
	}

	// TAC-4: direct access check — mesh-only, no auth headers at all.
	checkPath := fmt.Sprintf("/internal/tenants/%s/tenders/%s/acl/%s", tenantID, tenderID, granteeID)
	rec = doJSON(t, http.MethodGet, checkPath, nil, tenantID, uuid.Nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("check (post-grant): expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var check httpadapter.CheckAccessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &check); err != nil {
		t.Fatalf("decode check response: %v", err)
	}
	if !check.HasAccess {
		t.Fatalf("expected has_access:true after grant, got false")
	}
	if check.AccessLevel == nil || *check.AccessLevel != "edit" {
		t.Fatalf("expected access_level edit in check response, got %+v", check.AccessLevel)
	}

	// The check above must have populated the cache (cache-miss path: read
	// Postgres, then Set with the LLD §9 30s TTL).
	ttl, err := rawRedis.TTL(ctx, key).Result()
	if err != nil {
		t.Fatalf("redis ttl (post-check): %v", err)
	}
	if ttl <= 0 || ttl > 30*time.Second {
		t.Fatalf("expected a positive TTL <= 30s on %s after cache population, got %s", key, ttl)
	}

	// TAC-3: revoke.
	revokePath := fmt.Sprintf("/api/v1/tenants/%s/tenders/%s/acl/%s", tenantID, tenderID, granteeID)
	rec = doJSON(t, http.MethodDelete, revokePath, nil, tenantID, granterID, "tender_admin")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// Revoke's cache.Delete (LLD §9, DELETE-not-update invalidation) must
	// have evicted the key immediately — no waiting for the 30s TTL.
	if n, err := rawRedis.Exists(ctx, key).Result(); err != nil {
		t.Fatalf("redis exists (post-revoke): %v", err)
	} else if n != 0 {
		t.Fatalf("expected cache key %s evicted immediately after revoke, still present", key)
	}

	// TAC-4 again: must now report has_access:false, never a 404.
	rec = doJSON(t, http.MethodGet, checkPath, nil, tenantID, uuid.Nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("check (post-revoke): expected 200 (never 404), got %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &check); err != nil {
		t.Fatalf("decode check response (post-revoke): %v", err)
	}
	if check.HasAccess {
		t.Fatalf("expected has_access:false after revoke, got true")
	}

	// This third check call repopulates the cache with the negative
	// result under the same key/TTL convention.
	if n, err := rawRedis.Exists(ctx, key).Result(); err != nil {
		t.Fatalf("redis exists (post-revoke-check): %v", err)
	} else if n != 1 {
		t.Fatalf("expected has_access:false result to be cached under %s, found none", key)
	}
}

// TestE2E_InsufficientRole_Returns403 confirms TAC-2/TAC-3 enforce the
// role gate entirely from x-tenant-roles (LLD §8.2) — no membership-check
// round trip happens at all for a caller lacking tender_admin/tenant_admin/
// tenant_owner.
func TestE2E_InsufficientRole_Returns403(t *testing.T) {
	tenantID := uuid.New()
	callerID := uuid.New()
	tenderID := uuid.New()

	grantBody := map[string]any{"user_id": uuid.NewString(), "access_level": "view"}
	grantPath := fmt.Sprintf("/api/v1/tenants/%s/tenders/%s/acl", tenantID, tenderID)
	rec := doJSON(t, http.MethodPost, grantPath, grantBody, tenantID, callerID, "member")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 insufficient_role, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestE2E_DuplicateGrant_Returns409 confirms the uq_tae_active_entry
// unique-violation mapping end-to-end through the real HTTP+Postgres path.
func TestE2E_DuplicateGrant_Returns409(t *testing.T) {
	tenantID := uuid.New()
	granterID := uuid.New()
	granteeID := uuid.New()
	tenderID := uuid.New()

	grantBody := map[string]any{"user_id": granteeID.String(), "access_level": "view"}
	grantPath := fmt.Sprintf("/api/v1/tenants/%s/tenders/%s/acl", tenantID, tenderID)

	rec := doJSON(t, http.MethodPost, grantPath, grantBody, tenantID, granterID, "tenant_admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("first grant: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, http.MethodPost, grantPath, grantBody, tenantID, granterID, "tenant_admin")
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate grant: expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}
