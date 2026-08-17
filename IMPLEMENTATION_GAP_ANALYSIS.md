# Implementation Gap Analysis

Comparing `tender-acl-service-lld.md` (v2.0) against the current state of `iam-org-membership`
(O&M) as of this extraction, and recording what this repository (`iam-tender-acl`) implements to
close each gap — or deliberately does not. "Current State" describes O&M's code (or the LLD's own
text) as found; "Required Change"/"Implemented As" describes what this repo actually does. Affected
files are repo-relative to `iam-tender-acl` unless prefixed `iam-org-membership/`.

## Core endpoints (TAC-1, TAC-2, TAC-3, TAC-4)

| Requirement | Current State (O&M / LLD) | Implemented As | Risk | Affected Files |
|---|---|---|---|---|
| TAC-1 `GET .../acl` | O&M's P-21 (`acl_handler.go`'s `List`) | Ported to `httpadapter.Handler.List` | Low — logic unchanged, no DB dependency change | `internal/adapter/inbound/http/handler.go` |
| TAC-2 `POST .../acl` | O&M's P-22, membership check via in-process `s.memberships.FindByUserID` | Ported to `service.ACLService.Grant`, membership check repointed at `port.MembershipCheckClient.Exists` (synchronous HTTP call) | Medium — see "Grant-time membership-check error-code collapse" and "tenant_membership_id contract extension" below | `internal/core/service/acl_service.go`, `internal/adapter/outbound/membershipcheck/` |
| TAC-3 `DELETE .../acl/:user_id` | O&M's P-23 (`acl_handler.go`'s `Revoke`) | Ported to `service.ACLService.Revoke`/`httpadapter.Handler.Revoke`, returns `204 No Content` | Low, see optimistic-lock discrepancy below | `internal/core/service/acl_service.go`, `internal/adapter/inbound/http/handler.go` |
| TAC-4 `GET /internal/.../acl/:user_id` | O&M's I-12 (`internal_handler.go`'s `CheckTenderAccess`, registered without a `/api/v1` prefix at `cmd/server/main.go:519`) | Ported to `httpadapter.Handler.CheckAccess`, registered at `GET /internal/tenants/:id/tenders/:tender_id/acl/:user_id` (no `/api/v1` prefix) | Low — response contract (`has_access`/`access_level`/`expires_at`, never 404) byte-identical | `internal/adapter/inbound/http/internal_handler.go` |

## Discrepancy 1 — Optimistic-lock check on TAC-3 Revoke (LLD §12.1/§7.2.1 vs. actual code)

**LLD text**: §12.1/§7.2.1 specify a client-supplied `record_version` check on TAC-3 — the caller
sends the version it last read, `UPDATE ... WHERE id=$1 AND record_version=$2`, and zero rows
affected → `409 optimistic_lock_conflict`.

**Actual code (both O&M's source and this build)**: `Revoke()` is a plain, unconditional
`UPDATE tender_acl_entries SET deleted_at = now() WHERE tenant_id=$1 AND tender_id=$2 AND
user_id=$3 AND deleted_at IS NULL` — no version parameter, no lock check, no `409` path reachable
from this method. Confirmed by reading O&M's actual `tender_acl_repository.go` directly (not
assumed from the LLD).

**Decision made in this build**: preserve O&M's current behavior unchanged, per ADR-0007's
"preserve every... behaviour... unchanged" instruction (LLD §2) — do **not** add
caller-visible optimistic-lock behavior nobody currently sends a `record_version` for. The
`record_version` column and its trigger-driven bump (`trg_touch_tae`) are still installed (LLD's
own schema addition — see below), so the schema is forward-compatible with adding the check later,
but nothing gates on it today. `ErrCodeOptimisticLockConflict`/`409` is defined in
`internal/core/domain/errors.go` for forward-compatibility but is currently unreachable dead code from any
live path.

**Recommendation, not a decision**: if the optimistic-lock check should genuinely be added (e.g.
because some admin UI already tracks a version and expects the 409), that is a follow-up call
requiring its own review — not something to silently add in this pass.

Affected files: `internal/core/service/acl_service.go` (`Revoke`, has an inline comment pointing here),
`internal/adapter/outbound/postgres/migrations/0003_tender_acl_entries.up.sql`.

## Discrepancy 2 — Stale doc comment in O&M's source (found during research, independent of Discrepancy 1)

`iam-org-membership/internal/adapter/outbound/postgres/tender_acl_repository.go`'s
`SoftDeleteForUser` doc comment claims "for direct ACL revoke flows use `Revoke()`, which
optimistic-locks" — but `Revoke()` never has (see Discrepancy 1). This is a **latent documentation
bug in the source repository**, not something this extraction is responsible for fixing, and not
something the LLD mismatch above caused — it predates this extraction. Recorded here because it is
exactly the kind of stale assumption that could mislead a future reader of either repo into
believing TAC-3 already has concurrency protection it does not.

## Discrepancy 3 — `tenant_membership_id` provider-contract extension (LLD §7.6.2)

**LLD text**: §7.6.2's literal provider contract is `{"active": bool}`.

**Problem found while building**: `tender_acl_entries.tenant_membership_id` is `NOT NULL` (LLD
§7.2.1 schema, carried over unchanged from O&M). A bare boolean gives this service no way to
populate that column at grant time — the composite FK this check replaces is precisely what used
to guarantee a valid `tenant_membership_id` existed to reference.

**Implemented as**: `port.MembershipCheckClient.Exists` returns `(active bool, membershipID
uuid.UUID, err error)`, and `internal/adapter/outbound/membershipcheck/http_client.go` expects the provider response
to be extended to `{"active": bool, "tenant_membership_id": uuid|omitted}`. This is a **required
correction to the LLD**, not a silent implementation choice — `O_AND_M_DELTA.md` §4 specifies the
extended contract for whoever implements the actual O&M-side endpoint. Flagging explicitly: the LLD
itself needs an update here, since `{"active": bool}` alone is insufficient for any implementation,
not just this one's particular design choices.

Affected files: `internal/core/port/membershipcheck.go`, `internal/adapter/outbound/membershipcheck/http_client.go`,
`O_AND_M_DELTA.md` §4.

## Discrepancy 4 — Grant-time membership-check error-code collapse

**Current O&M behavior**: `TenderACLService.Grant`'s local `FindByUserID` call distinguishes
`ErrMemberNotFound` (grantee never a member) from found-but-not-`MembershipActive`
(`ErrMemberNotActive`) — two different domain errors, historically surfaced as different HTTP
statuses (404 vs 422).

**Implemented as**: the new `port.MembershipCheckClient.Exists` contract only carries `active bool` —
structurally, it cannot distinguish "never existed" from "existed but not active." Both collapse
into `grantee_not_active_member` (422) in `internal/core/service/acl_service.go`'s `Grant` (see the inline
comment there). This is an **intentional, reviewed narrowing**, not an oversight — recorded here
and in `O_AND_M_DELTA.md` §4 so a future reviewer who notices the lost distinction finds the
rationale rather than an unexplained gap.

## Gap — Per-user-removal ACL cascade (not covered anywhere in the LLD)

`MembershipService.RemoveUser` (O&M, ~line 544) performs a synchronous, same-transaction
`s.acls.SoftDeleteForUser(txCtx, tenantID, userID)` with no event emitted, unlike the
role/dept/delegation cascades three steps earlier in the same method. The LLD's §10.1 only
documents the **tenant**-offboarding cascade — this **per-user-removal** cascade is not accounted
for anywhere in the 21-section document. Once `tender_acl_entries` lives in a separate database,
this same-transaction call is physically impossible.

**Status: resolved (2026-08-17) — Option B implemented.** The user was asked explicitly which of
the two candidate solutions to build (post-commit best-effort notify client vs. a new lightweight
event) — full description of both, and this document's own recommendation of the notify-client
option, are preserved in `O_AND_M_DELTA.md` §5 for the record. **The user chose the event option
(Option B) over this document's recommendation.** Built: a new `TenantMembershipRemoved` SNS event
(O&M side: `internal/core/domain/event.go`/`event_payloads.go`, a new JSON Schema, emitted inside
`RemoveUser`'s existing `RunInTx` via the same transactional-outbox `pub.EnqueueCtx` pattern the
role/dept/delegation cascades already use) consumed by a new, independent
`member-removal-tenderacl-q` subscription in this repo (`internal/adapter/inbound/consumer/
member_removal_consumer.go`, a new `Repository.SoftDeleteForUser` soft-delete method, its own
metric/queue/DLQ/alerts, separate `processed_events` consumer name from the tenant-offboarding
one). Full details of what was built are in `O_AND_M_DELTA.md` §5's "Implemented (Option B)"
subsection.

**Consequence worth flagging on its own** (beyond just "the gap is closed"): this is a **second**
inbound event subscription, which means the LLD's own `TAC-EVT-2` invariant — "Consumes exactly one
event type (tenant-offboarding relay)... via own processed_events ledger + dedicated DLQ" — is now
**false as originally written**. This isn't a bug to silently work around; it's a genuine LLD
deviation this task's explicit user choice created, on top of a gap the LLD never anticipated in
the first place (§10.1 only documented the tenant cascade). See
`EVENT_COMPATIBILITY_REPORT.md`'s `TAC-EVT-2` section for the reconciled invariant text.

**Still open**: the LLD itself has never been updated to reflect either the original gap or this
resolution — §10.1 (and by extension §21's migration-plan Phase 3 description) should eventually
gain a section documenting `TenantMembershipRemoved` and the second queue, the same way `O_AND_M_DELTA.md`
and this document now do. That LLD amendment is out of scope for this task (this task modifies the
two service repos and their own root docs, not the standalone LLD source file) — flagging it here so
a future LLD revision doesn't miss it.

## Discrepancy 5 — §8.3 vs §8.4 route-prefix inconsistency for TAC-4

The LLD's own §8.3 endpoint table gives TAC-4 as `GET /internal/tenants/:id/tenders/:tender_id/acl/:user_id`
(no `/api/v1` prefix), while §8.4's request example header writes
`GET /api/v1/internal/tenants/{id}/tenders/{tender_id}/acl/{user_id}` (with the prefix) — an
internal inconsistency in the LLD itself, not something either source repo caused.

**Implemented as**: no `/api/v1` prefix, per §8.3's table — this also matches O&M's actual current
registration convention (`internal.GET("/tenants/:id/tenders/:tender_id/acl/:user_id", ...)` under
what is presumably an `/internal` group without `/api/v1`, confirmed by reading
`cmd/server/main.go` directly). **The LLD's §8.4 text is the one with the errata** — flagging this
so a future LLD revision fixes §8.4, not §8.3.

## Error-code-to-HTTP-status mapping — cross-checked against LLD §20 Appendix

`internal/core/domain/errors.go` + `internal/adapter/inbound/http/handler.go`'s `respondACLError` implement:

| Code | Status | LLD §20 says | Match? |
|---|---|---|---|
| `insufficient_role` | 403 | 403 | ✅ |
| `invalid_access_level` | 422 | 422 | ✅ |
| `invalid_reason` | 422 | 422 | ✅ |
| `invalid_expiry` | 422 | 422 | ✅ |
| `grantee_not_active_member` | 422 | 422 | ✅ |
| `optimistic_lock_conflict` | 409 | 409 | ✅ (defined, currently unreachable — see Discrepancy 1) |
| `duplicate_grant` | 409 | 409 | ✅ |
| `core_unavailable` | 503 | 503 | ✅ |
| `dependency_unavailable` | 503 | 503 | ✅ |
| `internal_server_error` | 500 | 500 | ✅ |
| `invalid_request` (malformed UUID path params / JSON body) | 400 | not in LLD §20's table | Gap — LLD's Appendix doesn't enumerate a generic malformed-request code; this repo adds one since gincommon's convention requires *some* code for unparseable input. Not a behavior change from O&M (which also 400s on malformed UUIDs), just a code the LLD's Appendix omitted. |

**Conclusion: fully aligned** with LLD §20 for every code the LLD actually enumerates; the one
addition (`invalid_request`/400) fills a gap the Appendix table left implicit rather than
contradicting it.

## Schema additions beyond O&M's current table (both explicitly called for by the LLD)

| Addition | LLD basis | Status |
|---|---|---|
| `record_version bigint NOT NULL DEFAULT 1 CHECK (record_version > 0)` | §7.2.1/§12.1 | Implemented. Column + trigger bump present; not yet gated on by Revoke (Discrepancy 1). |
| `reason CHECK (reason IS NULL OR char_length(reason) <= 500)` | §7.2.1, explicitly flagged by the LLD itself as "an LLD addition — the source schema left `reason` an unbounded text column" | Implemented, both at the DB layer (this CHECK) and the service layer (`maxReasonLength` in `internal/core/service/acl_service.go`). Not a caller-visible behavior change since the service-layer validation already enforced the same 500-char cap — this is defense-in-depth, not new-observable behavior. |
| Trigger name `trg_touch_tae` (LLD §7.5, verbatim) vs. O&M's current `trg_touch_tender_acl_entries` | §7.5 | Implemented using the LLD's name — the LLD is authoritative per this task's own instruction; the naming difference is cosmetic (same `touch_row()` function, same behavior) but recorded here for anyone diffing the two repos' migrations. |
| RLS policy hardening: `NULLIF(current_setting('app.tenant_id', true), '')::uuid` vs. the LLD's literal simpler `current_setting('app.tenant_id', true)::uuid` (§7.3) | Gap-3 instruction: match `iam-group-mapping`'s RLS migration exactly | Implemented using the hardened `NULLIF` form — `iam-group-mapping`'s own RLS test suite
(`TestRLS_NoGUCLeakageAcrossPooledConnection`) proved the simpler form can raise a cast error
rather than fail closed under PgBouncer transaction-pooling reuse. **The LLD's §7.3 literal SQL
should be corrected to include this hardening** — recorded here as an LLD correction, not a silent
deviation. |

## RLS forensic-logging machinery (TAC-D8)

O&M's `tender_acl_entries` RLS policy today wraps `tenant_id` checks in a `rls_check_tenant()`
`SECURITY DEFINER` helper with 1%-sampled violation logging to `rls_violation_log`. Per TAC-D8
(mirroring Wave-2's GM-D8), this service's policy is deliberately plain — no forensic wrapper, no
violation-sampling table. This is a documented, intentional scope reduction (not a regression in
tenant-isolation *enforcement*, only in *post-violation audit visibility*), confirmed present in
both the LLD text and this build's actual migration
(`internal/adapter/outbound/postgres/migrations/0003_tender_acl_entries.up.sql`).

## Package layout — LLD-mandated flat structure overrode the original task brief, later reversed

**Superseded — this service now uses Clean Architecture layering; the history below is kept for
context.** The task brief that kicked off this extraction instructed mirroring `iam-group-mapping`'s
full Clean Architecture layout (`internal/core/{domain,port,service}` +
`internal/adapter/{inbound,outbound}`). The LLD (§6, decision TAC-D1) explicitly and deliberately
specified a flat layout instead (`internal/{acl,membershipcheck,cache,consumer}`), calling it "a
deliberate, documented divergence ... not an oversight," and the initial build followed the LLD's
flat layout over the brief's own instruction on the reasoning that the LLD directly addressed the
question and the brief itself hedged elsewhere. That decision was later reversed on explicit
instruction to align with the sibling services' structure (see `CHANGELOG.md`) — the package
layout described in `ARCHITECTURE.md`'s "Layer model" section is the current, authoritative one.
TAC-D1 in the decision register is marked superseded rather than deleted, so this history isn't
lost.

## Minor mermaid-diagram inconsistency found while writing this document

`docs/architecture/mermaid/request-flow.mmd`'s TAC-3 branch labels the final response `200`; the
actual implemented status is `204 No Content` (confirmed in `internal/adapter/inbound/http/handler.go`'s `Revoke`).
This is a cosmetic diagram-vs-code drift from the scaffolding pass, not a behavior gap — noted here
rather than silently perpetuated in this document's own description of TAC-3 (see the endpoints
table above, which correctly states 204).

## Discrepancy 6 — `api/asyncapi.yaml`'s documented `TenantOffboarded` payload shape predates
confirmation of the real `platform-events` envelope wire format

Found while adding the second (`TenantMembershipRemoved`) receive operation to
`api/asyncapi.yaml` (Phase 3): the existing `TenantOffboardedPayload` schema documents fields
`event_id`/`event_type`/`tenant_id`/`occurred_at`, all nested under the message payload — but the
real `platform-events` library's `events.Envelope[T]` (confirmed via `go doc` against the actual
published module, not assumed) carries `id`/`type`/`tenant_id`/`subject`/`time` etc. as **top-level
envelope fields**, with `Payload`/`data` as a separate, genuinely event-specific sub-object.
`internal/adapter/inbound/consumer/offboarding_consumer.go`'s actual code already reads `env.ID`/`env.Type`/
`env.TenantID` directly (top-level), not from a nested payload — so the *code* is correct; only
`asyncapi.yaml`'s documented schema (written during an earlier scaffolding pass, before the
platform-libs real-API fix pass discovered the true envelope shape) is stale.

The new `TenantMembershipRemoved` entry added in this pass uses the confirmed-correct top-level
shape (`id`/`type`/`tenant_id`/`subject`/`time`) instead of repeating the same mistake. The
pre-existing `TenantOffboarded` entry was **not** rewritten in this pass (out of scope for a Phase
3 task that's about the new event, not reconciling an old one) — flagging it here so a future pass
fixes `TenantOffboardedPayload` to match reality rather than assuming the two entries' differing
shapes are intentional.

## Discrepancy 7 — `TenantMembershipRemoved` has not gone through `platform-schemagov` governance

O&M's own CI treats `api/asyncapi.yaml` as the schema source of truth, with
`internal/adapter/outbound/eventbus/schemas/*.json` **generated** from it via `platform-schemagov
extract --check` — not hand-written. This task added the new event's JSON schema by hand and did
**not** add a corresponding entry to O&M's `api/asyncapi.yaml` (a large, ~2550-line file with
intricate existing fan-out/filter-policy structure that deserves dedicated attention, not a rushed
append) or run `platform-schemagov` to verify it (attempted — `docker pull
ghcr.io/bcbp-solutions-fzc-llc/platform-schemagov:0.4` failed with `unauthorized`, no registry
credentials available in this environment). The event works correctly end-to-end (verified by real
integration tests), but this governance step is genuinely incomplete. Full detail in
`EVENT_COMPATIBILITY_REPORT.md`'s "Governance gap" section — this entry exists so this document's
own discrepancy catalog doesn't silently omit it.

## Migration plan status

Covered in full in `MIGRATION_RUNBOOK.md`. Summary of what's true today: this repository implements
Phase 1 ("Expand" — service exists, independently deployable and testable) in full, Phase 2 (data
export tooling, built and dry-run verified against synthetic data — no real O&M data existed to
migrate in this environment), Phase 3 (provider endpoint + `TenantMembershipRemoved`
notify-event, both built and unit/integration-tested in both repos), and Phase 4 (read cutover,
partially — see below) with their code complete but **not yet deployed or soaked against any real
staging/production environment** — see `MIGRATION_RUNBOOK.md`'s own per-phase status. **Phases 6
and 7 are now also executed**: O&M's P-21/22/23/I-12 handlers/service/repository/domain code have
been deleted entirely (Phase 6), and `tender_acl_entries`/`tender_acl_level` have been dropped from
O&M's schema via a new migration, applied against both a fresh testcontainer and the persistent
local dev database (Phase 7, irreversible). What remains genuinely unexecuted across every phase
is exclusively infrastructure/deployment work with no code representation here (see Discrepancies
8-9 below) — not schema or application code.

**Discrepancy 8 — Phase 4's two other named callers don't exist as real codebases.** The LLD's
Phase 4 description names three callers to repoint at TAC-4/TAC-1: "Tender Service, AuthZ
Enrichment" for TAC-4, and "admin tooling" for TAC-1. A research pass across every repo on this
machine confirmed: (a) **Tender Service doesn't exist yet** — the tender-acl LLD itself says so
(§22, ADR-0007 Option D is explicitly deferred pending that service's existence); (b) **"admin
tooling" isn't a real repo either** — TAC-1/2/3 are plain admin-gated HTTP routes, and whatever
ops/frontend client calls them isn't code in this workspace. Only `iam-authz-enrichment` (the LLD's
"AuthZ Enrichment") is real, and it has been repointed (see `MIGRATION_RUNBOOK.md` Phase 4). This
means Phase 4 is **not fully complete** even though the one executable part of it is — flagging
this explicitly so "Phase 4 done" isn't read as "every caller repointed," which isn't true and
can't be made true in this environment.

**Discrepancy 9 — Phase 5's tenant-offboarding event-routing repoint cannot be executed from any
code in this workspace.** Phase 5's action items include "O&M's tenant-offboarding publish path is
repointed to also target `tenant-lifecycle-tenderacl-q`." Research found: `TenantOffboarded` is not
produced by `iam-org-membership` at all — per that repo's own reference LLD (§7.1/§15.5/EVT-7), the
producer is **iam-realm-provisioner**, after a verified export+delete. A full grep of
`iam-realm-provisioner` for "Offboard" (case-insensitive) returned **zero hits anywhere in that
repo** — the stated producer does not actually implement this emission, the same class of finding
as Discrepancy 8's "Tender Service doesn't exist." Separately, even if it did, the actual SNS-topic
subscription/filter-policy wiring that would route the event to a new queue is **platform
Terraform-owned infrastructure, not application code** — `iam-group-mapping`'s own Wave 2
precedent for its equivalent queue (`deploy/iam/policy.tf.example`) confirms this explicitly, only
granting IAM permissions on an already-existing queue resource with the comment "Platform Terraform
owns the authoritative version of this resource." Also notable: `iam-group-mapping` has **no
`MIGRATION_RUNBOOK.md`/`O_AND_M_DELTA.md` at all** — its own Wave 2 write cutover never documented
how (or whether) it resolved this identical ambiguity, so there is no working precedent to follow
even in principle. **What this means concretely**: `iam-tender-acl`'s `OffboardingConsumer` on
`tenant-lifecycle-tenderacl-q` is built, tested, and ready — but whether it will ever receive a
real `TenantOffboarded` message depends on infrastructure work (implementing the emission in
`iam-realm-provisioner`, then provisioning the SNS subscription in platform Terraform) that is
entirely outside every repo in this workspace. This is not something a follow-up task against
`iam-org-membership` or `iam-tender-acl` alone can close.

**Minor, related gap — resolved in Phase 6**: `iam-org-membership`'s swagger docs (`docs/swagger/`)
and the OpenAPI annotations on `ACLHandler`/`CheckTenderAccess` described the pre-cutover
200/201/204 contract through Phase 5 (not regenerated or marked-deprecated at that point, since
the code was already slated for full deletion rather than ongoing maintenance). Phase 6 deleted
the annotated methods themselves and re-ran `make swag`; `docs/swagger/{docs.go,swagger.json,
swagger.yaml}` no longer mention any tender-ACL route or DTO.

## Discrepancy 10 — a second, independent ACL cascade call site was missed by the original Phase 3
pass, only caught during Phase 6 prep

`O_AND_M_DELTA.md` §2's "not removed in this Phase 1 delta" note (and Phase 3's actual
implementation) tracked exactly one call site for the per-user-removal ACL cascade:
`MembershipService.RemoveUser`'s `s.acls.SoftDeleteForUser(txCtx, tenantID, userID)`. Tracing every
reference to `TenderACLRepository`/`acls` across O&M before deleting the subsystem in Phase 6
surfaced a **second, structurally identical call site that neither the Phase 1 delta analysis nor
the Phase 3 implementation had accounted for**: `ProvisioningService.DeleteMember` (I-5, the
Keycloak `USER_DELETE` webhook cascade) also called `s.acls.SoftDeleteForUser` directly, in its own
transaction, independent of `MembershipService.RemoveUser`. Since `tender_acl_entries` no longer
lives in O&M's database at all after Phase 7, this call would have been a straightforward compile
error the moment the `TenderACLRepository` interface was deleted — it was not a silent runtime bug
in production, but it **was** a real gap in this task's own Phase 3 emission-parity work: I-5's
cascade was never given the same `TenantMembershipRemoved` event emission that Phase 3 added to
P-8's cascade, so until this fix, deleting a user via the Keycloak webhook path would not have
notified `iam-tender-acl` to soft-delete that user's ACL grants, while removing them via the
tenant-facing `RemoveUser` API would have.

**Fix applied** (Phase 6, same Option B pattern the user chose in Phase 3): `DeleteMember` now
unconditionally enqueues `TenantMembershipRemoved` (via the existing outbox-scoped
`port.EventPublisherFromContext`, same as every other cascade event in that method) instead of the
removed `s.acls.SoftDeleteForUser` call. The `acls port.TenderACLRepository` field and constructor
parameter were removed from `ProvisioningService` entirely (mirroring the identical removal
already done on `MembershipService`). Verified via `test/unit/provisioning_service_test.go`'s
`TestProvisioning_DeleteMember_WorkflowNotRequired` and the full O&M test suite (unit +
`integration` + `e2e` tags), all green.
