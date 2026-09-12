package valkey

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
)

// unreachableCache builds a Cache pointed at an address nothing listens on.
// Paired with an already-canceled context, go-redis returns "context
// canceled" deterministically without any real network I/O — no testcontainers
// dependency needed to exercise these error branches.
func unreachableCache() *Cache {
	return NewCache(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestCache_Get_ClientError_Wrapped(t *testing.T) {
	c := unreachableCache()
	got, hit, err := c.Get(canceledContext(), uuid.New(), uuid.New(), uuid.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cache: get:")
	assert.False(t, hit)
	assert.Nil(t, got)
}

func TestCache_Set_ClientError_Wrapped(t *testing.T) {
	c := unreachableCache()
	err := c.Set(canceledContext(), uuid.New(), uuid.New(), uuid.New(), domain.CachedAccess{HasAccess: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cache: set:")
}

// TestCache_Set_MarshalError_Wrapped covers Set's json.Marshal error path —
// no client I/O involved at all: encoding/json's time.Time.MarshalJSON
// returns an error for a year outside [0,9999], a well-known stdlib
// limitation that fires before Set ever reaches the client.
func TestCache_Set_MarshalError_Wrapped(t *testing.T) {
	c := unreachableCache()
	badTime := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	err := c.Set(context.Background(), uuid.New(), uuid.New(), uuid.New(), domain.CachedAccess{HasAccess: true, ExpiresAt: &badTime})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cache: marshal:")
}

func TestCache_Delete_ClientError_Wrapped(t *testing.T) {
	c := unreachableCache()
	err := c.Delete(canceledContext(), uuid.New(), uuid.New(), uuid.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cache: del:")
}

func TestCache_Ping_ClientError_Wrapped(t *testing.T) {
	c := unreachableCache()
	err := c.Ping(canceledContext())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cache: ping:")
}
