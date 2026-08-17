# Org & Membership (O&M) Delta

**Status: fully executed as of `MIGRATION_RUNBOOK.md` Phase 7.** This document originally shipped
as descriptive-only (Phase 1: no `iam-org-membership` code touched) and was carried out
incrementally in the phases that followed — §§1/2 in Phase 6, §3 in Phase 7, §4/§5 in Phase 3, §7's
ordering concern resolved by the same Phase 7 migration. Every section below is annotated with its
execution status; nothing in this document remains undone.

## 1. Endpoints removed from O&M ✅ executed (Phase 5 retire, Phase 6 delete)

| Old ID | Route | Disposition |
|---|---|---|
| P-21 | `GET /tenants/:id/tenders/:tender_id/acl` | Replaced by TAC-1 in this service. O&M's handler must first return `410 Gone` (soak period), then be deleted entirely (see `MIGRATION_RUNBOOK.md` Phase 5). |
| P-22 | `POST /tenants/:id/tenders/:tender_id/acl` | Replaced by TAC-2. Same `410`-then-delete sequencing. |
| P-23 | `DELETE /tenants/:id/tenders/:tender_id/acl/:user_id` | Replaced by TAC-3. Same sequencing. |
| I-12 | `GET /tenants/:id/tenders/:tender_id/acl/:user_id` (internal, registered under the `internal` route group) | Replaced by TAC-4. Same sequencing — callers (Tender Service, AuthZ Enrichment) must be repointed at this service before O&M's copy returns `410`. |

**Not moving**: `TenderAssigneeOverridden`'s write path (`AssigneeOverride`/`ValidateAndEmitAssigneeOverride`
in `internal_handler.go`, I-13) stays in O&M unchanged — it is unrelated to `tender_acl_entries`
(see `EVENT_COMPATIBILITY_REPORT.md`).

Files: `internal/adapter/inbound/http/acl_handler.go` (delete `List`/`Grant`/`Revoke` — TAC-1/2/3),
`internal/adapter/inbound/http/internal_handler.go` (delete `CheckTenderAccess`, ~lines 528-573 —
TAC-4/I-12; **keep** every other method in this file, e.g. `AssigneeOverride`, `ProvisionTenant`,
etc., since they're unrelated to ACL), `cmd/server/main.go` (remove the route registrations at
lines 478-481 for TAC-1/2/3 and line 519 for TAC-4).

**Executed:** Phase 5 first retired all four routes to `410 Gone`/`endpoint_retired`; Phase 6
deleted `acl_handler.go`, `CheckTenderAccess`, the four route registrations, and their DTOs
entirely (IDs never reused, per the I-6/I-7 quota-retirement precedent). O&M's full test suite
(unit + `integration` + `e2e` build tags) is green with the handlers gone.

## 2. Repositories/services/domain removed from O&M ✅ executed (Phase 6)

| Component | Disposition |
|---|---|
| `internal/core/domain/tender_acl.go` (`TenderACLEntry`, `TenderACLLevel`, `IsActive`) | Delete. Superseded by this service's own `internal/core/domain/acl.go`. |
| `internal/core/port/acl_repository.go` (`TenderACLRepository` interface) | Delete. Superseded by this service's own `internal/core/port/acl_repository.go`. |
| `internal/core/service/tender_acl_service.go` (`TenderACLService`) | Delete. Superseded by this service's own `internal/core/service/acl_service.go`. |
| `internal/adapter/outbound/postgres/tender_acl_repository.go` | Delete. Superseded by this service's own `internal/adapter/outbound/postgres/repository.go`. |

**Executed:** all four components deleted in Phase 6 (`git rm`, not disabled). A second call site
that this Phase-1 delta had not yet found — `ProvisioningService.DeleteMember`'s (I-5) own
`s.acls.SoftDeleteForUser` call, distinct from `MembershipService.RemoveUser`'s — was discovered
during Phase 6 prep; see `IMPLEMENTATION_GAP_ANALYSIS.md`'s dedicated discrepancy entry for that
gap and its fix (same Option B event-emission pattern as §5 below).

**Was** (Phase 1 note, now historical): `MembershipService.RemoveUser`'s call to
`s.acls.SoftDeleteForUser(txCtx, tenantID, userID)` (around line 544 of
`internal/core/service/membership_service.go`) stays exactly as-is for now — it becomes physically
impossible once `tender_acl_entries` lives in a different database, but replacing it is a genuine
design decision requiring review, not a mechanical deletion. See §5 below.

## 3. Table drop ✅ executed (Phase 7, irreversible)

`tender_acl_entries` and `tender_acl_level` were dropped from O&M's schema via
`internal/adapter/outbound/postgres/migrations/000015_drop_tender_acl_table.up.sql`, after Phase 6
confirmed zero remaining code references. A pre-drop snapshot (`pg_dump --format=custom` of the
local dev database) was taken immediately before applying the migration, per this runbook's own
requirement — retained alongside this task's working files. The migration was applied both to a
fresh testcontainer (via O&M's full `unit`/`integration`/`e2e` test suites, all green) and to the
persistent local dev Postgres (`org_membership` on `localhost:5534`), which held zero rows in
either table beforehand (an unseeded dev environment, not a signal about production volume) —
confirmed post-drop via `\dt`/`pg_type` that both objects are gone and `pgcommon_migrations` shows
version 15 applied cleanly.

**The `tender_acl_level` ENUM does NOT need a local copy kept behind in O&M.** Verified by grep
during this extraction: every reference to `tender_acl_level`/`TenderACLLevel` in
`iam-org-membership`'s tree lives inside the ACL subsystem itself (`internal/core/domain/tender_acl.go`,
`internal/core/service/tender_acl_service.go`, `internal/adapter/inbound/http/acl_handler.go`,
`internal/adapter/outbound/postgres/tender_acl_repository.go`, and the ACL-owned migration files).
Nothing outside that subsystem — no other domain type, no other service, no other migration —
references the ENUM. This is a genuine difference from the Wave-1 catalog-admin precedent (where
`tenant_plan`/`branding_level` DID need local copies kept behind, since `tenants.plan` and
`Tenant.FeatureFlags` still reference them): here, the whole vocabulary can simply be dropped with
the table in one migration: `DROP TABLE tender_acl_entries; DROP TYPE tender_acl_level;`.

## 4. New provider endpoint — reverse data-flow direction vs. precedent — IMPLEMENTED

**This is the one place this extraction's data-flow direction is the opposite of the Wave 1
(catalog-admin) and Wave 2 (group-mapping) precedents.** In both of those, `iam-org-membership` was
purely a *consumer* of a new endpoint the extracted service exposed. Here, `iam-org-membership`
must become a *provider* — the new `iam-tender-acl` service calls **into** O&M, not the other way
around.

**Endpoint**: `GET /internal/tenants/:id/members/:user_id/exists` — built (ADR-0007 Wave 3 Phase
3): `InternalHandler.CheckMemberExists` (`internal/adapter/inbound/http/internal_handler.go`),
backed by the new lean service method `MembershipService.CheckActiveMembership`
(`internal/core/service/membership_service.go` — a thin `FindByUserID` wrapper, deliberately not a
call to the existing `Get()`, which also resolves roles/dept memberships this existence check has
no use for and which `iam-tender-acl` calls synchronously on every ACL grant — LLD §7.6.2's
≤50ms-p99 budget makes the extra queries pure overhead here), registered in `cmd/server/main.go`.
New DTO `MemberExistsResponse` in `dto.go`. Swagger docs regenerated (`make swag`) and confirmed
compiling. Covered by handler-level validation tests (invalid tenant/user UUID → 400, matching this
codebase's existing convention of testing only validation branches at the handler layer) and full
service-level business-logic tests in `test/unit/membership_service_reads_test.go`
(`TestMembership_CheckActiveMembership_*`: active member, suspended member, not-found, repo error).
Full O&M test suite (unit + `integration` + `e2e` build tags) reverified green.

Confirmed absent before this task (read `internal_handler.go` in full and grepped the whole repo for
`members/:user_id/exists` and `/exists` under `internal/` — no match). Wraps
`MembershipRepository.FindByUserID(ctx, tenantID, userID) (*domain.TenantMembership, error)`
(`internal/core/port/membership_repository.go`).

**Important — the response shape cannot be the originally-scoped bare `{"active": bool}`.**
`tender_acl_entries.tenant_membership_id` is `NOT NULL` (LLD §7.2.1), and once the table lives in a
separate database, the new service has no other way to obtain that value at grant time — the
composite FK that used to guarantee a valid row is exactly what this endpoint replaces. The
response must therefore be:

```jsonc
// found, Status == domain.MembershipActive
{ "active": true, "tenant_membership_id": "3f2b8b1a-...-uuid" }
// ErrMemberNotFound, or found but any non-active status
{ "active": false }
```

Handler logic:
1. `m, err := membershipRepo.FindByUserID(ctx, tenantID, userID)`
2. `errors.Is(err, domain.ErrMemberNotFound)` → `{"active": false}`, 200 (not 404 — this is an
   existence check, not a resource fetch)
3. `err != nil` (other error) → `503`/`500` per O&M's existing internal-route error convention
4. `m.Status != domain.MembershipActive` → `{"active": false}`, 200
5. `m.Status == domain.MembershipActive` → `{"active": true, "tenant_membership_id": m.ID}`, 200

Auth: mesh-only, same `iam-system` internal-caller convention every other internal route in this
platform uses (no JWT, mTLS boundary) — the new service's `internal/adapter/outbound/membershipcheck/http_client.go`
already sends `X-User-Id: iam-system` / `X-User-Roles: iam-system` on this call, matching
`iam-group-mapping`'s `catalogclient` convention against `iam-catalog-admin`'s CAT-I1/CAT-I2.

**Error-code collapse — an intentional, reviewed behavior narrowing, not an oversight.** Today,
`TenderACLService.Grant`'s local `s.memberships.FindByUserID` call distinguishes two outcomes:
`ErrMemberNotFound` (the grantee has never been a member — historically surfaced as 404) versus
found-but-`Status != MembershipActive` (surfaced as `member_not_active`, 422, via
`domain.ErrMemberNotActive`). The new provider contract above collapses both into a single
`active: false` boolean — structurally, a network response can't carry the same 404-vs-422
distinction as an in-process error return without inventing a second field nobody asked for. The
new service maps `active: false` uniformly to `grantee_not_active_member` (422) regardless of
which of the two original causes produced it. This is recorded as a deliberate, reviewed narrowing
in `IMPLEMENTATION_GAP_ANALYSIS.md` — flag before treating it as final if the distinction turns out
to matter to any caller/monitoring dashboard downstream.

## 5. New outbound notify client — the per-user-removal ACL cascade gap

`MembershipService.RemoveUser` (P-8/I-5, `internal/core/service/membership_service.go`, ~line 544)
currently performs a **synchronous, same-transaction** call:

```go
// inside the same RunInTx as the membership soft-delete, after the
// role/dept/delegation cascades (which DO emit TenantRoleRevoked /
// DepartmentMembershipRevoked / DelegationEnded three steps earlier)
_ = s.acls.SoftDeleteForUser(txCtx, tenantID, userID)
```

with **no event emitted** for it. Once `tender_acl_entries` lives in `iam-tender-acl`'s own
database, this call is **physically impossible** — there is no cross-database transaction to
participate in. This is a real design gap the LLD's §10.1 doesn't cover (that section only
documents the *tenant*-offboarding cascade, not the *per-user-removal* one).

**Decision (2026-08-17): Option B, chosen explicitly by the user over this document's own
recommendation of Option A.** Both options below are kept as originally written for the historical
record of what was weighed; the "Implemented" subsection after them describes what was actually
built.

### Option A (recommended by this document, not the option chosen)

Reuse the existing post-commit, best-effort, fire-and-forget pattern `RemoveUser` already applies
two lines later for Realm Provisioner's session revocation:

```go
// ~line 561 today:
if s.rp != nil {
    _ = s.rp.RevokeUserSessions(ctx, tenantID, userID)
}
```

Add `internal/adapter/outbound/tenderacl/` in O&M with:

```go
type Client interface {
    NotifyUserRemoved(ctx context.Context, tenantID, userID uuid.UUID) error
}
```

called post-commit, immediately alongside the `RevokeUserSessions` call, error logged and
swallowed — never blocking or failing `RemoveUser` itself. The new `iam-tender-acl` service exposes
a small internal endpoint (e.g. `POST /internal/tenants/:id/members/:user_id/removed` — exact
shape TBD by whoever implements this) that soft-deletes that user's rows (mirroring what
`SoftDeleteForUser` does today).

**Rationale**: the LLD's own TAC-EVT-4/TAC-FAIL-2 framing establishes that a delayed or even
indefinitely-failed cascade here is never a live authorization risk — a removed user cannot present
valid gateway-injected headers again regardless of whether their `tender_acl_entries` rows have
been cleaned up. This is the same argument that justifies the tenant-offboarding cascade being
async (§10.1) rather than synchronous; it applies with even less urgency at the single-user grain.

### Option B (chosen)

Introduce a new lightweight SNS event, `TenantMembershipRemoved`, that `iam-tender-acl`'s
`internal/adapter/inbound/consumer` subscribes to, extending it beyond its original single
`TenantOffboarded` subscription. This requires: a new event schema entry in O&M's
`internal/adapter/outbound/eventbus/schemas/`, a new queue (chosen over reusing
`tenant-lifecycle-tenderacl-q` with a discriminated payload — see "Implemented" below for why), a
new `processed_events`-style idempotency check, and `schema-gov` registration.

This is objectively heavier machinery for a cascade the LLD's own framing says is low-stakes; the
recommendation above (Option A) still stands as this document's technical assessment. The user
explicitly chose Option B anyway when asked, so it is what was built — see the LLD-deviation note
this creates for `TAC-EVT-2` in `EVENT_COMPATIBILITY_REPORT.md` (this service no longer "consumes
exactly one event type," which the original LLD explicitly asserted).

### Implemented (Option B)

**In `iam-org-membership`** (this task's own changes, ADR-0007 Wave 3 Phase 3):
- `internal/core/domain/event.go`: new constant `EventTenantMembershipRemoved =
  "TenantMembershipRemoved"`, routed to the existing `iam.membership.events` SNS topic via
  `TopicForEvent`'s default case (no new topic).
- `internal/core/domain/event_payloads.go`: new `TenantMembershipRemovedPayload{UserID, TenantID,
  ActorID}` — deliberately minimal, just enough to scope the cascade.
- `internal/adapter/outbound/eventbus/schemas/TenantMembershipRemoved.json`: new JSON Schema
  (Draft-07), auto-loaded by `ValidatingCodec`'s `//go:embed schemas/*.json` — no registration list
  to update elsewhere.
- `internal/core/service/membership_service.go`'s `RemoveUser`: emits the new event via
  `pub.EnqueueCtx` **inside the same `RunInTx`** as the (still-present, unremoved)
  `s.acls.SoftDeleteForUser` call — the transactional-outbox pattern every other cascade event in
  this method already uses (`TenantRoleRevoked`/`DepartmentMembershipRevoked`/`DelegationEnded`),
  not the post-commit fire-and-forget pattern Option A would have used (events, unlike an HTTP call,
  *can* participate in the DB transaction, so there was no reason to give up that guarantee).
  Emitted unconditionally on every `RemoveUser` call, not gated on whether the user actually held
  any ACL grants — `iam-tender-acl`'s consumer is idempotent regardless.
- Existing `RemoveUser` unit tests updated for the new event appearing in the emitted-events list
  (3 tests: happy-path cascade, delegator-direction, two-depts-one-delegation) — all now assert one
  additional `EventTenantMembershipRemoved` at the end of the list. New tests added in
  `test/unit/membership_service_reads_test.go` for the (unexported-detail-free) service method
  `MembershipService.CheckActiveMembership` are unrelated to this item — see §4 above.
- Full test suite (unit + `integration` + `e2e` build tags, testcontainers-backed) reverified green
  after these changes.

**In `iam-tender-acl`** (this repo):
- **A second, independent SQS subscription** — `member-removal-tenderacl-q` (+
  `member-removal-tenderacl-q-dlq`, `maxReceiveCount=5`) — not a discriminated payload on
  `tenant-lifecycle-tenderacl-q`. Rationale: the two cascades are different failure domains (a
  stuck tenant-offboarding delete vs. a stuck per-user soft-delete are different incidents with
  different runbooks); a shared queue would conflate their DLQ depth, error-rate metrics, and
  on-call signal. This also matches the queue name's own existing scope (`tenant-lifecycle`
  specifically), which a per-user event doesn't belong under semantically.
- `internal/adapter/inbound/consumer/member_removal_consumer.go`: `MemberRemovalConsumer`, structurally identical
  to `OffboardingConsumer` (validate event type → parse `id`/`tenant_id` (envelope top-level) and
  `subject` (the removed user's id, matching the same convention
  `DelegationEnded`/`TenantRoleRevoked`/etc. already use for *their* subject) → idempotency check →
  cascade → mark processed).
- `internal/core/port/acl_repository.go` + `internal/adapter/outbound/postgres/repository.go`: new `Repository.
  SoftDeleteForUser(ctx, tenantID, userID) (deleted int64, err error)` — a **soft** delete (unlike
  `CascadeDeleteForTenant`'s hard delete), matching both O&M's original per-user semantics and this
  table's own retention framing (LLD §18 — revoked rows are kept indefinitely for audit outside of
  tenant offboarding).
- `internal/adapter/inbound/consumer/processed_events.go`: `ProcessedEvents` is now constructed with an explicit
  `consumer` name (`NewProcessedEvents(pool, consumer string)`) so the two consumers' idempotency
  ledgers stay independent under the table's composite `(event_id, consumer)` key — was previously
  a single hardcoded `"tenant_lifecycle_cleanup"` constant.
- `internal/adapter/outbound/metrics/metrics.go`: new, **separate** counter `tender_acl_member_removal_cascade_total`
  (not a shared metric with an extra label) — same reasoning as the separate queue.
- `cmd/tender-acl/main.go` / `config.go`: second `events.NewSQSConsumer`, second
  `MEMBER_REMOVAL_SQS_QUEUE_URL` required env var, wired into the same `errgroup` (both consumers,
  both processed-events cleanup loops, both graceful-shutdown `Stop()` calls).
- `api/asyncapi.yaml`: second `receive` operation (`receiveTenantMembershipRemoved`) — asyncapi.yaml
  still declares **zero** `send` operations (TAC-EVT-5 unaffected).
- `scripts/init-localstack.sh`, `docker-compose.yml`, `.env.example`, Helm `values.yaml`,
  `deploy/iam/policy.json`/`policy.tf.example`, `deploy/monitoring/{app-alerts.yml,slo-rules.yml}` +
  Helm `templates/prometheusrule.yaml` (new SLO-4): all extended for the second queue/DLQ/alert
  pair, mirroring the existing tenant-offboarding ones.
- New tests: `internal/adapter/inbound/consumer/member_removal_consumer_test.go` (9 unit tests, mirroring
  `offboarding_consumer_test.go`'s coverage exactly), plus integration tests in
  `test/integration/{repository_test.go,consumer_test.go}` against real Postgres (soft-delete
  fidelity, scoping to exactly one user, idempotent duplicate delivery, cross-consumer
  `processed_events` independence).
- All four test tiers (unit `-race`, `integration`, `rls`, `e2e`) reverified green against real
  Postgres/Valkey testcontainers after these changes. `go-arch-lint` reverified clean (no new
  component-boundary edges — `consumer` still has no import of `acl`, satisfying
  `UserRemovalCascader`/`MemberRemovalMetrics` structurally, exactly like `CascadeDeleter` already
  did).

**Not yet done** (genuinely still pending, unlike the items above): this event has not been
emitted against, or consumed from, any real staging/production AWS account — SQS queue
provisioning (Terraform, using the updated `policy.tf.example`) and a live soak period are Phase 3
runbook items still outstanding, same caveat as Phase 2's data-export tooling.

## 6. Cache changes

**iam-org-membership itself: none.** It does not cache ACL data today — there is no `om:acl`-style
key anywhere in its Valkey usage, unlike its `om:departments`/`om:plans` caching for the Wave-1
extraction. There is nothing to invalidate, migrate, or dual-write in O&M as part of this delta.

**Clarification, not a delta**: the *new* `iam-tender-acl` service does introduce a cache —
Valkey, key `tac:acl:{tenant}:{tender}:{user}`, fixed 30s TTL, DELETE-based invalidation on
grant/revoke, used only by TAC-4 (`CheckAccess`). This is wholly new infrastructure inside the new
repository; it has no analog in O&M to migrate away from, so it does not belong in this delta
document — it's covered in the new repo's own `ARCHITECTURE.md` (Cache Strategy section).

## 7. Migration order

Mirrors `MIGRATION_RUNBOOK.md`'s phase structure (Expand → Cut over reads → Cut over writes →
Contract), but this wave's risk profile is deliberately **the lowest of the three ADR-0007 waves**:
single admin-gated table, low write volume (grant/revoke are infrequent admin actions, not a hot
write path), and the service itself is explicitly disposable — slated for reabsorption into the
Tender Service at Wave 4 (§22 of the LLD) rather than being a long-lived microservice. Concretely:

1. **Expand** — `iam-tender-acl` stands up its own schema, seeded from O&M's current
   `tender_acl_entries` rows (one-time export). O&M's P-21/22/23/I-12 stay live and authoritative.
   The provider endpoint (§4) and notify client (§5) are built in O&M but not yet called by
   anything live.
2. **Cut over reads** — Tender Service and AuthZ Enrichment (I-12's callers) are repointed at
   TAC-4; admin tooling repointed at TAC-1. O&M's routes stay live as a rollback target.
3. **Cut over writes** — admin tooling repointed at TAC-2/TAC-3. O&M's P-21/22/23/I-12 handlers
   return `410 Gone` (not deleted). Note: unlike the originally-planned Option A (a notify client
   O&M would only start *calling* at this step), the chosen Option B (§5) has `RemoveUser`
   unconditionally emitting `TenantMembershipRemoved` from the moment this Phase 3 code deploys —
   there is no separate "start emitting" step. What changes at *this* step is only that
   `iam-tender-acl`'s own copy of `tender_acl_entries` becomes the copy that matters (before this
   step, the cascade is real but inert — it's tidying up rows nothing reads yet). O&M's
   tenant-offboarding path (already async elsewhere in O&M) is repointed at
   `tenant-lifecycle-tenderacl-q` at this same step.
4. **Contract** — remove the `410` handlers entirely; drop `tender_acl_entries` and
   `tender_acl_level` from O&M's schema (§3 — no local copy needed, unlike the Wave-1 ENUMs).

**Rollback** is straightforward through the end of step 3 (repoint tooling back at O&M, which still
has live tables and handlers). After step 4 it requires restoring from a pre-drop snapshot — but
given this table's low write volume and admin-only access pattern, the operational blast radius of
a rollback here is smaller than either prior wave's.
