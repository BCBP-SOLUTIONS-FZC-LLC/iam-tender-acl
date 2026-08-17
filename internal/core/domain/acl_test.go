package domain

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
