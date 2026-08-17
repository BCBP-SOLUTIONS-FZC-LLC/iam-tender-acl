# iam-tender-acl

Interim service for the restricted-tender access overlay (`tender_acl_entries`), extracted from
`iam-org-membership` per ADR-0007 Wave 3. **Deliberately minimal and disposable** — scheduled to
merge into the Tender Service once it matures (Option D / Wave 4, `tender-acl-service-lld.md` §22);
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
originally shipped with a flat, deliberately unlayered structure (`tender-acl-service-lld.md` §6,
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
| TAC-3 | `DELETE /api/v1/tenants/:id/tenders/:tender_id/acl/:user_id` | same | Revoke (soft-delete). No optimistic-lock check — see `IMPLEMENTATION_GAP_ANALYSIS.md`. Returns `204`. |
| TAC-4 | `GET /internal/tenants/:id/tenders/:tender_id/acl/:user_id` | mesh-only (mTLS), no role/JWT check | Never `404` — a missing grant is a valid, cacheable `has_access:false` answer. Cached 30s in Valkey. |

Full request/response schemas: Swagger UI at `/swagger` (generated from handler annotations via
`make swag`, mirrors `iam-org-membership`'s identical setup). Event contract (inbound only):
[`api/asyncapi.yaml`](api/asyncapi.yaml).

## Local development

```sh
make setup          # install tools, git hooks
make docker-up       # postgres + valkey + localstack (SQS-compatible), see docker-compose.yml
make run             # runs the binary against docker-up's dependencies
```

Docs, once running: `http://localhost:8086/swagger/index.html` (port from `HTTP_PORT`, see
`.env.example`). Regenerate after changing a handler's `// @…` annotations with `make swag`;
`make swag-check` is the CI drift gate.

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
| `SQS_QUEUE_URL` | `tenant-lifecycle-tenderacl-q` (tenant-offboarding cascade) | *(required)* |
| `MEMBER_REMOVAL_SQS_QUEUE_URL` | `member-removal-tenderacl-q` (per-user-removal cascade, ADR-0007 Wave 3 Phase 3) | *(required)* |
| `DOCS_ENABLED` | Opt-in to serving `/swagger` in production | `false` |
| `DOCS_AUTH_TOKEN` | If set, requires `Authorization: Bearer <token>` on `/swagger` in production | — |

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

## CI

`.github/workflows/ci.yml` runs `validate-test` + `validate-quality` in parallel, builds and scans
the container image (Hadolint, Trivy), and signs it (Cosign) on merge to `main`. See
`.github/workflows/` for the full set.

## Docker

`make docker-build` builds a distroless, digest-pinned, multi-stage image running both the HTTP
server and the SQS consumer in a single process (via `errgroup`) — no second binary.

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

## See also

- [`ARCHITECTURE.md`](ARCHITECTURE.md) — package layout, request/event flows, cache/RLS design, key invariants.
- [`O_AND_M_DELTA.md`](O_AND_M_DELTA.md) — what must change in `iam-org-membership` (not yet executed).
- [`MIGRATION_RUNBOOK.md`](MIGRATION_RUNBOOK.md) — phased cutover plan.
- [`IMPLEMENTATION_GAP_ANALYSIS.md`](IMPLEMENTATION_GAP_ANALYSIS.md) — LLD-vs-code discrepancies found during this build.
- [`EVENT_COMPATIBILITY_REPORT.md`](EVENT_COMPATIBILITY_REPORT.md) — TAC-EVT-1..5 verification.

## Ownership

IAM team. See `.github/CODEOWNERS`.
