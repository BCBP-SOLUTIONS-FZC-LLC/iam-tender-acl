# Changelog

All notable changes to this project are documented in this file. Format based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

This service has no production deployment yet, so entries marked **BREAKING** carry no live
impact — they're noted for anyone integrating against it in dev.

## [Unreleased]

### Added

- Initial extraction of the Tender ACL Service from `iam-org-membership`, per ADR-0007 Wave 3 and
  `tender-acl-service-lld.md` v2.0.
- TAC-1/TAC-2/TAC-3 (`GET`/`POST`/`DELETE /api/v1/tenants/:id/tenders/:tender_id/acl[/:user_id]`) —
  admin-gated grant/revoke/list of tender-scoped ACL entries, replacing O&M's P-21/P-22/P-23.
- TAC-4 (`GET /internal/tenants/:id/tenders/:tender_id/acl/:user_id`) — mesh-only, mTLS
  authorization check, replacing O&M's I-12. Never returns `404`; a missing grant is a valid,
  cacheable `has_access:false` answer.
- `membershipcheck` — swappable synchronous outbound client (grant-time-only, TAC-2), replacing the
  composite foreign key that could not survive the database split. Fails closed
  (`503 dependency_unavailable`) on any error, never open.
- Valkey-backed cache for TAC-4 only, key `tac:acl:{tenant}:{tender}:{user}`, fixed 30s TTL,
  DELETE-based invalidation on grant/revoke.
- Two independent inbound SQS cascades, each idempotency-gated via a `processed_events` ledger
  (`uuid` `event_id`, composite `(event_id, consumer)` key, 8-day retention):
  - tenant-offboarding cascade-delete, replacing the lost `fk_tae_tenant ON DELETE CASCADE`.
  - per-user-removal cascade soft-delete (ADR-0007 Wave 3 Phase 3), replacing the per-user-removal
    ACL cascade gap.
- Row-Level Security on `tender_acl_entries`, carried over from `iam-org-membership`'s enforcement
  (without its `rls_check_tenant()` forensic-logging wrapper — TAC-D8, deliberate).
- `GET /asyncapi` / `GET /asyncapi.yaml` — server-rendered event-contract viewer ported from
  `iam-user-profile`, backed by a compile-time-embedded `api/asyncapi.yaml`.
- `iam-org-membership`: `GET /internal/tenants/:id/members/:user_id/exists` — the provider endpoint
  `membershipcheck` depends on.
- Full CI/CD, Helm chart, OpenAPI + AsyncAPI specs, and root documentation
  (`O_AND_M_DELTA.md`, `MIGRATION_RUNBOOK.md`, `IMPLEMENTATION_GAP_ANALYSIS.md`,
  `EVENT_COMPATIBILITY_REPORT.md`, `ARCHITECTURE.md`).
- ADR-0007 Wave 3 Phases 4–7 (read/write cutover, cleanup, table drop) executed against
  `iam-org-membership`: its P-21/P-22/P-23/I-12 code path fully removed, `iam-authz-enrichment`
  repointed to call TAC-4 directly, and `tender_acl_entries`/`tender_acl_level` dropped from O&M's
  schema. This service is now the sole system of record for tender ACL data. "Tender Service" and
  "admin tooling" repointing remain not executable in this environment — see
  `MIGRATION_RUNBOOK.md`/`IMPLEMENTATION_GAP_ANALYSIS.md` for what that means for completeness.

### Changed

- Restructured from an initial flat package layout to the Clean Architecture / Ports-and-Adapters
  layering every sibling IAM service uses (`internal/core/{domain,port,service}` +
  `internal/adapter/{inbound,outbound}/...`), on explicit request, so this repo isn't the odd one
  out. No behavior change.
- **TAC-3 Revoke now enforces the LLD's optimistic-lock check (BREAKING)**: `Revoke` gates its
  update on `record_version`, and `DELETE .../acl/:user_id` now requires a JSON body
  (`{"record_version": <int64>}`) instead of none. A stale version or an already-revoked row now
  returns `409 optimistic_lock_conflict` instead of an unconditional `204`.
- **Generic HTTP metrics now flow entirely through `platform-gincommon` (BREAKING metric names)**:
  the local `tender_acl_requests_total`/`tender_acl_request_duration_seconds` duplicate of
  gincommon's own `http_requests_total`/`http_request_duration_seconds` was removed in favor of a
  full passthrough. Dashboards, alert rules, the HPA custom metric, and the release-canary gate
  updated to the gincommon names; `internal/adapter/outbound/metrics` now owns only metrics
  gincommon has no equivalent for (writes, grant checks, cache hits/misses, both cascades).
- `platform-events.InitWithRegisterer` is now called at startup, activating consumer metrics
  (`events_consumed_total`, `sqs_receive_errors_total`, etc.) that were previously registered but
  inert. The tenant-offboarding queue's config now goes through `platform-events/pkg/config`
  (`LoadSQS`/`SQSConfigFromEnv`/`SQSConsumerOptions`) instead of a hand-rolled equivalent, closing a
  gap where its receive/wait/visibility settings weren't configurable at all. The
  member-removal queue stays hand-rolled (the library only supports one queue's worth of env vars).
  **BREAKING env var**: concurrency for the tenant-offboarding queue moves from
  `CONSUMER_CONCURRENCY` to `SQS_CONCURRENCY` (`.env`/`.env.example`/Helm values updated to
  preserve the prior effective concurrency of 4).
- Graceful shutdown now drains both Postgres pools via `pgcommon.Pool.DrainAndClose` (bounded 30s),
  matching what the LLD already documented instead of a bare `Close()`.
- Three platform-pgcommon gaps closed: the `processed_events` idempotency ledger now goes through
  its own `pgcommon.Pool` (slow-query logging + OTel tracing on writes that were previously
  invisible to both); `/readyz`'s Postgres check uses `pgcommon.Pool.Health(ctx)` (adds pool
  utilization stats to the response instead of a bare up/down bool); `withTenant` uses
  `pgcommon.WithValidatedGUCSet` instead of the unvalidated variant.
- Logs, metrics, and traces now flow through `platform-gincommon`/`platform-pgcommon` end to end:
  tracing is entirely gincommon's (removed the hand-rolled OTel setup and a duplicate `otelgin`
  tracing middleware that produced two sibling spans per request); `/healthz` serves
  `gincommon.HealthHandler()` directly; `pgcommon.ConfigFromEnv()` adopted for the runtime pool
  (`PG_MAX_CONNS`/`PG_MIN_CONNS`/`PG_SLOW_QUERY_THRESHOLD` now genuinely configurable,
  `PGBouncerMode` forced `true` unconditionally per LLD §17.5 Case 5); a dead
  `events.WithMaxReceiveCount(5)` option (no-op without a registered dead-letter handler) removed.
  **BREAKING**: the OTLP collector endpoint is gRPC now, not HTTP — `OTEL_EXPORTER_OTLP_ENDPOINT`
  changes from `http://host:4318` to a bare `host:4317`.
- Postgres migrations consolidated from five incremental files into one (`0001_tender_acl_schema`)
  expressing the schema's final shape — this service has never been deployed, so there's no prior
  release to preserve incremental migration history for.

### Fixed

- `membershipcheck` client was unreachable: wrong URL path (missing iam-org-membership's `/api/v1`
  prefix) and wrong header names (`x-tenant-id`/`x-tenant-roles`, not `X-User-Id`/`X-User-Roles`) —
  every TAC-2 grant-time membership check was failing.
- Two stale documentation artifacts caught during an LLD-drift sweep: a migration comment claiming
  `record_version` wasn't enforced on Revoke (it is), and `api/asyncapi.yaml`'s
  `TenantOffboardedPayload` schema using a nested shape that predated confirming
  `platform-events`' real `Envelope[T]` wire format (flat, top-level fields) — consumer code was
  already reading the correct shape; only the spec's documentation of it was wrong.
- Both cascade consumers were dead: `MemberRemovalConsumer` and `OffboardingConsumer` dispatched on
  event type names (`TenantMembershipRemoved`, `TenantOffboarded`) that iam-org-membership had
  since renamed/consolidated under ADR-0008; every delivery was silently skip-and-acked. Fixed the
  type checks and updated `api/asyncapi.yaml` to match. The matching SNS→SQS subscription filter
  policy is infrastructure config outside this repo and still needs updating before either fix
  takes effect live.
- `dependency_unavailable`/503 was dead code on TAC-1/3/4 (**BREAKING**): a real Postgres/Valkey
  connectivity failure fell through to a generic `500` instead of the documented `503`. Added
  `wrapConnErr` in the repository layer to classify connectivity failures consistently across every
  repository method.

### Known gaps

See `IMPLEMENTATION_GAP_ANALYSIS.md` for full detail.

- This service consumes two event types where the LLD originally specified one
  (`EVENT_COMPATIBILITY_REPORT.md` has the reconciled invariant).
- `TenantMembershipRemoved` hasn't been run through `iam-org-membership`'s
  `platform-schemagov` governance pipeline, and that repo's own `api/asyncapi.yaml` doesn't
  document it yet.
- Nothing here has been deployed to, or soaked against, a real staging/production environment —
  verified only against testcontainers-backed test suites.
