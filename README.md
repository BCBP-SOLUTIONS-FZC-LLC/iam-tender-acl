# iam-tender-acl

Interim service for the restricted-tender access overlay (`tender_acl_entries`), extracted from
`iam-org-membership` per ADR-0007 Wave 3. **Deliberately minimal and disposable** — scheduled to
merge into the Tender Service once it matures (Option D / Wave 4, `docs/lld/iam-lld-tender-acl-service.md` §25);
do not add long-term abstractions here.

## Mental model

This service owns exactly one table, `tender_acl_entries`, and exposes four endpoints: three
admin-gated CRUD-ish operations (grant/revoke/list a tender-scoped access override) and one
mesh-only authorization check that a departed-from-Core caller (Tender Service, AuthZ Enrichment)
consults synchronously. It has one synchronous outbound dependency of its own (a grant-time
membership-existence check against `iam-org-membership`) and **two** inbound async subscriptions: a
tenant-offboarding cascade-delete, and (ADR-0007 Wave 3 Phase 3) a per-user-removal cascade
soft-delete. It publishes zero events.

If you're familiar with `iam-group-mapping` or `iam-catalog-admin`, the package layout will look
immediately familiar: this service uses the same Clean Architecture / Ports-and-Adapters layering
they do (`internal/core/{domain,port,service}` + `internal/adapter/{inbound,outbound}`). It
originally shipped with a flat, deliberately unlayered structure (`docs/lld/iam-lld-tender-acl-service.md` §6,
decision TAC-D1) — see [ARCHITECTURE.md](ARCHITECTURE.md) for that history and why it was reversed.

## Why this service exists

Part of ADR-0007's decomposition of `iam-org-membership`, executed in three waves ordered by risk
and urgency:

1. Wave 1 — `iam-catalog-admin` (departments/plans catalog)
2. Wave 2 — `iam-group-mapping` (JIT group→role/department mapping)
3. **Wave 3 — this service** (tender ACL overlay) — explicitly framed by ADR-0007 as "do last,
   lowest urgency": a single admin-gated table with low write volume, and the one wave that trades
   away a real DB-enforced integrity guarantee (a composite foreign key to tenant membership) for a
   synchronous grant-time-only check, in exchange for being independently deployable.

Unlike its two siblings, this service **is** on a live authorization-decision path (TAC-4, the I-12
successor) — its cache and latency posture get hot-path rigor despite the table's otherwise low
write volume.

## API overview

| ID | Method & path | Auth | Notes |
|---|---|---|---|
| TAC-1 | `GET /api/v1/tenants/:id/tenders/:tender_id/acl` | `tender_admin`/`tenant_admin`/`tenant_owner` (via `x-tenant-roles`) | List active + inactive ACL entries for a tender. Not cached. |
| TAC-2 | `POST /api/v1/tenants/:id/tenders/:tender_id/acl` | same | Grant access. Blocks on a synchronous membership-existence check against `iam-org-membership` — fails **closed** (`503 core_unavailable`) if that check can't be performed. |
| TAC-3 | `DELETE /api/v1/tenants/:id/tenders/:tender_id/acl/:user_id` | same | Revoke (soft-delete). Requires `{"record_version": <int64>}` in the body; a mismatch returns `409 optimistic_lock_conflict`. Returns `204`. |
| TAC-4 | `GET /internal/tenants/:id/tenders/:tender_id/acl/:user_id` | mesh-only (mTLS), no role/JWT check | Never `404` — a missing grant is a valid, cacheable `has_access:false` answer. Cached 30s in Valkey. |

Full request/response schemas: Swagger UI at `/swagger` (generated from handler annotations via
`make swag`, mirrors `iam-org-membership`'s identical setup). Event contract (inbound only):
[`api/asyncapi.yaml`](api/asyncapi.yaml), also browsable as a rendered HTML catalog at `/asyncapi`
(ported from `iam-user-profile`'s identical viewer) — see Local development below.

## Input validation

Domain-rule failures are raised as a `*domain.Error` wrapping one of the 12 sentinel codes in
`internal/core/domain/errors.go`; `respondACLError` (`internal/adapter/inbound/http/handler.go`)
maps the code to a frozen HTTP status (LLD §20). A raw `*pgconn.PgError` that escapes the repository
layer untranslated is classified by SQLSTATE class — `08`/`53` via
`pgcommon.IsConnectionException`/`IsInsufficientResources`, `57`/`58` via a small local fallback —
into `503 dependency_unavailable`; anything else falls back to `500 internal_server_error`.

```json
{
  "error": "optimistic_lock_conflict",
  "status": 409,
  "trace_id": "...",
  "request_id": "..."
}
```

### Notable validation rules

| Rule | Enforcement |
|---|---|
| `tenant_id` in the path is never trusted for authorization on its own | `requireSameTenant` checks it against the gateway-injected header AND that the caller holds `tender_admin`/`tenant_admin`/`tenant_owner`; RLS is a second, independent backstop |
| `access_level` must be `view`/`edit`/`approve` | `400 invalid_access_level` on anything else (TAC-2) |
| `reason` is capped at 500 characters | Enforced at the database layer (a `CHECK` constraint, not just API-layer validation) — an LLD addition beyond the source schema, which left it unbounded |
| `expires_at` must be in the future | `400 invalid_expiry` if not (TAC-2) |
| Optimistic locking on revoke | `record_version` is required in the request body; a mismatch — including against an already-revoked row — returns `409 optimistic_lock_conflict` with no way to distinguish the two causes (matches the LLD's own sequence diagram) |
| Grant-time membership existence | TAC-2 fails **closed** (`503 core_unavailable`) on any error/timeout from the synchronous check against `iam-org-membership` — never defaults to allowing the grant |
| TAC-1 pagination | `?limit=`/`?offset=` — non-integer or out-of-range values (`limit < 1`, `offset < 0`) are `400 invalid_request`; `limit` above 500 is silently clamped, not rejected |

## Architecture

Clean Architecture — dependencies point inward; outer layers never import inner layers. Full layer
diagrams, request/event flows, and cache/RLS design are in **[`ARCHITECTURE.md`](ARCHITECTURE.md)**;
the package tree itself is in **[`.claude/CLAUDE.md`](.claude/CLAUDE.md)**.

### Dependency rules (enforced by `go-arch-lint` in CI)

| Component | May depend on |
|---|---|
| `domain` | Nothing internal |
| `port` | `domain` only |
| `service` | `domain`, `port` |
| `adapters_inbound` (http/consumer) | `service`, `port`, `domain`, `observability` |
| `adapters_outbound` (postgres/valkey/membershipcheck) | `port`, `domain` |
| `cmd` | Everything above |

### Storage and messaging

| Concern | Technology | Notes |
|---|---|---|
| **Primary store** | PostgreSQL, database `tender_acl` on RDS | One table (`tender_acl_entries`), `FORCE ROW LEVEL SECURITY`; GUC `app.tenant_id` bound transaction-locally |
| **Cache** | Valkey (Redis-compatible) | TAC-4 only, advisory-only — a miss or outage falls through to Postgres, never a hard failure |
| **Events (outbound)** | None | This service publishes zero events (TAC-EVT-1) — no outbox, no SNS producer |
| **Events (inbound)** | AWS SQS, 2 queues | `tenant-lifecycle-tenderacl-q`, `member-removal-tenderacl-q`, each with its own `-dlq` |
| **Schema registry** | None | Consume-only — no Glue registration of this service's own; see LLD §10.5 |

### Shared library dependencies

| Library | Version | Purpose |
|---|---|---|
| `platform-gincommon` | v1.3.0 | HTTP middleware, Zap logging, OTel tracing, Prometheus metrics |
| `platform-events` | v1.4.0 | SQS consumer construction (no outbox/SNS publisher — unused here) |
| `platform-pgcommon` | v1.3.0 | pgx/v5 pool, RLS GUC injection, migrations, error helpers |

## Integrating with other services

### 1. Prerequisites

Every caller must run on the internal service mesh (mTLS + NetworkPolicy) for `/internal/*` (TAC-4),
or forward gateway-validated `x-user-id`/`x-tenant-id`/`x-tenant-roles` headers for `/api/v1/*`
(TAC-1..3). There is no JWT parsing in this service.

### 2. `iam-org-membership` — bidirectional

| Direction | Call | Notes |
|---|---|---|
| this → `iam-org-membership` | `GET /api/v1/internal/tenants/:id/members/:user_id/exists` | TAC-2 grant-time only, fail-closed, 300ms client timeout |
| `iam-org-membership` → events → this | `TenantMembershipsPurged` on `tenant-lifecycle-tenderacl-q`, `MembershipRevoked` on `member-removal-tenderacl-q` | Two independent queues/DLQs so each cascade's failure mode stays observable |

### 3. Tender Service — the admin surface and the approval-workflow caller

TAC-1/2/3 (`/api/v1/...`) are this service's admin-facing grant/revoke/list surface, normally
fronted by a tender-admin UI/BFF. TAC-4 is consulted by the Tender Service's own approval workflow
to decide whether a specific user may act on a specific restricted tender.

### 4. AuthZ Enrichment — the other TAC-4 caller

`GET /internal/tenants/:id/tenders/:tender_id/acl/:user_id` — same contract, same cache, same
never-404 posture as the Tender Service's own use of TAC-4.

### 5. Subscribing to the two inbound events

This service consumes, never produces (TAC-EVT-1). `api/asyncapi.yaml` documents both messages by
hand — see "Testing domain events locally" below for how to exercise them against a local queue.

**Idempotency:** both consumers record the envelope `id` against their own `processed_events`
scope (`tenant_lifecycle_cleanup`/`member_removal`) before committing any side effect — delivery is
at-least-once. **Ordering:** neither cascade depends on delivery order — both are idempotent,
terminal operations (hard-delete / soft-delete) on rows that either exist or don't.

### 6. Handling errors

Every non-2xx response is the flat envelope shown above. See
`docs/lld/iam-lld-tender-acl-service.md` §20 for the complete error taxonomy.

### 7. Rate limits

No generic per-caller/per-endpoint rate limiter exists in this codebase — inbound throttling, if
any, is the gateway's concern. There is no invitation-style business-logic throttle here either;
TAC-2/TAC-3 are simple single-row admin operations, not a bulk resource with its own abuse surface.

## Local development

```sh
make setup          # install tools, git hooks
make docker-up       # postgres + valkey + localstack (SQS-compatible), see docker-compose.yml
make run             # runs the binary against docker-up's dependencies
```

Docs, once running (port from `HTTP_PORT`, see `.env.example`):

| Tool | URL | Notes |
|---|---|---|
| Swagger UI | `http://localhost:8086/swagger/index.html` | REST contract (TAC-1/2/3/4). Regenerate after changing a handler's `// @…` annotations with `make swag`; `make swag-check` is the CI drift gate. |
| AsyncAPI viewer | `http://localhost:8086/asyncapi` | Event contract browser for `api/asyncapi.yaml` — server-rendered HTML, no CDN dependencies. This service publishes zero events (TAC-EVT-1), so both `TenantMembershipsPurged` and `MembershipRevoked` render under "Consumed Messages" with a `RECEIVE` badge; there is no "Published Messages" section. `GET /asyncapi.yaml` serves the spec itself, embedded into the binary at compile time (`api/embed.go`) rather than read from disk, so it works the same way in the built container image as it does locally. |

Both are dev-only by default; see `DOCS_ENABLED`/`DOCS_AUTH_TOKEN` below to opt either into production.

Environment variables — see `.env.example` for the full list; the three with no safe default
(`DATABASE_URL`, `SQS_QUEUE_URL`, `MEMBER_REMOVAL_SQS_QUEUE_URL`) must be set or the process fails
fast at startup. Notable ones:

| Variable | Purpose | Default |
|---|---|---|
| `DATABASE_URL` | `tender_acl_app` (RLS-bound) connection string | *(required)* |
| `MIGRATION_DATABASE_URL` | `tender_acl_migrator` (BYPASSRLS) connection string for the startup migration run | falls back to `DATABASE_URL` |
| `CORE_INTERNAL_BASE_URL` | `iam-org-membership`'s internal base URL, consulted only at TAC-2 grant time | `http://org-membership.iam.svc.cluster.local` |
| `MEMBERSHIP_CHECK_TIMEOUT_MS` | Client-side timeout for the membership-existence call (LLD §15) | `300` |
| `VALKEY_ADDR` | TAC-4's 30s cache | `localhost:6379` |
| `SQS_QUEUE_URL` | `tenant-lifecycle-tenderacl-q` (tenant-offboarding cascade). Loaded via `platform-events/pkg/config`'s `LoadSQS`, not this service's own `config.go` — see `SQS_CONCURRENCY` below | *(required)* |
| `SQS_CONCURRENCY` | `tenant-lifecycle-tenderacl-q`'s consumer concurrency — `platform-events/pkg/config`'s own env var. `SQS_MAX_MESSAGES`/`SQS_WAIT_SECONDS`/`SQS_VISIBILITY_TIMEOUT`/`SQS_MAX_RECEIVE_COUNT` are also available via the same helper; none are currently set, so library defaults (10/20s/30s/unset) apply. Queue #2 clones this env and inherits those tunables. | `1` (library default — set explicitly to `4` in `.env`/`.env.example`/`values.yaml` to preserve this queue's prior effective concurrency) |
| `MEMBER_REMOVAL_SQS_QUEUE_URL` | `member-removal-tenderacl-q` (per-user-removal cascade, ADR-0007 Wave 3 Phase 3) | *(required)* |
| `MEMBER_REMOVAL_SQS_CONCURRENCY` | `member-removal-tenderacl-q`'s consumer concurrency, applied via `SQSConsumerOptions` after cloning `LoadSQS`; falls back to `CONSUMER_CONCURRENCY` | `4` |
| `CONSUMER_CONCURRENCY` | fallback for queue #2 if `MEMBER_REMOVAL_SQS_CONCURRENCY` is unset | `4` |
| `DOCS_ENABLED` | Opt-in to serving `/swagger` and `/asyncapi`/`/asyncapi.yaml` in production | `false` |
| `DOCS_AUTH_TOKEN` | If set, requires `Authorization: Bearer <token>` on `/swagger` and `/asyncapi`/`/asyncapi.yaml` in production | — |

## Testing consumed events locally

This service never publishes — there is no outbox to inspect the way a producer service would.
"Testing events" here means driving the two inbound cascades against a local SQS-compatible queue.

### Step 1 — Start infrastructure

```bash
make docker-up
```

`scripts/init-localstack.sh` runs automatically and provisions both queues plus their DLQs
(`tenant-lifecycle-tenderacl-q`, `member-removal-tenderacl-q`, `maxReceiveCount=5` each).

### Step 2 — Verify the queues exist

```bash
docker compose exec localstack awslocal sqs list-queues --region us-east-1
```

### Step 3 — Send a test event and inspect the result

```bash
make run
# in another shell:
docker compose exec localstack awslocal sqs send-message \
  --queue-url http://localhost:4566/000000000000/tenant-lifecycle-tenderacl-q \
  --message-body '{"id":"<uuid>","type":"TenantMembershipsPurged","tenant_id":"<uuid>","time":"2026-01-01T00:00:00Z"}'
# or, for the per-user cascade:
docker compose exec localstack awslocal sqs send-message \
  --queue-url http://localhost:4566/000000000000/member-removal-tenderacl-q \
  --message-body '{"id":"<uuid>","type":"MembershipRevoked","tenant_id":"<uuid>","subject":"<user_id>","time":"2026-01-01T00:00:00Z"}'

docker compose exec postgres psql -U tender_acl_app -d tender_acl -c \
  "SELECT event_id, consumer, processed_at FROM processed_events ORDER BY processed_at DESC LIMIT 20;"
```

### Troubleshooting events

| Symptom | Likely cause | Fix |
|---|---|---|
| Message never disappears from the queue | Consumer isn't running, or the event failed and is awaiting redelivery | Check `make run`'s logs for `sqs: receive message failed`/cascade errors |
| `tender_acl_entries` rows for the tenant/user are untouched | Wrong `tenant_id`/`subject`, or the event `type` doesn't match what that queue expects (acked, not processed — check for a WARN log and `tender_acl_unexpected_event_type_total`) | Re-check the message body against the schema in `api/asyncapi.yaml` |
| A redelivered message re-runs the cascade | It shouldn't — both cascades are idempotent by construction (a repeat delete/soft-delete against already-gone rows is a no-op) | If this happens, it's a bug — file an issue |

## Testing

```sh
make test              # unit tests only
make test-integration   # + testcontainers Postgres/Valkey/SQS-compatible container
make test-rls           # canonical RLS suite (missing-GUC, cross-tenant read/write, pooled-connection no-leak)
make test-e2e           # full HTTP-stack request flows
make race               # unit tests with -race
make test-ci            # everything above, merged coverage report
```

## Security

- Trusts gateway-injected `x-user-id`/`x-tenant-id`/`x-tenant-roles` headers exclusively — no JWT
  parsing in this service (mesh/mTLS + upstream gateway is the trust boundary).
- Row-Level Security (RLS) enforces tenant isolation at the database layer as a backstop, not the
  primary defense — see `ARCHITECTURE.md`'s RLS/GUC section.
- TAC-4 is mesh-only (no external ingress, no role check) — tenant isolation there comes from RLS
  alone, running under the *target* tenant's GUC.
- See `.github/SECURITY.md` for the vulnerability-reporting process and this service's specific
  threat-model scope.

## Observability

**SLOs.** TAC-4 (the hot path): cache hit **< 5 ms**, cache miss **< 25 ms** — a single indexed
Postgres lookup, no join, no cross-service call.

### Metrics

All `tender_acl_`-prefixed (`internal/adapter/outbound/metrics/metrics.go`): `writes_total{op,result}`,
`grant_checks_total{status}`, `check_calls_total{status}`, `cache_hits_total{key}` /
`cache_misses_total{key}`, `tenant_offboarding_cascade_total{result}`,
`member_removal_cascade_total{result}`, `unexpected_event_type_total{queue,event_type}`,
`processed_events_duplicates_total{consumer}`. Passthrough `http_*` (`platform-gincommon`) and
`events_*`/`sqs_*` (`platform-events`) instruments round out the surface — generic HTTP metrics are
not duplicated under a `tender_acl_*` name.

### Tracing and logs

OTel Go SDK bootstrapped via `gincommon.InitTracing`, plus a `db.query` span per query via
`pgcommon.Config.Tracer`. Structured Zap logs never carry a credential or secret — only identifiers
(`tenant_id`, `tender_id`, `user_id`, `event_id`, `trace_id`).

## Deployment

One `Deployment`, no `CronJob`s — a single binary (`cmd/tender-acl`) runs the HTTP server and both
SQS consumers in-process. HPA 2–10 replicas, PDB `minAvailable: 1`. Self-migrates at startup (one
consolidated migration, `0001_tender_acl_schema` — this service has never been deployed, so there's
no incremental history to preserve). Full detail in [`ARCHITECTURE.md`](ARCHITECTURE.md)'s
Deployment section.

## CI

`.github/workflows/ci.yml` runs `validate-test` + `validate-quality` in parallel, builds and scans
the container image (Hadolint, Trivy), and signs it (Cosign) on merge to `main`. See
`.github/workflows/` for the full set.

## Docker

`make docker-build` builds a distroless, digest-pinned, multi-stage image running both the HTTP
server and the SQS consumer in a single process (via `errgroup`) — no second binary.

## Cross-service dependencies

Reads (TAC-1, TAC-4) have **no** synchronous cross-service dependency — Postgres + Valkey only.

| Operation | Sync dependency | Posture | On failure |
|---|---|---|---|
| Grant (TAC-2) | `iam-org-membership` (membership-existence check) | fail-closed | `503 core_unavailable`, no row written |
| List (TAC-1) | — | — | Never calls out |
| Revoke (TAC-3) | — | — | Never calls out |
| Authorization check (TAC-4) | — | — | Never calls out — this is deliberate (TAC-D3): the one read every caller depends on has zero cross-service risk |

## Out of scope

| Concern | Where it lives |
|---|---|
| Tender existence, ownership, restricted/unrestricted classification | Tender Service |
| Tenant, department, membership, tenant roles | `iam-org-membership` |
| Department/tenant-role group mappings | Group Mapping / JIT Config Service |
| Plan / feature flags / seat licensing / department catalog | Catalog / Admin Config Service |
| Audit records | Audit Log Service |
| Realm / Keycloak Admin API mutations | Realm Provisioner |

## What this service deliberately does not have

- **No outbound events.** Publishes nothing, ever — no outbox table, no SNS/EventBridge producer.
- **No long-term service-specific abstractions.** Every abstraction here exists only to let this
  service run standalone (e.g. the swappable `membershipcheck` interface, built specifically so
  Wave 4 can repoint or delete it cheaply) — not to anticipate future features this interim service
  will never grow.
- **No audit-log table.** Structured request logging is the only durable write record, matching the
  rest of this platform's current posture (no shared audit mechanism exists anywhere yet).
- **No GDPR per-user delete path.** A departed user's ACL row goes inert (unreachable via TAC-4
  once their membership lapses — see `ARCHITECTURE.md`'s TAC-D4 argument), not deleted, unless
  their whole tenant offboards.

## Contributing

This repo has no separate `CONTRIBUTING.md` — the PR checklist lives in
[`.github/pull_request_template.md`](.github/pull_request_template.md), and the docs below cover
development setup, extending the service, and testing requirements.

| Document | Description |
|---|---|
| [`.claude/CLAUDE.md`](.claude/CLAUDE.md) | Top-level guidance for Claude Code working in this repo |
| [`.claude/api-and-events.md`](.claude/api-and-events.md) | API contract, cache design, event contract, AsyncAPI viewer |
| [`.claude/database.md`](.claude/database.md) | Schema, RLS, migrations, roles |
| [`.claude/flows-and-concurrency.md`](.claude/flows-and-concurrency.md) | Request/event flows, optimistic locking, shutdown |
| [`.claude/operations.md`](.claude/operations.md) | Observability, config, CI/CD, security |
| [`.claude/development-guide.md`](.claude/development-guide.md) | Design decisions, troubleshooting, error codes |
| [`ARCHITECTURE.md`](ARCHITECTURE.md) | Detailed architecture narrative with diagrams |
| [`docs/lld/iam-lld-tender-acl-service.md`](docs/lld/iam-lld-tender-acl-service.md) | Full LLD — §19 open-question register, §20 error taxonomy, §26 decision register |
| [`VERSIONING.md`](VERSIONING.md) | SemVer policy, release process, runtime-contract freeze list |

## See also

- [`ARCHITECTURE.md`](ARCHITECTURE.md) — package layout, request/event flows, cache/RLS design, key invariants.
- [`docs/lld/iam-lld-tender-acl-service.md`](docs/lld/iam-lld-tender-acl-service.md) §24 — phased cutover plan and execution status.

## Ownership

IAM team. See `.github/CODEOWNERS`.
