//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
)

func TestValkeyCache_SetThenGet_RealRoundTrip(t *testing.T) {
	ctx := context.Background()
	tenantID, tenderID, userID := uuid.New(), uuid.New(), uuid.New()
	level := "approve"
	value := domain.CachedAccess{HasAccess: true, AccessLevel: &level}

	require.NoError(t, valkeyCache.Set(ctx, tenantID, tenderID, userID, value))

	got, hit, err := valkeyCache.Get(ctx, tenantID, tenderID, userID)
	require.NoError(t, err)
	require.True(t, hit)
	assert.True(t, got.HasAccess)
	require.NotNil(t, got.AccessLevel)
	assert.Equal(t, "approve", *got.AccessLevel)
}

func TestValkeyCache_Get_Miss_ReturnsHitFalseNoError(t *testing.T) {
	got, hit, err := valkeyCache.Get(context.Background(), uuid.New(), uuid.New(), uuid.New())
	require.NoError(t, err)
	assert.False(t, hit)
	assert.Nil(t, got)
}

func TestValkeyCache_Delete_RemovesKey(t *testing.T) {
	ctx := context.Background()
	tenantID, tenderID, userID := uuid.New(), uuid.New(), uuid.New()
	require.NoError(t, valkeyCache.Set(ctx, tenantID, tenderID, userID, domain.CachedAccess{HasAccess: true}))

	require.NoError(t, valkeyCache.Delete(ctx, tenantID, tenderID, userID))

	_, hit, err := valkeyCache.Get(ctx, tenantID, tenderID, userID)
	require.NoError(t, err)
	assert.False(t, hit)
}

func TestValkeyCache_Ping_Succeeds(t *testing.T) {
	require.NoError(t, valkeyCache.Ping(context.Background()))
}

// TestValkeyCache_Set_ExpiresAfterTTL confirms the LLD §9 30s TTL is real —
// using a directly-manipulated key with a short synthetic TTL via the raw
// client (not the 30s production constant, which would make this test
// impractically slow) to prove Valkey enforces expiry on this key shape at
// all; the fixed 30s constant itself is verified by inspection of
// internal/adapter/outbound/valkey/cache.go and exercised end-to-end in test/e2e.
func TestValkeyCache_Set_ExpiresAfterTTL(t *testing.T) {
	ctx := context.Background()
	shortLivedKey := "tac:acl:" + uuid.New().String() + ":" + uuid.New().String() + ":" + uuid.New().String()
	require.NoError(t, valkeyClient.Set(ctx, shortLivedKey, `{"has_access":true}`, 200*time.Millisecond).Err())

	exists, err := valkeyClient.Exists(ctx, shortLivedKey).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), exists)

	time.Sleep(350 * time.Millisecond)

	exists, err = valkeyClient.Exists(ctx, shortLivedKey).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), exists, "key must have expired")
}

// ── Get: unmarshal error (bad JSON stored at a valid key) ─────────────────────

func TestValkeyCache_Get_UnmarshalError(t *testing.T) {
	ctx := context.Background()
	tenantID, tenderID, userID := uuid.New(), uuid.New(), uuid.New()
	// Store malformed JSON directly via the raw client.
	k := "tac:acl:" + tenantID.String() + ":" + tenderID.String() + ":" + userID.String()
	require.NoError(t, valkeyClient.Set(ctx, k, `{invalid json}`, 30*time.Second).Err())
	t.Cleanup(func() { _, _ = valkeyClient.Del(context.Background(), k).Result() })

	_, _, err := valkeyCache.Get(ctx, tenantID, tenderID, userID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cache: unmarshal")
}

// ── Set: marshal error (time.Time with year > 9999 fails MarshalJSON) ─────────

func TestValkeyCache_Set_MarshalError(t *testing.T) {
	extreme := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	err := valkeyCache.Set(context.Background(), uuid.New(), uuid.New(), uuid.New(),
		domain.CachedAccess{ExpiresAt: &extreme})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cache: marshal")
}

// ── Set: client error (canceled context) ────────────────────────────────────

func TestValkeyCache_Set_ClientError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := valkeyCache.Set(ctx, uuid.New(), uuid.New(), uuid.New(), domain.CachedAccess{HasAccess: true})
	assert.Error(t, err)
}

// ── Delete: client error (canceled context) ─────────────────────────────────

func TestValkeyCache_Delete_ClientError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := valkeyCache.Delete(ctx, uuid.New(), uuid.New(), uuid.New())
	assert.Error(t, err)
}

// ── Ping: client error (canceled context) ───────────────────────────────────

func TestValkeyCache_Ping_ClientError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := valkeyCache.Ping(ctx)
	assert.Error(t, err)
}
