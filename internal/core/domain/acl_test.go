package domain

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── TenderACLLevel.Valid ──────────────────────────────────────────────────────

func TestTenderACLLevel_Valid_AllValidLevels(t *testing.T) {
	for _, lvl := range []TenderACLLevel{ACLView, ACLEdit, ACLApprove} {
		assert.True(t, lvl.Valid(), "expected %q to be valid", lvl)
	}
}

func TestTenderACLLevel_Valid_InvalidLevel_False(t *testing.T) {
	assert.False(t, TenderACLLevel("wizard").Valid())
}

func TestTenderACLLevel_Valid_EmptyLevel_False(t *testing.T) {
	assert.False(t, TenderACLLevel("").Valid())
}

// ── TenderACLEntry.IsActive ───────────────────────────────────────────────────

func TestTenderACLEntry_IsActive_NoDeletedAtNoExpiry_True(t *testing.T) {
	e := &TenderACLEntry{ID: uuid.New()}
	assert.True(t, e.IsActive(time.Now()))
}

func TestTenderACLEntry_IsActive_DeletedAt_False(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Minute)
	e := &TenderACLEntry{ID: uuid.New(), DeletedAt: &past}
	assert.False(t, e.IsActive(now))
}

func TestTenderACLEntry_IsActive_ExpiresAtExactNow_False(t *testing.T) {
	now := time.Now()
	e := &TenderACLEntry{ID: uuid.New(), ExpiresAt: &now}
	assert.False(t, e.IsActive(now), "expires_at == now is not after now → inactive")
}

func TestTenderACLEntry_IsActive_ExpiresAtInPast_False(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)
	e := &TenderACLEntry{ID: uuid.New(), ExpiresAt: &past}
	assert.False(t, e.IsActive(now))
}

func TestTenderACLEntry_IsActive_FutureExpiry_True(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	e := &TenderACLEntry{ID: uuid.New(), ExpiresAt: &future}
	assert.True(t, e.IsActive(now))
}

func TestCachedAccess_JSONRoundTrip(t *testing.T) {
	level := "edit"
	expires := time.Now().Truncate(time.Second).UTC()
	original := CachedAccess{HasAccess: true, AccessLevel: &level, ExpiresAt: &expires}

	raw, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded CachedAccess
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, original.HasAccess, decoded.HasAccess)
	require.NotNil(t, decoded.AccessLevel)
	assert.Equal(t, *original.AccessLevel, *decoded.AccessLevel)
	require.NotNil(t, decoded.ExpiresAt)
	assert.True(t, original.ExpiresAt.Equal(*decoded.ExpiresAt))
}

func TestCachedAccess_NoAccess_JSONOmitsOptionalFields(t *testing.T) {
	original := CachedAccess{HasAccess: false}
	raw, err := json.Marshal(original)
	require.NoError(t, err)

	var asMap map[string]any
	require.NoError(t, json.Unmarshal(raw, &asMap))
	_, hasLevel := asMap["access_level"]
	_, hasExpiry := asMap["expires_at"]
	assert.False(t, hasLevel, "access_level must be omitted, not null, for has_access:false")
	assert.False(t, hasExpiry, "expires_at must be omitted, not null, for has_access:false")
}
