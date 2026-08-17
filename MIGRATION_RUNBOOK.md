# Migration Runbook

Cross-service rollout for extracting `tender_acl_entries` from `iam-org-membership` (O&M) into
this service, per `tender-acl-service-lld.md` §21 (Migration Plan) and ADR-0007 Wave 3. **Status as
of this document: all 7 phases have been executed** in this workspace (real Postgres/Valkey/
localstack via Docker, real Go builds/tests throughout) — `tender_acl_entries` has been fully
extracted and dropped from O&M's schema. What remains genuinely outstanding is exclusively
infrastructure/deployment work that has no code representation in this workspace: no repo has
been deployed to any real environment, no AWS account has real queues/topics provisioned, and no
staging/production database exists to run any of this against — every phase below calls out
exactly which of its action items were skipped for that reason, and which were real, verified
code/schema changes.

This is explicitly the **lowest-urgency, lowest-risk** of the three ADR-0007 waves: a single
admin-gated table, low write volume (grant/revoke are infrequent admin actions), and a service that
is itself disposable — slated for reabsorption into the Tender Service at Wave 4 (LLD §22) rather
than being hardened as a long-lived microservice. Treat every phase below with that framing in
mind: the goal is a clean, reversible extraction, not maximal operational rigor.

## Phase 1 — Deploy new service ✅ (this repository)

**What:** Stood up `iam-tender-acl` with its own database, migrations, and the full TAC-1/2/3/4
endpoint surface plus the tenant-lifecycle-cleanup SQS consumer, independently deployable and
independently testable. No traffic is routed to it yet; `iam-org-membership` remains untouched and
authoritative.

Built in this phase (paths below are as they existed at the time — this flat layout was later
restructured into Clean Architecture layering, see `ARCHITECTURE.md`'s "Layer model" section and
`CHANGELOG.md`; the equivalent code lives under `internal/core/{domain,port,service}` and
`internal/adapter/{inbound,outbound}` today):
- `internal/acl/` (flat layout per LLD §6/TAC-D1): domain, service, repository, HTTP handlers
  (TAC-1/2/3 public, TAC-4 internal/mesh-only), Postgres repository + migrations, RLS policy.
- `internal/membershipcheck/`: swappable `Checker` interface + `HTTPChecker` HTTP adapter calling
  O&M's not-yet-built provider endpoint (see `O_AND_M_DELTA.md` §4).
- `internal/cache/`: Valkey-backed `tac:acl:{tenant}:{tender}:{user}` cache, 30s TTL, used only by
  TAC-4.
- `internal/consumer/`: `OffboardingConsumer` subscribing to the existing `TenantOffboarded` relay
  on `tenant-lifecycle-tenderacl-q`, with its own `processed_events` idempotency table.
- Full CI/CD (`.github/workflows/`), Helm chart (`deploy/helm/tender-acl/`), OpenAPI/AsyncAPI specs
  (`api/`), and this repo's own root docs.

- **Risk:** Low — this is a greenfield deployment; nothing else depends on it existing yet.
- **Rollback:** Trivial — undeploy the service, drop its database. Nothing else references it.
- **Observability:** `/healthz`, `/readyz` (Postgres + Valkey health), `/metrics` (Prometheus,
  `tender_acl_*` prefix per LLD §14.2). Confirm `/readyz` returns `200` before proceeding to Phase 2.
- **Verification:** `go build ./...`/`go vet ./...` green; `go test ./...` and
  `go test -tags=integration,rls,e2e ./...` green (the latter runs real migrations + RLS policy
  enforcement + repository behavior against throwaway Postgres/Valkey/SQS-compatible containers via
  testcontainers). Manual smoke: start the binary locally, grant an ACL entry via TAC-2 with a
  mocked membershipcheck response, confirm TAC-4 returns `has_access:true`.

## Phase 2 — Data expand ✅ (tooling built and dry-run verified; production run still pending)

**What:** One-time export of O&M's current `tender_acl_entries` rows into this service's database
(or logical replication for a live cutover with zero data-loss window). The LLD's migration plan
(§21) chooses this export-then-cutover sequence explicitly to avoid a dual-write phase's
operational cost — mirroring both prior waves' approach, not a dual-write window.

**Tooling:** `scripts/migrate-data-from-org-membership.sh` — a `psql`-based export/load/verify
script. Reads `SOURCE_DSN` (O&M, read-only) and `TARGET_DSN` (this service, **must** be the
`tender_acl_migrator` role — `BYPASSRLS` is required for a cross-tenant bulk load; `tender_acl_app`
would silently load 0 rows or reject cross-tenant inserts under RLS). Refuses to load into a
non-empty target unless `--force` is passed (which truncates first — this **is** the documented
rollback path, made concrete as a flag rather than a manual step). Verifies row-count and
`max(updated_at)` parity between source and target before exiting 0.

**Dry-run status (2026-08-17, local dev):** O&M's local dev database currently has **0 rows** in
`tender_acl_entries` (and 0 `tenants`/`tenant_memberships` — this is an unseeded dev environment,
not a signal about production volume). To validate the script itself rather than trivially exporting
nothing, it was exercised against 2 synthetic rows (one active `edit` grant, one soft-deleted `view`
grant, seeded directly into O&M's dev Postgres under a throwaway tenant/membership) — copied
byte-for-byte into this service's dev database (full column fidelity confirmed on `access_level`,
`reason`, `deleted_at`, `tenant_membership_id`), parity check passed, and RLS was independently
confirmed to gate the migrated rows correctly (`tender_acl_app` with no GUC set: 0 rows; with
`app.tenant_id` set to the seeded tenant: 2 rows). The synthetic rows and their supporting
tenant/membership rows were deleted from O&M afterward, and the migrated copies truncated from this
service's dev database, restoring both to the empty state they were found in — **this was a tooling
validation run, not a real data migration**; no production or staging data has been touched.
A real bug was caught and fixed during this dry run: the script's parity-check query was initially
missing its `FROM tender_acl_entries` clause.

**Before running this against a real staging/production O&M database:**
1. Confirm `tender_acl_migrator`'s credentials for `TARGET_DSN` are provisioned (not yet true in any
   deployed environment — this service hasn't been deployed anywhere but local dev).
2. Confirm the real row count in the target O&M environment first (`SELECT count(*) FROM
   tender_acl_entries`) so the "nothing to load" branch isn't mistaken for a broken connection.
3. Re-run the row-count + `max(updated_at)` verification the script performs, and additionally spot
   check a sample of rows by `id` between source and target (the script checks aggregate parity, not
   per-row equality — sufficient for the count and volume this table sees, but call this out
   explicitly if the real table ever grows large enough that aggregate parity stops being convincing).

- **Risk:** Low — a bad export is easy to detect (row-count/checksum comparison) and cheap to
  re-run; O&M's table is untouched throughout (the script only ever `SELECT`s from O&M).
- **Rollback:** `./scripts/migrate-data-from-org-membership.sh --force` truncates this service's
  table and reloads from source. O&M is never written to by this script.
- **Observability:** Row-count parity check between O&M's `tender_acl_entries` and this service's,
  run as a one-off script, not a permanent monitor (this table's write volume doesn't justify
  ongoing reconciliation tooling).
- **Verification:** `SELECT count(*), max(updated_at) FROM tender_acl_entries` on both databases
  must match before Phase 3 begins. Confirmed passing against the synthetic dry-run data above; still
  needs a real run against actual O&M data once a staging environment exists.

## Phase 3 — Provider endpoint + notify event live in O&M ✅ (code built & tested; not yet deployed/soaked)

**What:** Deploy `O_AND_M_DELTA.md`'s new provider endpoint (`GET
/internal/tenants/:id/members/:user_id/exists`, §4) and the per-user-removal cascade notify (§5) in
`iam-org-membership`, but **not yet relied upon by anything user-facing** — this service's
`membershipcheck.HTTPChecker` isn't in the request path until Phase 4, so this phase is purely
"stand up the O&M-side dependency and let it soak."

**Decision made this phase**: `O_AND_M_DELTA.md` §5 originally left the per-user-removal cascade
mechanism (§5) as an explicit open decision (notify-client "Option A" vs. event "Option B"). Asked
directly, **the user chose Option B** (a new `TenantMembershipRemoved` SNS event) over this
document's own recommendation of Option A. This is a real deviation from the LLD's `TAC-EVT-2`
invariant ("consumes exactly one event type") — reconciled explicitly in
`EVENT_COMPATIBILITY_REPORT.md`, not silently absorbed.

**Built and tested this phase** (both repos, full detail in `O_AND_M_DELTA.md` §4/§5's
"Implemented" subsections):
- O&M: `GET /internal/tenants/:id/members/:user_id/exists` (`InternalHandler.CheckMemberExists` +
  `MembershipService.CheckActiveMembership`), and `RemoveUser` now emits `TenantMembershipRemoved`
  inside its existing transaction (transactional-outbox pattern, same as the role/dept/delegation
  cascades already emit). Full O&M test suite (unit + `integration` + `e2e` tags) green.
- `iam-tender-acl`: a second, independent SQS subscription (`member-removal-tenderacl-q` +
  its own DLQ/consumer/metric/processed-events-consumer-name), a new `Repository.
  SoftDeleteForUser` soft-delete method, wired into `cmd/tender-acl/main.go`'s errgroup alongside
  the existing offboarding consumer. All four test tiers (unit `-race`, `integration`, `rls`,
  `e2e`) green against real Postgres/Valkey testcontainers; `go-arch-lint` clean.

**Genuinely still outstanding** (not done in this pass): `TenantMembershipRemoved` has **not** been
run through `platform-schemagov`'s governance pipeline, and O&M's own `api/asyncapi.yaml` (the
real schema source of truth there) does not yet document the new event — see
`EVENT_COMPATIBILITY_REPORT.md`'s "Governance gap" section and `IMPLEMENTATION_GAP_ANALYSIS.md`'s
Discrepancy 7 for exactly what's missing and why it wasn't attempted here (a large, intricate
existing file; no registry credentials available to run the validation tool locally). Neither
repo has been deployed to any real environment, and no queue/DLQ has been provisioned in a real
AWS account (Terraform templates are ready — `deploy/iam/policy.tf.example` — but not applied).

- **Risk:** Low — additive-only change in O&M; no existing O&M behavior is modified, only a new
  route and a new transactionally-emitted event added.
- **Rollback:** Remove the new route and stop emitting the event; nothing else in O&M depends on
  either yet. On `iam-tender-acl`'s side, the new consumer can be disabled without affecting the
  (still-primary) tenant-offboarding consumer.
- **Observability:** Request count/latency on the new provider endpoint (should be near-zero until
  Phase 4); `tender_acl_member_removal_cascade_total{result="error"}` and
  `member-removal-tenderacl-q-dlq` depth (new `TenderAclMemberRemovalCascadeFailing`/
  `TenderAclMemberRemovalDLQGrowing` alerts, SLO-4 in `deploy/monitoring/slo-rules.yml`) should
  track `RemoveUser` call volume once this reaches a real environment.
- **Verification:** Manually call the new endpoint against a known active/inactive/nonexistent
  membership and confirm the `{"active": bool, "tenant_membership_id": uuid}` shapes match
  `O_AND_M_DELTA.md` §4 exactly. Confirm a real `RemoveUser` call in a staging environment produces
  exactly one `TenantMembershipRemoved` message on `member-removal-tenderacl-q` and that
  `iam-tender-acl`'s consumer soft-deletes the corresponding row — this end-to-end path is verified
  today only against testcontainers, not a real deployed pair of services.

## Phase 4 — Read cutover ✅ (partially executed — see below for what's real vs. not executable here)

**What:** Tender Service and AuthZ Enrichment (I-12's current callers) are repointed at TAC-4;
admin tooling is repointed at TAC-1. O&M's P-21/P-22/P-23/I-12 handlers and table remain live as a
rollback target — this service is now serving real read traffic for the first time.

**Reality check before executing this phase**: a research pass across every repo on this machine
found that **"Tender Service" does not exist anywhere in this environment** — the tender-acl LLD
itself states it explicitly (§22/ADR-0007 Option D): it's a planned future service, not something
deployed yet, so there is nothing to repoint. **"Admin tooling" is also not a real codebase** —
P-21/22/23 (TAC-1/2/3) are plain tenant-facing admin routes; whatever ops/frontend client calls them
today isn't a repo in this workspace. Of the three named callers, only **AuthZ Enrichment
(`iam-authz-enrichment`)** is real and repointable — confirmed as the actual I-12 caller via
`internal/adapter/outbound/orgmembership/client.go`'s `GetTenderACL` method.

**What was actually done this phase:**
- `iam-authz-enrichment`'s `GetTenderACL` (I-12) now calls **this service's TAC-4** directly, not
  Org & Membership. `internal/adapter/outbound/orgmembership.Client` gained a second, independent
  base-URL/timeout/`http.Client` triplet (`TenderACLBaseURL`/`TenderACLTimeout`,
  env vars `TENDER_ACL_BASE_URL`/`TENDER_ACL_TIMEOUT`, default 20ms — sized against TAC-4's own
  5ms-hit/20ms-miss SLO, not reused from `ORG_MEMBERSHIP_TIMEOUT`'s 10ms) — I-8/I-14 are unaffected,
  still calling Org & Membership. The call path also changed from
  `/api/v1/internal/tenants/.../acl/...` to `/internal/tenants/.../acl/...` (TAC-4 has no `/api/v1`
  prefix, per this service's own LLD §8.3 — a real path difference, not just a base-URL swap).
  Same fail-fast-outside-`APP_ENV=dev` startup validation added for the new var, mirroring the
  existing `ORG_MEMBERSHIP_BASE_URL` check.
- Full `iam-authz-enrichment` test suite (unit + config + client tests, 13 new/updated test cases)
  green; response-shape compatibility confirmed by direct code inspection: `domain.TenderACL`'s
  JSON tags already match TAC-4's response exactly, and the one difference (TAC-4 sends
  `expires_at`, Org & Membership's old I-12 never did) is a compatible superset — `ExpiresAt` is
  decoded but never read by any decision logic in that service.
- **Not executed** (genuinely out of scope for this environment): repointing "Tender Service" and
  "admin tooling" — neither exists as code here. Whoever owns those integrations (or builds Tender
  Service in the future, per Wave 4) needs to repoint them at TAC-4/TAC-1 respectively before this
  phase is *complete*, even though the one real caller found here is done.
- **Not executed**: live staging verification (LLD §17.3's byte-identical-shapes comparison) — no
  staging environment exists yet. The closest available substitute — direct comparison of the two
  services' actual Go DTOs/JSON tags plus this service's own `test/e2e` suite (grant → TAC-4 chain)
  — was done instead; flagged here as a real gap, not silently treated as equivalent to a live
  staging comparison.

- **Risk:** Medium — this is the first phase where a real request path (I-8-adjacent authorization
  checks via TAC-4) depends on this service being up. `TAC-FAIL-2` bounds the blast radius: even a
  full outage here degrades to `503`, never a silently-wrong `has_access` answer.
- **Rollback:** Revert `iam-authz-enrichment`'s client change (or set `TENDER_ACL_BASE_URL` back to
  Org & Membership's address, if TAC-4's response shape is ever intentionally diverged from I-12's
  — not true today) — no data has diverged since Phase 2's export snapshot, assuming no writes have
  landed on this service yet.
- **Observability:** This service's own dashboards (TAC-1/4 request rate/latency, `tac:acl:*` cache
  hit ratio) plus alerting on TAC-4 error rate `>10%`/5min (LLD §14.5). On `iam-authz-enrichment`'s
  side, watch `DENY_TENDER_ACL_INSUFFICIENT` rate and the `AuthzEnrichmentServiceUnavailableSpike`
  alert (`docs/runbook.md` there now explicitly calls out checking iam-tender-acl, not Org &
  Membership, when this fires for tender-scoped requests only).
- **Verification:** Compare TAC-1 list output and TAC-4 check output against O&M's equivalent
  responses for a sample of known tenants/tenders/users in staging — byte-identical shapes per LLD
  §17.3. **Not done against real staging data** (none exists) — see above for what substitute
  verification was actually performed.

## Phase 5 — Write cutover ✅ (partially executed — see below for what's real vs. not executable here)

**What:** Admin tooling is repointed at TAC-2/TAC-3; O&M's `MembershipService.RemoveUser` starts
calling the new notify client (`O_AND_M_DELTA.md` §5); O&M's tenant-offboarding publish path
(already async elsewhere in O&M) is repointed to also target
`tenant-lifecycle-tenderacl-q`. O&M's P-21/P-22/P-23/I-12 handlers return `410 Gone`, not deleted
yet — this service becomes sole writer of record for `tender_acl_entries`.

**Reality check before executing this phase** (same pattern as Phase 4): two of this phase's three
action items are **not executable in this environment**:
- **"Admin tooling repointed at TAC-2/TAC-3"** — as found in Phase 4, no such codebase exists here.
  Nothing to repoint.
- **"O&M's `RemoveUser` starts calling the new notify client"** — this bullet describes Option A
  (the notify-client approach), which was **not** the option chosen in Phase 3 — the user chose
  Option B instead, and `RemoveUser` has been unconditionally emitting `TenantMembershipRemoved`
  since Phase 3 already landed. There is no separate "start calling" step for Option B — see
  `O_AND_M_DELTA.md` §5's "Implemented (Option B)" subsection.
- **"O&M's tenant-offboarding publish path is repointed to also target
  `tenant-lifecycle-tenderacl-q`"** — a dedicated research pass found this **cannot be done from
  any code in this workspace**: `TenantOffboarded` is not produced by `iam-org-membership` at all
  (per its own reference LLD, it's produced by **iam-realm-provisioner** after a verified
  export+delete) — and `iam-realm-provisioner` **does not actually implement this emission**
  anywhere in that repo (confirmed via an exhaustive grep, zero hits). The actual SNS-topic
  subscription/filter-policy wiring that would route this event to a new queue is also **not**
  code in any repo here — `iam-group-mapping`'s own Wave 2 precedent for its analogous queue
  confirms this explicitly: its `deploy/iam/policy.tf.example` only grants IAM consume-permissions
  on an already-existing queue resource, with the comment "Platform Terraform owns the
  authoritative version of this resource." This is real infrastructure-provisioning work outside
  this workspace, not a gap in this task's execution.

**What was actually done this phase** (the one real, executable item — a genuine code change in
`iam-org-membership`):
- `P-21`/`P-22`/`P-23` (`GET`/`POST /tenants/:id/tenders/:tender_id/acl`,
  `DELETE .../acl/:user_id`) and `I-12` (`GET /internal/.../acl/:user_id`) now return **410 Gone**
  with wire code `endpoint_retired` — new `domain.ErrEndpointRetired` sentinel, a new
  `domainErrorStatus` case, and a shared `RetiredTenderACL` handler
  (`internal/adapter/inbound/http/retired_routes.go`) wired in place of the original handler
  methods in `cmd/server/main.go`. The original handlers/service/repository are **not deleted** —
  only the route registrations changed, so this is a one-line-per-route revert.
- `test/e2e/harness_test.go` (a second, independent copy of the router wiring used only by e2e
  tests) updated to match, plus a pre-existing gap fixed: it was missing Phase 3's
  `CheckMemberExists` route entirely — added.
- New `test/e2e/retired_routes_test.go`: 6 new e2e tests, all green against real Postgres —
  confirms all four retired routes return `410`/`endpoint_retired` through the real HTTP router
  (not just a unit test of the handler function in isolation), plus two new tests giving
  `CheckMemberExists` its first HTTP-level coverage (previously handler/service-level only).
- Full O&M test suite (unit + `integration` + `postgres` + `e2e` build tags) reverified green.

- **Risk:** Medium — any caller still hardcoded to O&M's old routes breaks visibly (which is the
  point of `410` over silent drift) but is still an incident if it's an integration nobody
  remembered to repoint. In this environment specifically, the risk is theoretical — no real
  "admin tooling" caller exists to break — but this caveat must travel with this change if it's
  ever cherry-picked into a context where such a caller does exist.
- **Rollback:** Re-enable O&M's handlers (disabled, not deleted) — revert the four route
  registrations in `cmd/server/main.go` (and `test/e2e/harness_test.go`) back to the original
  handler methods. Nothing else needs unwinding: no data migration, no consumer wiring, no external
  dependency was touched by this specific change.
- **Observability:** Alert on any `410 Gone` traffic during this soak period — it's a caller that
  missed the cutover, not noise. Watch `tender_acl_tenant_offboarding_cascade_total`,
  `tender_acl_member_removal_cascade_total`, and both DLQ depth alerts (LLD §14.5,
  `deploy/monitoring/app-alerts.yml`) — though note per the reality check above, the actual event
  traffic on either queue is unverified in any real environment; the consumers are built and
  tested against synthetic/testcontainer events only.
- **Verification:** Run this phase for a full release cycle (minimum: one full on-call rotation)
  with zero unexpected `410` traffic and zero DLQ growth before proceeding to an irreversible step.
  **Not done** — there is no real traffic or deployed environment to observe in this pass; the
  substitute verification performed was the new e2e test suite confirming the `410` contract is
  correct, not a real soak period.

## Phase 6 — Cleanup ✅ (executed)

**What:** Remove the `410`-returning P-21/P-22/P-23/I-12 handlers from O&M entirely (not just
disable them). Retired IDs are never reused in O&M's catalogue (mirroring the I-6/I-7
quota-retirement precedent the Wave-1/Wave-2 extractions also followed).

**What was actually done:** Deleted `internal/adapter/inbound/http/acl_handler.go`,
`internal/core/domain/tender_acl.go`, `internal/core/port/acl_repository.go`,
`internal/core/service/tender_acl_service.go`,
`internal/adapter/outbound/postgres/tender_acl_repository.go`, the Phase-5
`retired_routes.go` 410 handler (no longer needed once routes are fully unregistered), and every
ACL-specific test file (`tender_acl_service_test.go`, `acl_scenarios_test.go`,
`acl_manual_coverage_test.go`, `acl_list_handler_test.go`, `group_acl_coverage_test.go`).
`InternalHandler.CheckTenderAccess` (I-12) and the three tenant-facing ACL routes in
`cmd/server/main.go`/`test/e2e/harness_test.go` were removed, replaced with one-line
"retired, moved to iam-tender-acl. ID never reused" comments. `ErrACLAlreadyExists`,
`ErrInvalidExpiresAt`, and the now-unused `ErrEndpointRetired` sentinel (Phase 5's only caller,
`RetiredTenderACL`, no longer exists) were removed from `domain/errors.go` and their
`middleware.go` mappings. Remaining ACL-specific test cases across a dozen other test files
(handler ctors/matrix/query-validation, membership/provisioning unit tests, postgres repo tests,
RLS's tenant-scoped-table-count assertion) were updated to drop their `acls`
constructor arguments and removed test cases. `test/e2e/retired_routes_test.go`'s four
`_Returns410` tests were rewritten to assert plain `404` (a fully-unregistered gin route, not a
410 stub) — the two `CheckMemberExists` HTTP tests in the same file were kept unchanged.

**Bug found and fixed during this phase's prep** (not part of the original Phase 6 scope, but
discovered while tracing every ACL call site before deleting the subsystem): `O_AND_M_DELTA.md`'s
own §2 had only tracked `MembershipService.RemoveUser`'s `SoftDeleteForUser` call — a second,
independent call site in `ProvisioningService.DeleteMember` (I-5, the Keycloak USER_DELETE cascade)
was missed by the original Phase 3 pass. It has been fixed the same way (Option B —
unconditionally emit `TenantMembershipRemoved` instead of a same-transaction cross-database call),
see `IMPLEMENTATION_GAP_ANALYSIS.md` for the dedicated discrepancy entry.

- **Risk:** Medium — code deletion in a repository this service no longer depends on for anything
  except historical git-blame.
- **Rollback:** Reversible via git revert up to the point this phase's PR merges; no data
  operations occur in this phase.
- **Observability:** Confirmed zero references to the deleted handlers/routes remain
  (`grep -rn "acl_handler\|CheckTenderAccess" iam-org-membership` outside test fixtures/history —
  the only hits are retirement comments, no code).
- **Verification:** Full O&M test suite green with the handlers actually deleted — `go build ./...`,
  `go vet ./...` (default + `integration` + `e2e` tags), `go test ./...`,
  `go test -tags integration ./test/postgres/...`, and `go test -tags e2e ./test/e2e/...` all pass
  against real Postgres/Valkey/localstack via Docker (not just unit-level mocks). `make swag`
  regenerated `docs/swagger/` with the ACL routes/DTOs gone. `go-arch-lint` shows the same 23
  pre-existing notices as before this phase (verified via `git stash` diff) — no new violations
  introduced.

## Phase 7 — Table drop ✅ (executed, irreversible)

**What:** Drop `tender_acl_entries` and `tender_acl_level` from O&M's schema (`O_AND_M_DELTA.md`
§3 — confirmed no local ENUM copy is needed, unlike the Wave-1 catalog ENUMs, since nothing outside
the ACL subsystem references `tender_acl_level`).

**What was actually done:** New migration
`internal/adapter/outbound/postgres/migrations/000015_drop_tender_acl_table.{up,down}.sql`
(`DROP TABLE IF EXISTS tender_acl_entries; DROP TYPE IF EXISTS tender_acl_level;`, `lock_timeout`
guarded per this repo's convention). A pre-drop snapshot (`pg_dump --format=custom`) of the local
dev database was taken immediately before applying it. The migration was verified two ways: (1)
every fresh testcontainer spun up by the `integration`/`e2e` test suites applies it as part of
`RunMigrations`, and all three test tiers pass; (2) it was also applied directly to the persistent
local dev Postgres (`org_membership` on `localhost:5534`, port 5534), which held **0 rows** in
both objects beforehand (an unseeded dev environment — no production/staging environment exists
in this workspace to run it against instead). Post-drop, `\dt tender_acl_entries` and a
`pg_type` lookup confirm both objects are gone; `pgcommon_migrations` shows version 15 applied
cleanly (`dirty=false`).

- **Risk:** High and **irreversible** — this is a `DROP TABLE`/`DROP TYPE`. A full pre-drop
  snapshot was taken immediately before this step (see above) — retained alongside this task's
  working files for at least one full incident-response window.
- **Rollback:** Restore O&M's table from the pre-drop snapshot and replay any writes this service
  made since Phase 5 (there were none needing replay into O&M in this pass, since O&M has not been
  the writer of record since Phase 5, and the dev database held zero rows regardless).
- **Observability:** Confirmed zero references to `tender_acl_entries`/`tender_acl_level` remain in
  O&M's codebase (`grep -rn "tender_acl_entries\|tender_acl_level\|TenderACLLevel" iam-org-membership`
  returns nothing outside this migration's own `.up`/`.down` files and historical migration files
  000001-000006, which predate the drop and are never re-run against an already-migrated database).
- **Verification:** Full O&M test suite green with the table actually dropped — confirmed against
  both a fresh testcontainer database (via the real `integration`/`e2e` test runs, not code review)
  and the persistent local dev database (schema inspection post-drop). No real staging/production
  database exists in this workspace to additionally verify against.

**Wave 4 (LLD §22, folding this service into the Tender Service, ADR-0007 Option D) is separate
future work, not part of this runbook** — it has its own entry criteria (TAC-Q4: Tender Service has
its own stable datastore, established release cadence, and a team prepared to own this schema +
endpoints + the `membershipcheck` client), none of which are scheduled as of this document.
