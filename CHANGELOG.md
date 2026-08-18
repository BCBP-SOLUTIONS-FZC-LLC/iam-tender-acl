# Changelog

All notable changes to this project are documented in this file. Format based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Changed — `processed_events.event_id` is now `uuid`, matching `iam-group-mapping`'s convention

Compared this service's SQS-consumer idempotency ledger against `iam-group-mapping`'s (the only
other sibling with two independent event-driven cascades to compare against): the check-then-mark
algorithm, composite `(event_id, consumer)` primary key, `ON CONFLICT DO NOTHING` mark step,
mark-after-success ordering, and periodic retention sweep were already identical — both services
converged on the same pattern independently. The one real difference: `iam-group-mapping`'s
`processed_events.event_id` column (`migrations/0006_processed_events.up.sql`) is typed `uuid`, a
documented, deliberate improvement over the `text` shape `iam-org-membership`'s original convention
used, since event ids are always genuine UUIDs (validated via `uuid.Parse` before ever reaching the
store). This service's `internal/adapter/outbound/postgres/migrations/0004_processed_events.up.sql`
now does the same. No application-code change needed — both `OffboardingConsumer` and
`MemberRemovalConsumer` already validate and pass a `uuid.UUID`, which binds to the `uuid` column
natively. Verified: full `unit`/`integration`/`rls`/`e2e` suites green against real Postgres after
the schema change.

### Changed — TAC-3 Revoke now enforces the LLD's optimistic-lock check (BREAKING)

- **`Revoke()` (`internal/adapter/outbound/postgres/repository.go`) now gates its `UPDATE` on
  `record_version = $4`**, matching `tender-acl-service-lld.md` §11.2/§12.1, which this repo had
  previously deferred (see `IMPLEMENTATION_GAP_ANALYSIS.md` Discrepancy 1) to preserve
  `iam-org-membership`'s prior unconditional-update behavior unchanged. Zero rows affected (stale
  version, already revoked, or no such row) now returns `409 optimistic_lock_conflict` —
  `ErrCodeOptimisticLockConflict` is no longer dead code.
- **Breaking**: TAC-3's request contract changes from no body to a required JSON body —
  `DELETE .../acl/:user_id` now requires `{"record_version": <int64>}`. A call with no body, or a
  missing/zero `record_version`, returns `400 invalid_request` instead of the previous unconditional
  `204`. A repeat revoke of an already-revoked row is no longer an idempotent no-op `204` — it now
  `409`s on the version mismatch. Any existing caller (admin tooling) must be updated to read and
  send `record_version` (present on every `ACLResponse`, e.g. from TAC-1's `List`) before this ships
  to it.
- `ARCHITECTURE.md`'s request-flow diagram (and its `docs/architecture/mermaid/request-flow.mmd`
  source), `IMPLEMENTATION_GAP_ANALYSIS.md`'s Discrepancy 1, and `internal/core/domain/errors.go`'s
  doc comment updated to describe this as current behavior rather than a deferred gap.
- Verified: `TestHandler_Revoke_VersionConflict_Returns409` (unit),
  `TestE2E_RevokeStaleVersion_Returns409` (e2e, real Postgres), swagger regenerated (`make swag`).

### Changed — Logs, metrics, and traces now flow through the shared libraries; `pgcommon.ConfigFromEnv()` adopted

Follow-up to the shared-library reuse audit below — the two items it flagged as "left as-is"
(`ConfigFromEnv()` adoption, and this service's hand-rolled OTel setup) are now done.

- **`cmd/tender-acl/telemetry.go` deleted.** Tracing is entirely `platform-gincommon`'s now: a
  `*gincommon.TracingOptions` (endpoint/insecure/sample-ratio, derived from `cfg.Environment` —
  this service uses `ENVIRONMENT`, not `APP_ENV`, so gincommon's own env-based defaults are
  explicitly overridden rather than silently mismatched) is threaded into
  `internal/adapter/inbound/http.NewRouter`, which passes it to
  `gincommon.ObservabilityMiddlewares` — the same call that lazily installs the real OTel
  `TracerProvider` on first use. `router.go`'s previously-discarded `_ trace.Tracer` parameter is
  now that `*gincommon.TracingOptions` value — same parameter slot, no new params needed.
  `otel.Tracer("tender-acl")` (a delegating handle, per the OTel SDK's documented global-provider
  swap behavior — safe to obtain before or after gincommon installs the real provider) replaces
  the locally-built `tracerProvider.Tracer(...)` everywhere a `trace.Tracer` is threaded into
  `ACLService`/the two SQS consumers. Shutdown is `gincommon.Shutdown(nil)` (tracing-only flush —
  `nil` is explicitly documented as valid; this service's slog logger has no `Sync() error` to
  flush anyway). **Breaking for the OTLP collector endpoint**: it's gRPC now (gincommon's own
  exporter), not HTTP — `OTEL_EXPORTER_OTLP_ENDPOINT` changes from a `http://host:4318` URL to a
  bare `host:4317`; updated in `.env`/`.env.example`/`deploy/helm/tender-acl/values.yaml`.
- **`internal/adapter/outbound/metrics/metrics.go` rewritten from OTel meter instruments to
  `prometheus/client_golang` `CounterVec`/`HistogramVec`**, registered directly against the
  default registry — the same registry both gincommon's own HTTP metrics and this process's
  `/metrics` endpoint (`promhttp.Handler()`) already share, matching every sibling IAM service's
  convention instead of a parallel OTel-metrics-plus-Prometheus-bridge pipeline. All six
  `tender_acl_*` metric names/labels are byte-identical to before (verified live against
  `/metrics`); `NewMetrics()` dropped its `metric.Meter` parameter (now called with no arguments).
  `test/integration/service_test.go`'s `testMetrics(t)` helper — called from three separate test
  functions — now returns a `TestMain`-constructed shared instance instead of calling
  `NewMetrics()` per-test: `prometheus.Register` (unlike OTel instrument creation) errors on a
  second registration of the same metric name against the process-wide default registry.
- **Found and fixed a latent bug surfaced by this rewrite**: `deploy/monitoring/slo-rules.yml`'s
  SLO-1 recording rule queried `tender_acl_request_duration_seconds_bucket{...,le="0.020"}` — but
  Prometheus's `le` bucket label is the exact string `client_golang`/OTel expose for a boundary's
  float64 value (Go's minimal-digits formatting always produces `"0.02"`, never `"0.020"`), and
  PromQL label matching is verbatim-string, not numeric. The query never matched any series —
  silently "no data," not an error — since the rule was written. Caught by checking the actual
  `/metrics` output live while verifying the new histogram's buckets; fixed to `le="0.02"` (both
  occurrences). This bug predates this change (the old OTel-Prometheus-bridge histogram would have
  exposed the identical `"0.02"` string) — not something the metrics rewrite introduced.
- **`pgcommon.ConfigFromEnv()` adopted** for the runtime Postgres pool (`cmd/tender-acl/main.go`),
  replacing a hand-built `pgcommon.Config{DSN, PGBouncerMode, GUCProvider}` literal. Warnings are
  logged unconditionally (`postgres config warning`, verified live: local dev's `sslmode=disable`
  DSN correctly produces one). `GUCProvider` and `PGBouncerMode` are still set explicitly
  afterward — `PGBouncerMode` is **forced `true` unconditionally, not env-driven** via
  `PG_BOUNCER_MODE`: transaction-scoped RLS GUCs are required for correctness regardless of
  deployment topology (LLD §17.5 Case 5), so this one field is deliberately not left to
  configuration to get right (`PG_BOUNCER_MODE` is not added to any env file, to avoid implying
  it's a tunable). `PG_MAX_CONNS`/`PG_MIN_CONNS`/`PG_SLOW_QUERY_THRESHOLD` are now genuinely
  configurable (previously not read at all) — added to `.env`/`.env.example`/`values.yaml` with
  `pgcommon`'s own defaults. Two new adapters in `cmd/tender-acl/observability.go`:
  `slogDomainLogger` (pgcommon's `domain.Logger`, `Field`-based — a different shape from
  gincommon's own map-based `Logger`, hence a separate small type) wired as `Config.Logger` so
  slow-query `WARN` lines flow through the same structured logger as everything else (previously
  unset — slow queries logged nowhere); `otelSpanTracer` (structural `StartSpan` adapter over
  `trace.Tracer`, no import of pgcommon's internal port package needed) wired as `Config.Tracer` so
  every Postgres query gets its own child `db.query` OTel span under the request's trace
  (previously unset — no such spans existed at all).
- Verified: `go build`/`go vet ./...` (all four build-tag combinations), `make lint`/`go-arch-lint`
  (0 issues), full `unit`/`integration`/`rls`/`e2e` suites (including
  `TestRLS_NoGUCLeakageAcrossPooledConnection`, confirming the forced `PGBouncerMode=true`
  preserved the correctness invariant), and a live run: startup logs show the `postgres config
  warning`, `http_request` log lines flow through gincommon's logging middleware with
  `trace_id`/`request_id` populated, `/metrics` shows all `tender_acl_*` series under their
  original names with the `le="0.02"` bucket present, and the OTLP/gRPC exporter attempts
  `127.0.0.1:4317` (connection-refused, expected — no local collector) instead of the old
  HTTP/4318 endpoint.

### Changed — shared-library reuse audit (`platform-events`/`platform-gincommon`/`platform-pgcommon`)

- **Removed duplicate tracing instrumentation.** `internal/adapter/inbound/http/router.go` ran
  both `otelgin.Middleware("tender-acl")` and `gincommon.ObservabilityMiddlewares` (which itself
  runs gincommon's own `TracingMiddleware`) — both create an OTel server span from the same
  incoming trace context on every request, so every request produced two sibling SERVER spans
  in the trace backend instead of one. Removed the `otelgin.Middleware` line (and its now-unused
  `go.opentelemetry.io/contrib/instrumentation/.../otelgin` import/dependency) since gincommon's
  own tracing is a strict superset.
- **`/healthz` now serves `gincommon.HealthHandler()` directly**, replacing a local
  reimplementation that reproduced its exact `{"status":"ok"}` body by hand (mirrors the same fix
  already applied to `iam-catalog-admin`). Its swagger `@Summary`/`@Router /healthz` annotations
  were dropped along with the handler function they were attached to — same precedent as
  `iam-catalog-admin`'s identical change; `make swag` regenerated `docs/swagger/` accordingly.
- **`events.WithMaxReceiveCount(5)` was a no-op on both SQS consumers, removed.** `platform-events`
  only consults `maxReceiveCount` when a `WithDeadLetterHandler` is also registered (verified
  against the library's internal source) — this service never registered one, since
  `deploy/iam/README.md` already documents that redrive-after-5-attempts is handled entirely by
  each queue's own SQS redrive policy (the pod has no SQS permissions on either DLQ). The option
  looked like it was doing something; it wasn't. Removed with an explanatory comment at both call
  sites (`cmd/tender-acl/main.go`) rather than adding an unnecessary in-process dead-letter handler.
- No config/error-classification duplication found: `pgcommon.IsUniqueViolation` is already used
  correctly for TAC-2's duplicate-grant detection, and the gincommon `RequestContext`
  bridge (`ContextBridgeMiddleware`) is a necessary adapter (gincommon's own type is unexported),
  not avoidable duplication. `pgcommon.ConfigFromEnv()` adoption (for free `PG_MAX_CONNS`/
  `PG_MIN_CONNS`/etc. env-var tuning and startup validation warnings) was considered but left
  as-is — nothing currently documents these as tunable, so it's an optional enhancement, not a fix.
  Verified: `go build`/`go vet ./...` (all four build-tag combinations), `make lint`/`go-arch-lint`
  (0 issues), full `unit`/`integration`/`rls`/`e2e` suites, and a live run confirming `/healthz`/
  `/readyz` still respond correctly.

### Changed

- **Restructured this service's package layout from the original flat, near-literal-lift structure
  (TAC-D1) to the same Clean Architecture / Ports-and-Adapters layering every sibling IAM service
  uses** (`iam-org-membership`, `iam-catalog-admin`, `iam-group-mapping`) — on explicit request, to
  bring this repo in line with the rest of the platform. `internal/acl` split into
  `internal/core/{domain,port,service}` (domain types, port interfaces, `ACLService`);
  `internal/membershipcheck`'s `Checker` interface moved to `internal/core/port` as
  `MembershipCheckClient`, its `HTTPChecker` implementation to
  `internal/adapter/outbound/membershipcheck`; `internal/cache` split into `port.Cache` +
  `internal/adapter/outbound/valkey`'s `Cache`; `internal/consumer` moved to
  `internal/adapter/inbound/consumer`; the HTTP handler/router/DTOs moved to
  `internal/adapter/inbound/http`; `internal/acl/postgres` moved to
  `internal/adapter/outbound/postgres` (including its `migrations/` directory). `.go-arch-lint.yml`
  rewritten to enforce the new dependency rule (core has no adapter dependencies; adapters depend
  on core, never on each other) — passes with zero violations. No behavior change: every test
  (unit, `integration`, `e2e`, `rls` build tags) passes unchanged against the same real Postgres/
  Valkey containers. `ARCHITECTURE.md`, `README.md`, `IMPLEMENTATION_GAP_ANALYSIS.md`, the CI
  workflow comments, and the PR/issue templates updated to describe the new layout; TAC-D1 in the
  decision register is marked superseded rather than deleted, so the history of why it was
  originally flat isn't lost.

### Added

- ADR-0007 Wave 3 Phases 6/7 (cleanup + table drop, both fully executed): `iam-org-membership`'s
  P-21/P-22/P-23/I-12 handlers/service/repository/domain code have been deleted entirely (not just
  disabled), and `tender_acl_entries`/`tender_acl_level` have been dropped from O&M's schema via a
  new migration (`000015_drop_tender_acl_table`), applied against both a fresh testcontainer and
  the persistent local dev database, preceded by a `pg_dump` pre-drop snapshot per the runbook's
  own requirement. This service is now the sole system of record for tender ACL data. A real bug
  found during this pass and fixed: `ProvisioningService.DeleteMember` (I-5) had its own,
  independent `tender_acl_entries` cascade call site that Phase 3's emission-parity work had
  missed — see `IMPLEMENTATION_GAP_ANALYSIS.md` Discrepancy 10.
- ADR-0007 Wave 3 Phase 5 (write cutover, partial): `iam-org-membership`'s P-21/P-22/P-23/I-12
  routes now return `410 Gone` (wire code `endpoint_retired`) instead of serving real reads/writes
  — verified end-to-end with 6 new e2e tests there. Two of this phase's three action items are
  **not executable in this environment**: "admin tooling" repointing (no such codebase, same
  finding as Phase 4) and the tenant-offboarding SNS-routing repoint (the stated producer,
  `iam-realm-provisioner`, doesn't implement `TenantOffboarded` emission anywhere in that repo, and
  the actual subscription wiring is platform-Terraform-owned infrastructure outside this
  workspace) — see `MIGRATION_RUNBOOK.md`/`IMPLEMENTATION_GAP_ANALYSIS.md` Discrepancy 9.
- ADR-0007 Wave 3 Phase 4 (read cutover, partial): `iam-authz-enrichment`'s I-12 caller
  (`GetTenderACL`) now calls this service's TAC-4 directly instead of `iam-org-membership` —
  confirmed as the only real, repointable caller in this environment ("Tender Service" doesn't
  exist yet per ADR-0007 Option D; "admin tooling" isn't a real codebase either — see
  `MIGRATION_RUNBOOK.md`/`IMPLEMENTATION_GAP_ANALYSIS.md` Discrepancy 8 for what this means for
  Phase 4's completeness).
- Initial extraction of the Tender ACL Service from `iam-org-membership`, per ADR-0007 Wave 3 and
  `tender-acl-service-lld.md` v2.0. Phase 1 of `MIGRATION_RUNBOOK.md` — greenfield deployment, no
  production traffic routed yet.
- TAC-1/TAC-2/TAC-3 (`GET`/`POST`/`DELETE /api/v1/tenants/:id/tenders/:tender_id/acl[/:user_id]`) —
  admin-gated grant/revoke/list of tender-scoped ACL entries, replacing O&M's P-21/P-22/P-23.
- TAC-4 (`GET /internal/tenants/:id/tenders/:tender_id/acl/:user_id`) — mesh-only, mTLS
  authorization check, replacing O&M's I-12. Never returns `404`; a missing grant is a valid,
  cacheable `has_access:false` answer.
- `internal/membershipcheck` — swappable synchronous outbound client replacing the composite
  foreign key (`tenant_id`, `tenant_membership_id`, `user_id`) that could not survive the database
  split. Fails closed (`503 core_unavailable`) on any error; never fails open.
- `internal/cache` — Valkey-backed cache for TAC-4 only, key `tac:acl:{tenant}:{tender}:{user}`,
  fixed 30s TTL, DELETE-based invalidation on grant/revoke.
- `internal/consumer` — tenant-offboarding cascade-delete consumer on `tenant-lifecycle-tenderacl-q`
  (with `tenant-lifecycle-tenderacl-q-dlq`, `maxReceiveCount=5`), replacing the lost
  `fk_tae_tenant ON DELETE CASCADE`. Own `processed_events` idempotency ledger, 8-day retention.
- Row-Level Security on `tender_acl_entries` (new — this table had RLS as part of
  `iam-org-membership`'s database and retains it here unchanged in enforcement, though without the
  source's `rls_check_tenant()`/`rls_violation_log` forensic-logging wrapper — TAC-D8).
- Flat package layout (`internal/{acl,membershipcheck,cache,consumer}`) per
  `tender-acl-service-lld.md` §6, decision TAC-D1 — a deliberate divergence from the Clean
  Architecture layering used by `iam-group-mapping`/`iam-catalog-admin`, since this service is
  explicitly interim (slated for Wave-4 reabsorption into the Tender Service, LLD §22).
- Full CI/CD (`.github/workflows/`), Helm chart (`deploy/helm/tender-acl/`), OpenAPI + AsyncAPI
  specs (`api/`), and root documentation (`O_AND_M_DELTA.md`, `MIGRATION_RUNBOOK.md`,
  `IMPLEMENTATION_GAP_ANALYSIS.md`, `EVENT_COMPATIBILITY_REPORT.md`, `ARCHITECTURE.md`).
- `scripts/migrate-data-from-org-membership.sh` (`MIGRATION_RUNBOOK.md` Phase 2) — one-time
  export/load/verify tool for the `tender_acl_entries` data expand step; dry-run verified against
  synthetic data (no real rows existed in this environment's `iam-org-membership` yet).
- `iam-org-membership`: `GET /internal/tenants/:id/members/:user_id/exists`
  (`InternalHandler.CheckMemberExists` + `MembershipService.CheckActiveMembership`) — the provider
  endpoint this service's `membershipcheck.HTTPChecker` depends on (`MIGRATION_RUNBOOK.md` Phase 3,
  `O_AND_M_DELTA.md` §4).
- A second, independent inbound SQS subscription — `member-removal-tenderacl-q` (+ its own DLQ,
  `MemberRemovalConsumer`, `Repository.SoftDeleteForUser`, and `tender_acl_member_removal_cascade_total`
  metric) — consuming a new `TenantMembershipRemoved` event `iam-org-membership`'s `RemoveUser` now
  emits, replacing the per-user-removal ACL cascade gap (`MIGRATION_RUNBOOK.md` Phase 3,
  `O_AND_M_DELTA.md` §5 "Option B", chosen by explicit user decision over this project's own
  Option-A recommendation).

### Known gaps (see `IMPLEMENTATION_GAP_ANALYSIS.md` for full detail)

- **`TAC-EVT-2` LLD deviation**: this service now consumes **two** event types, not the one the LLD
  specifies — see `EVENT_COMPATIBILITY_REPORT.md`'s reconciled invariant.
- The new `TenantMembershipRemoved` event has not been run through `iam-org-membership`'s
  `platform-schemagov` governance pipeline, and that repo's own `api/asyncapi.yaml` does not yet
  document it (`IMPLEMENTATION_GAP_ANALYSIS.md` Discrepancy 7).
- Nothing in Phases 2–3 has been deployed to, or soaked against, any real staging/production
  environment — verified only against testcontainers-backed test suites in both repositories.
