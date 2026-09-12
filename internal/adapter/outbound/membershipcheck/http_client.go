// Package membershipcheck is the HTTP adapter implementing
// port.MembershipCheckClient against iam-org-membership — replacing the
// composite FK this table lost when it moved out of Core's database
// (docs/lld/iam-lld-tender-acl-service.md §6, §7.6.2). It exists specifically so Wave 4
// (folding this service into the Tender Service, ADR-0007 Option D) can
// repoint or delete this dependency cheaply.
package membershipcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/port"
)

// defaultTimeout is the LLD §15 configured client timeout (300ms) — not to
// be confused with §7.6.2's "≤50ms p99" figure, which is a latency
// expectation, not the enforced timeout.
const defaultTimeout = 300 * time.Millisecond

// HTTPChecker implements port.MembershipCheckClient against
// iam-org-membership's internal GET
// /api/v1/internal/tenants/{tenantID}/members/{userID}/exists endpoint.
type HTTPChecker struct {
	baseURL    string
	httpClient *http.Client
}

var _ port.MembershipCheckClient = (*HTTPChecker)(nil)

// NewHTTPChecker builds an HTTPChecker. If httpClient is nil, a client with
// timeout (or defaultTimeout, if timeout <= 0) is used.
func NewHTTPChecker(baseURL string, httpClient *http.Client, timeout time.Duration) *HTTPChecker {
	if httpClient == nil {
		if timeout <= 0 {
			timeout = defaultTimeout
		}
		httpClient = &http.Client{Timeout: timeout}
	}
	return &HTTPChecker{baseURL: strings.TrimRight(baseURL, "/"), httpClient: httpClient}
}

// setInternalHeaders authenticates as the reserved iam-system principal.
// iam-org-membership's routes run behind platform-gincommon's
// ProtectedMiddlewares, which requires a well-formed UUID in x-tenant-id
// (not just x-user-id) and reads role membership from x-tenant-roles (not
// X-User-Roles) — the previous X-User-Id/X-User-Roles-only pattern here
// 401/403'd on every call.
func setInternalHeaders(req *http.Request, tenantID uuid.UUID) {
	req.Header.Set("x-user-id", "iam-system")
	req.Header.Set("x-tenant-id", tenantID.String())
	req.Header.Set("x-tenant-roles", "iam-system")
}

type existsResponse struct {
	Active             bool       `json:"active"`
	TenantMembershipID *uuid.UUID `json:"tenant_membership_id,omitempty"`
}

// Exists calls Core's membership-existence endpoint. This client fails
// CLOSED: any network error, timeout, or non-2xx status is returned as an
// error, never treated as "not active" and never defaulted to "active".
func (c *HTTPChecker) Exists(ctx context.Context, tenantID, userID uuid.UUID) (bool, uuid.UUID, error) {
	url := fmt.Sprintf("%s/api/v1/internal/tenants/%s/members/%s/exists", c.baseURL, tenantID, userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return false, uuid.UUID{}, fmt.Errorf("membershipcheck: build request: %w", err)
	}
	setInternalHeaders(req, tenantID)
	propagateTraceparent(ctx, req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, uuid.UUID{}, fmt.Errorf("membershipcheck: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // best-effort close, nothing actionable on failure

	if resp.StatusCode != http.StatusOK {
		return false, uuid.UUID{}, fmt.Errorf("membershipcheck: unexpected status %d", resp.StatusCode)
	}

	var out existsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, uuid.UUID{}, fmt.Errorf("membershipcheck: decode response: %w", err)
	}
	if !out.Active {
		return false, uuid.UUID{}, nil
	}

	// The provider contract (LLD §7.6.2, TAC-D11) always returns
	// tenant_membership_id alongside active:true — this field replaces the
	// composite FK this table lost, and tender_acl_entries.tenant_membership_id
	// is NOT NULL. A response missing it is a contract violation, not a
	// valid "active" result: fail closed rather than writing a placeholder
	// zero UUID into the one integrity guarantee this field exists to provide.
	if out.TenantMembershipID == nil || *out.TenantMembershipID == uuid.Nil {
		return false, uuid.UUID{}, fmt.Errorf("membershipcheck: active:true response missing tenant_membership_id")
	}
	return true, *out.TenantMembershipID, nil
}
