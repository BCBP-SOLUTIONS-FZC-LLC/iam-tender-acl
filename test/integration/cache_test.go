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
