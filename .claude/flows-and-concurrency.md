# Flows, concurrency, and shutdown

## Grant flow (TAC-2)

`ACLService.Grant` (span `service.ACLService.Grant`):

1. Validate `level.Valid()`, `len(reason) <= 500`, `expiresAt` in the future (or nil) — domain
   errors, no I/O.
2. `s.checker.Exists(ctx, tenantID, userID)` — the **one** synchronous outbound call this whole
   service makes. Any network error, timeout, or non-2xx → `RecordGrantCheck(ctx, "unavailable")`
   + `503 core_unavailable`, fails **closed**, never defaults to allowing the grant (TAC-FAIL-1).
   `!active` → `RecordGrantCheck(ctx, "not_active")` + `422 grantee_not_active_member` (collapses
   `iam-org-membership`'s previously-distinct 404/422 into one code — the new provider contract
   only distinguishes active/not-active).
3. `s.repo.Grant(ctx, entry)` — single-row `WithTenantTx`, `UNIQUE (tenant_id, tender_id, user_id)
   WHERE deleted_at IS NULL` enforces TAE-1 (at most one active grant); a violation surfaces as
   `409 duplicate_grant` via `IsUniqueViolation`.
4. On success, `cache.Delete(...)` for the `tac:acl:*` key — best-effort, a failure is
   `WarnContext`-logged, never fails the request (TAC-FAIL-2).

## Revoke flow (TAC-3)

`ACLService.Revoke(ctx, tenantID, tenderID, userID, expectedVersion)`:

- `s.repo.Revoke` soft-deletes (`deleted_at = now()`), gated on `UPDATE ... WHERE record_version =
  $expectedVersion` — a version mismatch (including one caused by the row already being revoked
  since it was last read by the caller) surfaces as `409 optimistic_lock_conflict`, never a silent
  no-op.
- Same post-commit `cache.Delete` best-effort invalidation as Grant.

## CheckAccess flow (TAC-4)

`ACLService.CheckAccess` — **never** returns not-found; no active grant is a valid, cacheable
`{has_access: false}` answer:

1. `cache.Get` — hit → `RecordCacheHit` + `RecordCheckCall(statusFor(hasAccess))`, return
   immediately. A cache **read error** (distinct from an ordinary miss) is logged at `WARN` and
   falls through to Postgres, counted in neither cache metric.
2. Miss → `RecordCacheMiss`, then `s.repo.FindActive(...)` — `SELECT ... WHERE deleted_at IS NULL
   AND (expires_at IS NULL OR expires_at > now())` (the TAE-3 predicate, `TenderACLEntry.IsActive`).
3. Build `domain.CachedAccess{HasAccess, AccessLevel, ExpiresAt}`, `RecordCheckCall`, then
   `cache.Set` (30s TTL) — best-effort, `WARN`-logged on failure, never fails the read.

## Tenant-offboarding cascade (`OffboardingConsumer`)

`TenantMembershipsPurged` event (Core's rename of its former `TenantOffboarded`, ADR-0008) → own
span `OffboardingConsumer.Handle`:

1. Idempotency check-before: `processedEvents.IsProcessed(ctx, eventID)` (consumer
   `tenant_lifecycle_cleanup`) — if already processed, ack and skip.
2. `repo.CascadeDeleteForTenant(ctx, tenantID)` — hard `DELETE FROM tender_acl_entries WHERE
   tenant_id=$1`. **Idempotent by construction**: a repeat delete on an already-cleared tenant is a
   safe no-op — a delayed or even indefinitely-failed cascade is never a live authorization risk
   (TAC-EVT-4), since TAC-4 answers `has_access:false` for a tenant with no rows regardless of
   whether the cascade ever ran.
3. `processedEvents.MarkProcessed(ctx, eventID)` mark-after.
4. `tender_acl_tenant_offboarding_cascade_total{result}` metric either way.
5. No explicit cache invalidation here — any cached `tac:acl:*` entries for the offboarded tenant
   simply expire within the 30s TTL.

## Member-removal cascade (`MemberRemovalConsumer`, ADR-0007 Wave 3 Phase 3)

`MembershipRevoked` event (Core's rename/consolidation of its former `TenantMembershipRemoved`,
ADR-0008) → own span `MemberRemovalConsumer.Handle`:

1. Same idempotency check-before/mark-after pattern, consumer `member_removal` — independent
   ledger entries from the offboarding consumer (composite PK `(event_id, consumer)`), so the two
   never collide on the same `event_id`.
2. Reads `env.Subject` as the **removed user's ID** (not the tenant, not the actor).
3. `repo.SoftDeleteForUser(ctx, tenantID, userID)` — soft-deletes (not hard-deletes) that user's
   entries within the tenant.
4. `tender_acl_member_removal_cascade_total{result}` metric.

## Optimistic concurrency

`record_version` (bigint, `CHECK > 0`, default `1`) is the sole concurrency-control mechanism —
bumped automatically by the `touch_row()` trigger on every UPDATE. Only `Revoke` gates on it
explicitly (`UPDATE ... WHERE record_version = $N`); `Grant` doesn't need to since it always
inserts a fresh row (guarded by the partial unique index, not a version check).

## Transaction discipline

Every tenant-scoped operation on `tender_acl_entries` goes through `pgcommon`'s tenant-scoped
transaction helper — there is **no** code path that hands out a bare connection/transaction,
including TAC-4's SELECT-only path. `withTenant`
(`internal/adapter/outbound/postgres/repository.go`) calls
`pgcommon.WithValidatedGUCSet(ctx, pgdomain.GUCSet{TenantID: tenantID.String()})`; the validation
error itself is **not** routed through `wrapConnErr` (it's a caller-input problem, not a
connectivity one). TAC-4 runs under the **target** tenant's GUC, not the caller's — internal routes
are not RLS-exempt.

`processed_events` operations go through `pool.WithConn(ctx, func(ctx, conn *pgxpool.Conn) error
{...})` instead — no GUC binding needed, since that table has no RLS.

## Graceful shutdown ordering

Coordinated via `errgroup` in `cmd/tender-acl/main.go`: HTTP server → metrics server → both SQS
consumers → both cleanup tickers, then finally the Postgres pool. The main pool's shutdown uses
`pgPool.DrainAndClose(drainCtx)` (a 30s-timeout context), **not** a bare `Close()` — lets
in-flight queries finish rather than dropping them mid-transaction. The raw pool (built via
`pgcommon.NewPool`, matching the main pool's `Logger`/`Tracer`/`PGBouncerMode`) follows the same
pattern.

## Failure domains (TAC-FAIL-1–3)

- **TAC-2's synchronous membership check fails closed** — an attacker cannot force a grant to
  succeed by causing `iam-org-membership` to become unreachable (TAC-FAIL-1).
- **TAC-1/TAC-3/TAC-4 have zero synchronous cross-service dependency** — only TAC-2 does
  (TAC-FAIL-3).
- **Own-DB/cache down**: `503 dependency_unavailable` on any route needing Postgres; Valkey down
  degrades TAC-4 to direct-Postgres reads, never an incorrect answer served past the 30s TTL
  (TAC-FAIL-2).
- **Tenant-offboarding cascade delay/failure is never a live authorization risk** (TAC-EVT-4) — see
  above.
