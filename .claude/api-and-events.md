# API and events

## API contract

**Identity model**: gateway-injected `x-user-id`/`x-tenant-id`/`x-tenant-roles` headers only — no
JWT parsing in this service (mesh/mTLS + upstream gateway is the trust boundary). TAC-1/2/3 are
role-gated (`tender_admin`/`tenant_admin`/`tenant_owner` via `x-tenant-roles`, checked plus
`requireSameTenant` comparing the gateway-injected tenant against the `:id` path param — defense in
depth alongside RLS). TAC-4 has **no** role/JWT check at all — mesh-only.

| ID | Method & path | Auth | Notes |
|---|---|---|---|
| TAC-1 | `GET /api/v1/tenants/:id/tenders/:tender_id/acl` | role-gated | List active + inactive entries. Not cached — low-frequency, admin-gated read. |
| TAC-2 | `POST /api/v1/tenants/:id/tenders/:tender_id/acl` | role-gated | Grant. Blocks on `port.MembershipCheckClient.Exists` — fails **closed** (`503 core_unavailable`) if that check can't be performed, never fails open. |
| TAC-3 | `DELETE /api/v1/tenants/:id/tenders/:tender_id/acl/:user_id` | role-gated | Revoke (soft-delete). Body must include `{"record_version": <int64>}`; mismatch → `409 optimistic_lock_conflict`. Returns `204`. |
| TAC-4 | `GET /internal/tenants/:id/tenders/:tender_id/acl/:user_id` | mesh-only, no `/api/v1` prefix | **Never** `404` — no active grant is a valid, cacheable `has_access:false` answer. Cached 30s in Valkey. |

Full request/response schemas: Swagger UI at `/swagger/index.html`, generated from handler `//
@…` annotations via `make swag` (`make swag-check` is CI's drift gate).

### Error envelope

`domain.Error{Code, Message}` (`internal/core/domain/errors.go`) — flat, no wrapped-sentinel
shape (unlike `iam-org-membership`'s `DomainError`+`Cause`; this taxonomy is flat enough that a
bare code+message has always sufficed). `respondACLError`
(`internal/adapter/inbound/http/handler.go`) is the **single** place mapping codes to HTTP status.
See `development-guide.md`'s Appendix for the full code→status table.

### Grant validation (TAC-2, `ACLService.Grant`)

In order: `level.Valid()` (`view`/`edit`/`approve`) → `invalid_access_level`; `len(reason) > 500` →
`invalid_reason`; `expiresAt != nil && !expiresAt.After(now)` → `invalid_expiry`; then the
membership check (see Flows doc).

## Caching (TAC-4 only)

One cache region: `tac:acl:{tenant}:{tender}:{user}` → `{has_access, access_level, expires_at}`,
fixed **30s TTL**, no jitter — hardcoded, not independently configurable via env var. `port.Cache`
(`Get`/`Set`/`Delete`/`Ping`). TAC-1 never touches the cache. Grant/Revoke `DEL` the key
**after** commit (never write-through with UPDATE) so a mid-write process death can never serve a
stale `has_access:true`. A failed `DEL` or cache read is logged at `WARN` and swallowed — never
surfaced to the caller; Valkey down degrades TAC-4 to direct-Postgres reads (higher latency, not an
outage).

## Events

**Publishes zero events** — no outbox table, no SNS/EventBridge producer (TAC-D5: no new event
introduced for grant/revoke, preserving the source's "no bus event" posture). This is a permanent
design choice for this service's lifetime, not a gap.

**Consumes two**, both via `platform-events` SQS consumers, both idempotency-gated through
`ProcessedEvents` (composite key `(event_id, consumer)`, see `database.md`):

| Event | Queue | Consumer | Action |
|---|---|---|---|
| `TenantOffboarded` | `tenant-lifecycle-tenderacl-q` (+ DLQ) | `OffboardingConsumer` (`consumer="tenant_lifecycle_cleanup"`) | `CascadeDeleteForTenant` — hard `DELETE FROM tender_acl_entries WHERE tenant_id=$1` |
| `TenantMembershipRemoved` | `member-removal-tenderacl-q` (+ DLQ) | `MemberRemovalConsumer` (`consumer="member_removal"`) | `SoftDeleteForUser` — reads `env.Subject` as the removed user ID |

Queue #1 is built via `platform-events/pkg/config.SQSConfigFromEnv(sqsEnv, ...)` +
`SQSConsumerOptions(sqsEnv)...`, where `sqsEnv := eventsconfig.LoadSQS()` (validated, warnings
logged via `LogWarningsTo`) — this is the library-owned config path. Queue #2 stays hand-rolled:
literal `events.SQSConfig{QueueURL, Region, Logger}` + `events.WithConcurrency(getEnvInt(
"CONSUMER_CONCURRENCY", 4))`, because `platform-events/pkg/config` only covers one queue per
service.

Consumer metrics (`events_consumed_total{queue,event_type,status}`,
`sqs_receive_errors_total{queue}`, `sqs_delete_errors_total{queue}`,
`sqs_visibility_extension_errors_total{queue}`, etc.) are activated by a single
`events.InitWithRegisterer("tender-acl", buildVersion, gincommon.MetricsRegisterer())` call in
`main.go` — the consumer internals compute these on every message regardless; they no-op until
this call runs once.

## AsyncAPI contract and viewer

`api/asyncapi.yaml` (AsyncAPI 3.0) is the source of truth for the event contract above —
`components.tags.{published,consumed}` + a `tags: [$ref consumed]` on both `TenantOffboarded` and
`TenantMembershipRemoved` messages (there is no `published` tag usage anywhere in this spec, by
design).

It is served two ways, both gated by `DocsConfig`/`docsAuthMiddleware` (dev-only by default;
`DOCS_ENABLED`+`DOCS_AUTH_TOKEN` opt production in), registered in
`registerDocsRoutes` (`internal/adapter/inbound/http/router.go`):

- **`GET /asyncapi.yaml`** — the raw spec, served directly from `apispec.AsyncAPISpec`
  (`api/embed.go`'s `//go:embed asyncapi.yaml` byte slice — compiled into the binary, not read
  from disk, because the final distroless image only contains the compiled binary, not the source
  tree).
- **`GET /asyncapi`** — a server-rendered HTML catalog (`internal/adapter/inbound/http/asyncapi.go`,
  ported from `iam-user-profile`'s identical viewer, renderer logic unchanged). It's entirely
  YAML-driven — walks `components.messages`/`components.schemas` directly via
  `splitMessagesByDirection`/`isConsumed()`, not a hardcoded name list — so it needed zero renderer
  changes to handle this service's own two-message, zero-published-message spec correctly. **One
  adaptation** beyond the ported code: the "Published Messages" section/sidebar group is omitted
  **entirely** (not rendered empty) since this service publishes nothing — `iam-user-profile`'s own
  viewer always renders both headings because it always has ≥1 published message. Both
  `TenantOffboarded` and `TenantMembershipRemoved` render under "Consumed Messages" with a
  `RECEIVE` badge.

`sync.Once`-cached parse (`loadAsyncSpec`/`parseAsyncSpec`) — no per-request disk I/O either way,
since the bytes are already in memory.
