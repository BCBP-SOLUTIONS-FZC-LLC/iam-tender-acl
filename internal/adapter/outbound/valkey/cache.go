// Package valkey implements port.Cache backed by Valkey (Redis-compatible)
// — this service's only cache region (LLD §9): tac:acl:{tenant}:{tender}:
// {user}, 30s TTL, read/written only by TAC-4/CheckAccess. TAC-1 is
// deliberately not cached (low-frequency, admin-gated read — LLD §17.4/
// TAC-Q5).
package valkey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/port"
)

const ttl = 30 * time.Second

// ClientConfig configures the underlying Valkey/Redis client.
type ClientConfig struct {
	Addr     string
	Password string
	DB       int
}

// NewClient builds a go-redis client from cfg.
func NewClient(cfg ClientConfig) *redis.Client {
	return redis.NewClient(&redis.Options{Addr: cfg.Addr, Password: cfg.Password, DB: cfg.DB})
}

// Cache implements port.Cache against Valkey.
type Cache struct {
	client *redis.Client
}

var _ port.Cache = (*Cache)(nil)

// NewCache builds a Cache from an already-constructed go-redis client.
func NewCache(client *redis.Client) *Cache { return &Cache{client: client} }

func key(tenantID, tenderID, userID uuid.UUID) string {
	return fmt.Sprintf("tac:acl:%s:%s:%s", tenantID, tenderID, userID)
}

// Get implements the read side of the Cache-Aside pattern used by TAC-4.
// A cache read failure is not returned as fatal by convention — callers
// should log and fall through to Postgres — but the error is still
// surfaced here so the caller can decide.
func (c *Cache) Get(ctx context.Context, tenantID, tenderID, userID uuid.UUID) (*domain.CachedAccess, bool, error) {
	raw, err := c.client.Get(ctx, key(tenantID, tenderID, userID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("cache: get: %w", err)
	}

	var v domain.CachedAccess
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false, fmt.Errorf("cache: unmarshal: %w", err)
	}
	return &v, true, nil
}

// Set implements the write side of the Cache-Aside pattern, with the LLD
// §9 fixed 30s TTL.
func (c *Cache) Set(ctx context.Context, tenantID, tenderID, userID uuid.UUID, value domain.CachedAccess) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("cache: marshal: %w", err)
	}
	if err := c.client.Set(ctx, key(tenantID, tenderID, userID), payload, ttl).Err(); err != nil {
		return fmt.Errorf("cache: set: %w", err)
	}
	return nil
}

// Delete evicts the tac:acl:* entry for one (tenant, tender, user) tuple.
func (c *Cache) Delete(ctx context.Context, tenantID, tenderID, userID uuid.UUID) error {
	if err := c.client.Del(ctx, key(tenantID, tenderID, userID)).Err(); err != nil {
		return fmt.Errorf("cache: del: %w", err)
	}
	return nil
}

// Ping verifies connectivity, for use by readiness probes.
func (c *Cache) Ping(ctx context.Context) error {
	if err := c.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("cache: ping: %w", err)
	}
	return nil
}
