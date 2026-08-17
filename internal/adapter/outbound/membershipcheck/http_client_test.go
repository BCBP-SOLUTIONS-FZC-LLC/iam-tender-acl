package membershipcheck

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPChecker_Exists_Active(t *testing.T) {
	tenantID, userID, membershipID := uuid.New(), uuid.New(), uuid.New()
	var gotPath string
	var gotUserID, gotUserRoles string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUserID = r.Header.Get("X-User-Id")
		gotUserRoles = r.Header.Get("X-User-Roles")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "tenant_membership_id": membershipID.String()})
	}))
	defer server.Close()

	checker := NewHTTPChecker(server.URL, nil, 0)
	active, gotMembershipID, err := checker.Exists(t.Context(), tenantID, userID)
	require.NoError(t, err)
	assert.True(t, active)
	assert.Equal(t, membershipID, gotMembershipID)
	assert.Equal(t, "/internal/tenants/"+tenantID.String()+"/members/"+userID.String()+"/exists", gotPath)
	assert.Equal(t, "iam-system", gotUserID, "must authenticate as the reserved iam-system principal")
	assert.Equal(t, "iam-system", gotUserRoles)
}

func TestHTTPChecker_Exists_NotActive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"active": false})
	}))
	defer server.Close()

	checker := NewHTTPChecker(server.URL, nil, 0)
	active, _, err := checker.Exists(t.Context(), uuid.New(), uuid.New())
	require.NoError(t, err)
	assert.False(t, active)
}

// TestHTTPChecker_Exists_NonOKStatus_FailsClosed covers TAC-FAIL-1: any
// non-2xx response must surface as an error (mapped by the caller to 503
// core_unavailable), never as "not active" and never as "active".
func TestHTTPChecker_Exists_NonOKStatus_FailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	checker := NewHTTPChecker(server.URL, nil, 0)
	active, _, err := checker.Exists(t.Context(), uuid.New(), uuid.New())
	require.Error(t, err)
	assert.False(t, active)
}

func TestHTTPChecker_Exists_MalformedJSON_ReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer server.Close()

	checker := NewHTTPChecker(server.URL, nil, 0)
	_, _, err := checker.Exists(t.Context(), uuid.New(), uuid.New())
	require.Error(t, err)
}

// TestHTTPChecker_Exists_Timeout_ReturnsError covers the LLD §15 300ms
// client-side timeout: a slow provider must fail the check (fail closed),
// not hang indefinitely or default to active.
func TestHTTPChecker_Exists_Timeout_ReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"active": true})
	}))
	defer server.Close()

	checker := NewHTTPChecker(server.URL, nil, 20*time.Millisecond)
	_, _, err := checker.Exists(t.Context(), uuid.New(), uuid.New())
	require.Error(t, err)
}

func TestHTTPChecker_Exists_NetworkError_ReturnsError(t *testing.T) {
	// Nothing listening on this port.
	checker := NewHTTPChecker("http://127.0.0.1:1", nil, 50*time.Millisecond)
	_, _, err := checker.Exists(t.Context(), uuid.New(), uuid.New())
	require.Error(t, err)
}
