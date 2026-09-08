package valkey

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// TestKeyFormat locks in the tac:acl:{tenant}:{tender}:{user} key shape
// (LLD §9) — a real Valkey round-trip is exercised by the testcontainers-
// backed integration suite (test/integration), not here.
func TestKeyFormat(t *testing.T) {
	tenantID, tenderID, userID := uuid.New(), uuid.New(), uuid.New()
	got := key(tenantID, tenderID, userID)
	want := "tac:acl:" + tenantID.String() + ":" + tenderID.String() + ":" + userID.String()
	assert.Equal(t, want, got)
}

// ── Constructor smoke tests ───────────────────────────────────────────────────

// TestNewClient_ReturnsNonNil verifies that NewClient returns a non-nil
// *redis.Client without dialing (the client is lazy).
func TestNewClient_ReturnsNonNil(t *testing.T) {
	client := NewClient(ClientConfig{Addr: "localhost:6379"})
	assert.NotNil(t, client)
	_ = client.Close()
}

// TestNewCache_ReturnsNonNil verifies that NewCache wraps the client and
// returns a non-nil *Cache.
func TestNewCache_ReturnsNonNil(t *testing.T) {
	client := NewClient(ClientConfig{Addr: "localhost:6379"})
	cache := NewCache(client)
	assert.NotNil(t, cache)
	_ = client.Close()
}
