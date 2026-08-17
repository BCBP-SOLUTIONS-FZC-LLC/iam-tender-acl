# Event Compatibility Report

Per `tender-acl-service-lld.md` §10 (TAC-EVT-1..5). Unlike the Wave-1 (`iam-catalog-admin`)
precedent, this service is **not** event-silent, and zero outbound publishing still holds — but as
of ADR-0007 Wave 3 Phase 3, it now has **two** inbound subscriptions, not the one the LLD
originally specified. This report verifies each TAC-EVT invariant against the actually-built code
(not just the LLD text), reconciles the LLD deviation `TAC-EVT-2` created, and confirms
`TenderAssigneeOverridden` was correctly left behind in `iam-org-membership`.

## TAC-EVT invariants — verified against the built service

| # | Invariant (LLD §10.5) | Verified against | Status |
|---|---|---|---|
| TAC-EVT-1 | No SNS events published for own domain writes — no outbox table, no publisher wiring | `go.mod` has `platform-events` as a dependency (needed for the *consumer* side only); grepped `internal/` and `cmd/` for `NewSNSPublisher`/`NewRoutingPublisher`/`outbox.ApplySchema`/`outbox.NewRunner` — no match anywhere in this repository. `internal/core/service/acl_service.go`'s `Grant`/`Revoke` only call the repository and the cache, never a publisher. Still true after Phase 3: `TenantMembershipRemoved` is a new event this repository *consumes*, not one it publishes — it is published by `iam-org-membership`. | **Confirmed.** |
| TAC-EVT-2 | ~~Consumes exactly one event type (tenant-offboarding relay)~~ via its own `processed_events` ledger + dedicated DLQ (`maxReceiveCount=5`) | **This invariant is now FALSE AS ORIGINALLY WRITTEN — a deliberate, user-chosen LLD deviation, not a bug.** ADR-0007 Wave 3 Phase 3 (`O_AND_M_DELTA.md` §5, "Option B", explicitly chosen by the user over this document's own Option-A recommendation) adds a second inbound subscription: `TenantMembershipRemoved` on `member-removal-tenderacl-q`, replacing the per-user-removal ACL cascade gap the original LLD never anticipated (§10.1 only documented the tenant-level cascade). **Reconciled invariant**: this service consumes exactly two event types — `TenantOffboarded` (`tenant-lifecycle-tenderacl-q`) and `TenantMembershipRemoved` (`member-removal-tenderacl-q`) — each via its **own independent** `processed_events`-ledger scope (`consumer = "tenant_lifecycle_cleanup"` vs. `consumer = "member_removal"`, same composite-key table) and its **own dedicated** DLQ (`maxReceiveCount=5` on both). `internal/adapter/inbound/consumer/offboarding_consumer.go` and `internal/adapter/inbound/consumer/member_removal_consumer.go` each recognize exactly one event type, skip-not-retry on a mismatched `event_type`, and share no queue/DLQ/metric — verified via `internal/adapter/outbound/metrics/metrics.go`'s two separate counters (`tender_acl_tenant_offboarding_cascade_total` / `tender_acl_member_removal_cascade_total`) and the two `NewProcessedEvents(pool, consumer)` call sites in `cmd/tender-acl/main.go`. | **Deviation confirmed and reconciled — see above; not the LLD's original invariant.** |
| TAC-EVT-3 | Cascade-deletes are idempotent, require no ordering | Tenant-offboarding: `internal/adapter/outbound/postgres/repository.go`'s `CascadeDeleteForTenant` is a plain `DELETE FROM tender_acl_entries WHERE tenant_id=$1` — a no-op on an already-cleaned tenant. Per-user-removal (new, Phase 3): `SoftDeleteForUser` is `UPDATE ... SET deleted_at = now() WHERE tenant_id=$1 AND user_id=$2 AND deleted_at IS NULL` — equally a no-op once already soft-deleted (verified in `test/integration/repository_test.go`'s `TestRepository_SoftDeleteForUser_Idempotent`). Both consumers' `IsProcessed`/`MarkProcessed` idempotency checks are a belt-and-suspenders dedup layer on top of that, not the source of the idempotency itself. Neither cascade has any cross-event ordering dependency — each event fully determines its own outcome independent of delivery order or duplication. | **Confirmed for both subscriptions.** |
| TAC-EVT-4 | Delayed/failed cleanup is never a live authz risk | Tenant-offboarding: structural argument inherited from the LLD (§7.6.3) — a departed/offboarded tenant's users have no active `tenant_memberships` row, so AuthZ Enrichment's I-8 hot path never issues them the gateway-injected headers TAC-4 trusts; not independently verifiable from within this repo. Per-user-removal (new, Phase 3): the identical argument applies at the single-user grain — a removed user has no active membership either, so the same I-8 gate excludes them regardless of whether their `tender_acl_entries` rows have been cleaned up yet (this is exactly `O_AND_M_DELTA.md` §5's own rationale for choosing an async cascade over the old synchronous same-transaction call). `internal/core/service/acl_service.go`'s `CheckAccess` predicate (`deleted_at IS NULL AND (expires_at IS NULL OR expires_at > now())`) is identical regardless of which cascade would eventually touch a given row. | **Confirmed by design argument for both** (not independently testable from within this repo). |
| TAC-EVT-5 | `asyncapi.yaml` declares one receive operation and zero send operations | **Deviation, same root cause as TAC-EVT-2**: `api/asyncapi.yaml`'s `operations:` block now contains **two** `receive`-action entries, `receiveTenantOffboarded` and `receiveTenantMembershipRemoved` (added Phase 3). No `send`-action operation exists anywhere in the file — that half of the invariant still holds exactly. | **Zero-send half confirmed; one-receive-operation half is a deliberate, documented deviation (see TAC-EVT-2).** |

## `TenderAssigneeOverridden` — confirmed correctly left behind in `iam-org-membership`

`internal/adapter/outbound/eventbus/schemas/TenderAssigneeOverridden.json` (in O&M) is I-13's
event, emitted from `MembershipService.ValidateAndEmitAssigneeOverride` — called by
`internal_handler.go`'s `AssigneeOverride` method. This is a completely separate write path from
`tender_acl_entries`: it does not read, write, or reference the ACL table at all; it emits an event
about *who is assigned as a tender's owner*, not about *who has ACL access to a tender*.

This repository (`iam-tender-acl`) **neither emits nor consumes** `TenderAssigneeOverridden`:
- No schema file for it exists anywhere in this repo.
- `internal/adapter/inbound/consumer/offboarding_consumer.go` only recognizes `event_type == "TenantOffboarded"`;
  `internal/adapter/inbound/consumer/member_removal_consumer.go` (new, Phase 3) only recognizes
  `event_type == "TenantMembershipRemoved"`. Neither recognizes `TenderAssigneeOverridden`.
- `api/asyncapi.yaml` has no channel, message, or schema referencing it.

This matches the LLD's own explicit framing (§3, §10.2): "the only event ever associated with the
`tender_acl` domain in the wider system is `TenderAssigneeOverridden`, which is unrelated to
`tender_acl_entries`... and stays behind in `iam-org-membership`." Confirmed correct — no action
needed in either repository regarding this event.

## Consumer-compatibility table

| Producer | Consumer | Expected relationship per LLD | Verified against this service |
|---|---|---|---|
| `iam-org-membership` (existing `TenantOffboarded` relay — the same event already consumed elsewhere in the platform for other tenant-lifecycle cleanup) | `iam-tender-acl`'s `consumer.OffboardingConsumer (internal/adapter/inbound/consumer)` | New subscriber added to an existing relay, no change to the producer's schema or publish path (LLD §10.1/§7.6.4 — replaces the lost `fk_tae_tenant ON DELETE CASCADE`) | **Compatible.** This service is a pure additional consumer — it does not alter, version, or require any change to O&M's existing `TenantOffboarded` publish path. `api/asyncapi.yaml`'s `x-consumer-schema-dependency` block documents this repo's dependency on the producer's payload shape explicitly, since there is no Glue Schema Registry integration (LLD §10.4, explicit documented exemption) to catch a breaking upstream change automatically. |
| `iam-org-membership` (new `TenantMembershipRemoved` event, ADR-0007 Wave 3 Phase 3) | `iam-tender-acl`'s `consumer.MemberRemovalConsumer (internal/adapter/inbound/consumer)` | **Not** in the original LLD's event catalogue at all — this is genuinely new work this task added, not a pre-existing relay this service subscribed to (see `TAC-EVT-2` above). | **Compatible, but governance-incomplete — see the note below the table.** The producer (`RemoveUser`, O&M) and consumer (`MemberRemovalConsumer`, this repo) were built and integration-tested together in the same task, so the wire shape is confirmed correct end-to-end (`test/integration/consumer_test.go`'s `TestMemberRemovalConsumer_Handle_SoftDeletesAgainstRealPostgres`). What's **not** confirmed: this event has not gone through `platform-schemagov`'s `extract`/`validate`/`register` pipeline the way every other O&M event does (see below). **Second producer added in Phase 6**: `ProvisioningService.DeleteMember` (I-5, the Keycloak `USER_DELETE` webhook cascade) was found during Phase 6 prep to have its own independent tender-ACL cascade call site that Phase 3 had missed — it now emits the identical `TenantMembershipRemoved` event/payload shape as `RemoveUser`, so the consumer side needs no changes; this is the same event type from a second call site, not a new event. See `IMPLEMENTATION_GAP_ANALYSIS.md` Discrepancy 10. |
| N/A (this service has no outbound events) | Tender Service, AuthZ Enrichment (TAC-4 callers, post-cutover) | No event relationship — these are synchronous HTTP callers of TAC-4, not event consumers | **Compatible — confirmed by absence.** Neither caller is named anywhere in this service's event catalogue — their relationship to this service is purely the synchronous request/response path documented in `ARCHITECTURE.md`'s request-flow diagram, not an event contract. |
| `iam-tender-acl` (no publisher) | Any hypothetical downstream consumer of an ACL grant/revoke event | LLD §3/§10.2 confirm no such event exists in the platform's catalogue today, and this extraction adds none | **Compatible — confirmed by design, not just by absence.** Grepped this repo for any `SNS`/publisher wiring (none exists, TAC-EVT-1) and confirmed the LLD's own text states this extraction introduces no new event type for grant/revoke, preserving O&M's "no bus event for ACL writes" posture unchanged. |

## Governance gap — `TenantMembershipRemoved` has not gone through `platform-schemagov`

**This is a genuine, incomplete item, not something to gloss over.** `iam-org-membership`'s own CI
(`validate-test.yml`'s "Event schema sync check") treats `api/asyncapi.yaml` as the single source of
truth for event payload schemas: `internal/adapter/outbound/eventbus/schemas/*.json` files are
meant to be **generated** from it via `platform-schemagov extract --asyncapi api/asyncapi.yaml
--schema-dir internal/adapter/outbound/eventbus/schemas --check`, not hand-written.

What this task actually did for the new event:
- Added `EventTenantMembershipRemoved`/`TenantMembershipRemovedPayload` to O&M's Go domain code.
- **Hand-wrote** `internal/adapter/outbound/eventbus/schemas/TenantMembershipRemoved.json` directly
  (matches `ValidatingCodec`'s `//go:embed schemas/*.json` loading, which auto-discovers any file
  present — confirmed this makes runtime payload validation work correctly, per
  `test/unit`/`test/postgres` passing).
- Did **not** add a corresponding entry to O&M's own `api/asyncapi.yaml` (a large, ~2550-line file
  with an intricate existing structure — multiple SNS-fan-out channel subscriptions, envelope/
  payload schema pairs, filter-policy blocks per existing membership event — that a careful,
  correct addition deserves dedicated attention rather than a rushed patch appended at the end of
  this task).
- Attempted to run `platform-schemagov` locally to at least check whether the hand-written JSON
  passes validation — **could not**: `docker pull ghcr.io/bcbp-solutions-fzc-llc/platform-schemagov:0.4`
  failed with `unauthorized` (private GHCR image, no credentials available in this environment).

**Net effect**: the event works correctly end-to-end today (verified by real integration tests
against real Postgres), but it has not been run through this platform's actual schema-governance
tooling, and O&M's own `api/asyncapi.yaml` does not yet document it. Whether O&M's CI would
currently pass or fail its "Event schema sync check" for this new file is **unverified** — the
`--check` flag's exact diffing semantics against an asyncapi.yaml that doesn't mention the new
event were not inspected closely enough to state confidently either way. **This is real,
outstanding work for whoever picks up Phase 3's deployment**, not a hidden problem — flagged here
explicitly rather than assumed away.

## Forward-looking constraint

If a genuine event need arises here in the future (e.g. a downstream service wanting to react to
ACL grants/revokes in near-real-time rather than polling TAC-1/TAC-4), it must re-enter the normal
`schema-gov` pipeline (extract → validate → register) and receive a proper event-catalogue entry
like every other IAM event — no ad hoc event type may bypass that governance. (`TenantMembershipRemoved`
above is exactly the case where this task did NOT fully follow that rule — see the governance-gap
note directly above; this paragraph states the standing rule that gap needs to be closed against.)
Given this service's explicitly disposable nature (slated for Wave-4 reabsorption into the Tender
Service, LLD §22), any such future event is more
likely to be designed as part of that merged service than added here.
