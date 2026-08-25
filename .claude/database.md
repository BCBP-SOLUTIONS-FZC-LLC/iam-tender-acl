# Database

- **Name**: `tender_acl` (local dev port 5536, offset from Postgres's standard 5432 so this
  service's `docker-compose.yml` stack can run side-by-side with sibling repos' own stacks).
- **Tables**: exactly two — `tender_acl_entries` (the one domain table, RLS-enforced) and
  `processed_events` (the shared idempotency ledger, not tenant-scoped, no RLS).
- **Pool**: `platform-pgcommon`'s `pgcommon.Pool` — two separate pools in `main.go`, both built via
  `pgcommon.NewPool`, both with `PGBouncerMode: true` forced unconditionally (`PG_BOUNCER_MODE` env
  var is deliberately *not* read — transaction-scoped RLS GUCs are required regardless of topology):
  - **App pool** (`DATABASE_URL`, role `tender_acl_app`, no `BYPASSRLS`) — every
    `tender_acl_entries` read/write, gated through `GUCProvider: pgcommon.GUCSetFromContext` /
    `pgcommon.WithValidatedGUCSet`.
  - **Raw pool** (`MIGRATION_DATABASE_URL` fallback to `DATABASE_URL` at startup; separately, a
    `processed_events`-only pool built on the admin DSN in tests) — no `GUCProvider`, since
    `processed_events` has no RLS.
- **Migrations**: run by the binary itself at startup (`RunMigrations` → `pgmigrate.Runner{FS, DSN,
  Logger}.Up(ctx)`), no separate migrate Job/service to wait on. `make migrate-up`/`migrate-down`/
  `migrate-create` (standalone golang-migrate CLI, `DATABASE_MIGRATION_URL`) are for manual/CI use
  outside the running binary.

## Core tables

### `tender_acl_entries`

Relocated from `iam-org-membership` unchanged in shape, minus two FKs that couldn't survive the
database split — see "What was lost" below.

| Column | Type | Notes |
|---|---|---|
| `id` | `uuid` PK | `gen_random_uuid()` |
| `tenant_id` | `uuid` | RLS-scoped |
| `tender_id` | `uuid` | no FK — cross-service, Tender Service owns tenders |
| `user_id` | `uuid` | |
| `tenant_membership_id` | `uuid` | **audit-only** — no longer DB-validated against a live row (see below) |
| `access_level` | `tender_acl_level` enum | `view` / `edit` / `approve` |
| `granted_by` | `uuid` | |
| `reason` | `text` | `CHECK (reason IS NULL OR char_length(reason) <= 500)` — a DB-layer cap the LLD explicitly calls for, beyond the source schema's unbounded column |
| `expires_at` | `timestamptz` | nullable |
| `record_version` | `bigint` | `CHECK (record_version > 0)`, default `1` — optimistic-lock token, bumped by `touch_row()` on every UPDATE |
| `created_at` / `updated_at` | `timestamptz` | `updated_at` bumped by `touch_row()` |
| `deleted_at` | `timestamptz` | nullable — presence = soft-deleted/revoked |

Indexes: `uq_tae_active_entry` UNIQUE `(tenant_id, tender_id, user_id) WHERE deleted_at IS NULL`
(TAE-1: at most one active grant per tenant/tender/user), plus `idx_tae_tenant_tender`,
`idx_tae_user` (both partial on `deleted_at IS NULL`), `idx_tae_membership`.

Trigger `trg_touch_tae` (name per LLD §7.5 verbatim — differs from `iam-org-membership`'s
`trg_touch_tender_acl_entries`; the LLD name wins) — `BEFORE UPDATE ... FOR EACH ROW WHEN (OLD.* IS
DISTINCT FROM NEW.*)`, calls `touch_row()` (shared with `iam-group-mapping`'s identical function).

**What was lost in the split** (both replaced, not just dropped):
- `fk_tae_tenant ... ON DELETE CASCADE REFERENCES tenants(id)` → replaced by the
  `TenantMembershipsPurged` consumer's cascade-delete (`OffboardingConsumer`, TAC-D7, mirrors
  Wave-2's GM-D2 — async subscription, not a second synchronous check).
- `fk_tae_tenant_membership ... REFERENCES tenant_memberships(id, tenant_id, user_id)` → replaced
  by a synchronous grant-time-only membership-existence check
  (`internal/adapter/outbound/membershipcheck`, TAC-D2) — `tenant_membership_id` remains for audit
  purposes only.

### `processed_events`

Idempotency ledger backing **both** cascade consumers. Composite PK `(event_id, consumer)` — not
`event_id` alone — so `OffboardingConsumer` (`consumer = "tenant_lifecycle_cleanup"`) and
`MemberRemovalConsumer` (`consumer = "member_removal"`) dedup independently instead of colliding on
the same `event_id` from an unrelated relay. `event_id` is `uuid` (not `text`, unlike
`iam-org-membership`'s original convention) — this service's event IDs are always validated via
`uuid.Parse` before reaching this store, so the stronger column type is strictly correct.

No `event_type`/`expires_at` column — retention is a batched `DELETE ... WHERE processed_at <
now() - interval '8 days'` sweep (`ProcessedEvents.CleanupExpired`, backed by
`idx_processed_events_prune`), not a precomputed per-row expiry.

## Row-Level Security (RLS)

- `ENABLE ROW LEVEL SECURITY` + `FORCE ROW LEVEL SECURITY` + `REVOKE ALL ... FROM PUBLIC` on
  `tender_acl_entries`.
- Single policy `tenant_isolation FOR ALL USING/WITH CHECK (tenant_id =
  NULLIF(current_setting('app.tenant_id', true), '')::uuid)`.
- `NULLIF(...,'')` hardens against PgBouncer transaction-pooling GUC leakage:
  `current_setting(..., true)` can return `''` (not `NULL`) for a connection that referenced
  `app.tenant_id` before without ever binding it, and a bare `''::uuid` cast raises rather than
  excluding rows. Normalizing to `NULL` first makes a missing/never-bound GUC compare as `tenant_id
  = NULL` — **fail closed** (zero rows on read, every write rejected), never an error, never a
  cross-tenant leak. Proven necessary by `iam-group-mapping`'s
  `TestRLS_NoGUCLeakageAcrossPooledConnection`, mirrored by this service's own `test/rls` suite
  (`make test-rls`).
- GUC is bound via `pgcommon.WithValidatedGUCSet` inside every tenant-scoped transaction, `SELECT
  set_config('app.tenant_id', $1, true)` with `is_local = true` — auto-unset at COMMIT/ROLLBACK, so
  a pooled connection can never leak one tenant's GUC into the next tenant's transaction.
- TAC-D8: unlike `iam-org-membership`'s original table, this policy is **deliberately plain** — no
  `rls_check_tenant()` SECURITY DEFINER wrapper, no `rls_violation_log` forensic logging (mirrors
  `iam-group-mapping`'s GM-D8). RLS enforcement itself is unaffected, only the audit-on-violation
  behavior was dropped.
- `processed_events` has **no RLS** — it's not tenant-facing through any API.

## PostgreSQL roles

| Role | Purpose | `BYPASSRLS` |
|---|---|---|
| `tender_acl_app` | Runtime application role (`DATABASE_URL`) | **Never** — every statement it issues against `tender_acl_entries` is subject to `FORCE ROW LEVEL SECURITY`, no exceptions |
| `tender_acl_migrator` | Applies schema migrations; backs any cross-tenant admin access path (LLD §7.4) | Yes — `ALTER ROLE tender_acl_migrator BYPASSRLS` |

Dev-only passwords are checked into the migration, matching `docker-compose.yml`'s credentials —
production rotates these out of band, never by editing the migration.

## Migrations

A single migration, `0001_tender_acl_schema` (+ paired `.down.sql`), under
`internal/adapter/outbound/postgres/migrations/` — this service has never been deployed, so there's
no prior release to preserve incremental migration history for; the schema is expressed as its
final shape rather than as `enums`/`touch_row_function`/`tender_acl_entries`/`processed_events`/
`app_role` steps. It creates, in order: the `tender_acl_level` enum (local copy, cross-DB enum
drift with other services unenforced — TAC-Q6, deliberately deferred), the shared `touch_row()`
trigger function (identical to `iam-group-mapping`'s), `tender_acl_entries` + its indexes/trigger/
RLS policy, `processed_events` + its prune index, and the `tender_acl_app`/`tender_acl_migrator`
roles + grants.

## DSN resolution and slow-query / pool tuning

`internal/adapter/outbound/postgres/db.go`'s `DSNFromEnv`/`ApplyStatementTimeout`/
`MigrationDSNFromEnv` are the single DSN-assembly path for both pools and the migration runner —
mirroring `iam-user-profile`'s and `iam-org-membership`'s identical helpers, so DSN assembly has
exactly one implementation instead of a second one hand-rolled in `cmd/tender-acl`. `PG_STATEMENT_TIMEOUT`
(a Go duration, e.g. `5s`) is appended as a server-side `statement_timeout` — but only when the DSN
is assembled from `PG_*` vars, not when `DATABASE_URL` is set directly (its query string is passed
through verbatim, matching the same rule both siblings apply).

`PG_MAX_CONNS=10`, `PG_MIN_CONNS=2`, `PG_SLOW_QUERY_THRESHOLD=200ms` — read via
`pgcommon.ConfigFromEnv()`, safe to omit (library defaults apply). `/readyz` surfaces pool
utilization (`total_conns`/`idle_conns`/`acquired_conns`/`max_conns`/`utilization`) via
`pgcommon.Pool.Health(ctx)`.
