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
	"go.opentelemetry.io/otel/trace"
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

// TestHTTPChecker_Exists_PropagatesTraceparent covers the fix linking this
// outbound call's span to Core's, so a request into tender-acl that
// triggers TAC-2's grant-time check shows as one connected trace instead of
// two disconnected ones.
func TestHTTPChecker_Exists_PropagatesTraceparent(t *testing.T) {
	var gotTraceparent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"active": true})
	}))
	defer server.Close()

	traceID, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	spanID, _ := trace.SpanIDFromHex("1112131415161718")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID,
		TraceFlags: trace.FlagsSampled, Remote: true,
	})
	ctx := trace.ContextWithSpanContext(t.Context(), sc)

	checker := NewHTTPChecker(server.URL, nil, 0)
	_, _, err := checker.Exists(ctx, uuid.New(), uuid.New())
	require.NoError(t, err)
	assert.Equal(t, "00-0102030405060708090a0b0c0d0e0f10-1112131415161718-01", gotTraceparent)
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

// TestHTTPChecker_Exists_ActiveWithNoMembershipID covers the branch where the
// response is active:true but tenant_membership_id is omitted — the client
// must return (true, zero-UUID, nil) rather than an error.
func TestHTTPChecker_Exists_ActiveWithNoMembershipID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// active:true but NO tenant_membership_id field
		_, _ = w.Write([]byte(`{"active":true}`))
	}))
	defer server.Close()

	checker := NewHTTPChecker(server.URL, nil, 0)
	active, membershipID, err := checker.Exists(t.Context(), uuid.New(), uuid.New())
	require.NoError(t, err)
	assert.True(t, active)
	assert.Equal(t, uuid.UUID{}, membershipID, "no membership ID in response → zero UUID")
}

func TestHTTPChecker_Exists_NetworkError_ReturnsError(t *testing.T) {
	// Nothing listening on this port.
	checker := NewHTTPChecker("http://127.0.0.1:1", nil, 50*time.Millisecond)
	_, _, err := checker.Exists(t.Context(), uuid.New(), uuid.New())
	require.Error(t, err)
}

// TestHTTPChecker_Exists_InvalidURL_ReturnsError covers the
// http.NewRequestWithContext error path — a URL containing a control
// character is malformed and the request cannot be built.
func TestHTTPChecker_Exists_InvalidURL_ReturnsError(t *testing.T) {
	checker := NewHTTPChecker("http://invalid\x00host", nil, 50*time.Millisecond)
	_, _, err := checker.Exists(t.Context(), uuid.New(), uuid.New())
	require.Error(t, err)
}
