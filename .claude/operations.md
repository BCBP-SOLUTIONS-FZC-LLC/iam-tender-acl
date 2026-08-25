# Operations

## Security

- **Trust boundary**: gateway-injected `x-user-id`/`x-tenant-id`/`x-tenant-roles` headers only — no
  JWT parsing in this service (mesh/mTLS + upstream gateway is the trust boundary).
- **Tenant isolation**: RLS (fail-closed on missing/empty GUC, see `database.md`) is the *primary*
  defense; the handler-layer `requireSameTenant` check (gateway tenant vs. `:id` path param) is
  defense-in-depth — both must independently fail for a cross-tenant leak.
- **TAC-4 is mesh-only**: no public ingress, no JWT/role check at all — the entire trust boundary
  there is network-layer (mTLS + NetworkPolicy), same posture as every other IAM internal route on
  this platform. Tenant isolation on TAC-4 comes from RLS alone, running under the *target*
  tenant's GUC.
- **Grant-time bypass resistance**: any membershipcheck error fails closed, never open (see
  `flows-and-concurrency.md`).
- **`reason` field**: free-text, not validated for PII content, treated as opaque — same posture as
  similar fields elsewhere on this platform. Removed only via tenant-offboarding cascade or
  explicit revoke, no dedicated GDPR delete path (a departed user's row goes inert once their
  membership lapses, not deleted, unless their whole tenant offboards — TAC-D4).
- See `.github/SECURITY.md` for the vulnerability-reporting process and this service's specific
  threat-model scope.

## Observability

### Metrics (Prometheus)

**Generic per-request HTTP metrics are `platform-gincommon`'s own** —
`http_requests_total{method,route,status_class,error_class}`,
`http_request_duration_seconds{method,route,status_class,error_class}` — passed through as-is via
`ObservabilityMiddlewares`' `MetricsMiddleware`, **not** duplicated under a `tender_acl_*` name.
This service was never deployed before this passthrough landed, so it's the only metric naming
this service has ever shipped with in production (no migration/dual-write concern).

**Business-level metrics** keep the `tender_acl_*` prefix (gincommon has no equivalent):

| Metric | Labels |
|---|---|
| `tender_acl_writes_total` | `op` (`grant`/`revoke`), `result` (`success`/`error`) |
| `tender_acl_grant_checks_total` | `status` (`active`/`not_active`/`unavailable`) |
| `tender_acl_check_calls_total` | `status` (`has_access`/`no_access`) |
| `tender_acl_cache_hits_total` / `tender_acl_cache_misses_total` | `key` (fixed `tac:acl`) |
| `tender_acl_tenant_offboarding_cascade_total` | `result` |
| `tender_acl_member_removal_cascade_total` | `result` |

**Both SQS consumers' metrics are `platform-events`'s own** — `events_consumed_total{queue,
event_type,status}`, `events_consume_duration_seconds{queue,event_type}`,
`sqs_receive_errors_total{queue}`, `sqs_delete_errors_total{queue}`,
`sqs_visibility_extension_errors_total{queue}` (the latter two are flagged by the library's own
docs as *causing* duplicate delivery when non-zero — exactly the failure mode `processed_events`
exists to survive). Activated by one `events.InitWithRegisterer("tender-acl", buildVersion,
gincommon.MetricsRegisterer())` call in `main.go`.

### Tracing

OTel Go SDK, W3C Trace Context. Span shape: `otelgin (inbound.http) → service.ACLService.<method>
→ outbound.postgres` (+ `outbound.membershipcheck` only on Grant, + `tac:*` cache read/DEL spans on
CheckAccess/Grant/Revoke). Consumers get their own top-level spans
(`OffboardingConsumer.Handle`/`MemberRemovalConsumer.Handle`).

### Logging

A single Zap-backed logger, built once via `platform-gincommon/pkg/logger.NewLogger(cfg.Environment)`
(`cmd/tender-acl/main.go`) — matching `iam-user-profile`'s and `iam-org-membership`'s identical
convention, not a local slog JSON handler. Every log line in this process flows through it:
`gincommon.Config.Logger` (HTTP request logs), `pgcommon.Config.Logger` (slow-query/migration logs,
via `postgres.LoggerAdapter`), `platform-events`' SQS warnings, and the core service/consumer layers
(via `internal/core/port.SlogStyleLogger`, a thin wrapper preserving the existing `*slog.Logger`-style
call syntax — `Info(msg, "key", val, ...)`, `InfoContext(ctx, msg, ...)` — on top of the shared
sink). `*Context` calls carry `trace_id` automatically when a span is present; both consumers'
`Handle` methods additionally bind `tenant_id`/`event_id` (and `user_id` for member-removal) once
per call via `SlogStyleLogger.With`. Cache/DB best-effort failures (cache invalidation, cache
populate, cache read) are logged at `WARN`, never escalated to a caller-visible error.

### Health

- `/healthz` — `gincommon.HealthHandler()`, liveness only.
- `/readyz` — calls `pgcommon.Pool.Health(ctx)`; response includes `checks.postgres` (ok/error) and
  `checks.postgres_pool` (`total_conns`/`idle_conns`/`acquired_conns`/`max_conns`/`utilization`).
  Cache-degraded (Valkey down) is reported but does **not** take the pod out of service — writes
  proceed normally, TAC-4 just falls back to direct Postgres reads.

### Dashboards / alerts

"Tender ACL" Grafana folder: Requests & Writes; Grant-Time Membership Check; Tenant-Offboarding
Cleanup (LLD §14.4). Alerts (LLD §14.5): `/readyz` failing >5min → SEV-2; TAC-4 error rate >10%/5min
→ SEV-2; membershipcheck unreachable during TAC-2 sustained >15min → SEV-3; DLQ depth > 0 → SEV-3.

## Configuration (env vars)

Three have **no safe default** and must be set or the process fails fast at startup:
`DATABASE_URL`, `SQS_QUEUE_URL`, `MEMBER_REMOVAL_SQS_QUEUE_URL`.

| Variable | Purpose | Default |
|---|---|---|
| `HTTP_PORT` | main HTTP listener | `8086` |
| `METRICS_PORT` | metrics server | `9090` |
| `DATABASE_URL` | `tender_acl_app` (RLS-bound) connection string | *(required)* |
| `MIGRATION_DATABASE_URL` | `tender_acl_migrator` (BYPASSRLS) startup-migration connection string | falls back to `DATABASE_URL` |
| `DATABASE_MIGRATION_URL` | separate var read by the standalone `migrate-up`/`down`/`create` Make targets | same default value as above, distinct var |
| `PG_MAX_CONNS` / `PG_MIN_CONNS` / `PG_SLOW_QUERY_THRESHOLD` | pool sizing (`pgcommon.ConfigFromEnv`) | `10` / `2` / `200ms` |
| `PG_STATEMENT_TIMEOUT` | server-side `statement_timeout` appended to the DSN (`postgres.ApplyStatementTimeout`) so a hung query releases its pool connection instead of holding it for the full request lifetime — a Go duration (e.g. `5s`); only applied when the DSN is assembled from `PG_*` vars, not when `DATABASE_URL` is set directly | unset (no timeout) |
| `VALKEY_ADDR` / `VALKEY_PASSWORD` | TAC-4's 30s cache | `localhost:6379` / — |
| `CORE_INTERNAL_BASE_URL` | `iam-org-membership`'s internal base URL, consulted only at TAC-2 grant time | `http://org-membership.iam.svc.cluster.local` |
| `MEMBERSHIP_CHECK_TIMEOUT_MS` | client-side timeout for the membership-existence call | `300` |
| `SQS_QUEUE_URL` / `SQS_DLQ_URL` | `tenant-lifecycle-tenderacl-q` (+ DLQ) — loaded via `platform-events/pkg/config.LoadSQS`, not this service's own `config.go` | *(required)* |
| `SQS_CONCURRENCY` | queue #1's concurrency — `platform-events/pkg/config`'s own var | `1` (library default; set to `4` explicitly to preserve prior effective concurrency) |
| `MEMBER_REMOVAL_SQS_QUEUE_URL` / `..._DLQ_URL` | `member-removal-tenderacl-q` (+ DLQ), ADR-0007 Wave 3 Phase 3 | *(required)* |
| `CONSUMER_CONCURRENCY` | queue #2's concurrency only — hand-rolled, not `platform-events/pkg/config` | `4` |
| `AWS_REGION` / `AWS_ENDPOINT_URL` / `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | LocalStack in dev; production uses IRSA and drops static creds | `us-east-1` / LocalStack URL / dummy |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | gRPC OTLP endpoint (bare `host:port`, no scheme) — via `platform-gincommon`'s `ObservabilityMiddlewares` | `localhost:4317` |
| `ENVIRONMENT` | selects `platform-gincommon`'s Zap dev-console vs. prod-JSON encoder (`logger.NewLogger`, "dev"/"development"/"local" vs. everything else) and env/trace badges elsewhere | `development` |
| `PROCESSED_EVENTS_CLEANUP_INTERVAL` | cleanup ticker cadence for the 8-day retention sweep | — |
| `DOCS_ENABLED` | opt `/swagger` and `/asyncapi`/`/asyncapi.yaml` into production | `false` |
| `DOCS_AUTH_TOKEN` | if set, requires `Authorization: Bearer <token>` on those routes in production | — |

## CI/CD

`.github/workflows/ci.yml` fans out three parallel jobs on push/PR (paths-ignore skips pure-doc
commits):

- **`validate-test.yml`** — race + coverage gate; gates `trivy`/`smoke`.
- **`validate-quality.yml`** — `tidy`/`fmt-check`/`vet`/`lint`; gates `push`/`release` only.
- **`build-image`** — Hadolint + `.dockerignore` check *before* the (expensive) multi-stage
  distroless Docker build, then Trivy scan + Cosign signing on merge to `main`.

Other workflows: `changelog-check.yml` (CHANGELOG discipline gate), `release.yml`.

`make ci` = `tidy fmt-check vet lint test-ci build` — the local equivalent of the quality+test gates.

## Docker

`make docker-build` → distroless, digest-pinned, multi-stage image running the HTTP server *and*
both SQS consumers in a **single process** (`errgroup`) — no second binary, no sidecar. The final
stage copies only the compiled binary, never the source tree — this is exactly why `api/asyncapi.yaml`
had to move to `//go:embed` (`api/embed.go`) rather than being read from disk at request time.
