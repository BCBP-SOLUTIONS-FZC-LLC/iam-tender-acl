# Changelog

All notable changes to this project are documented in this file. Format based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

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

- TAC-3 (Revoke) does not implement the `record_version`-gated optimistic-lock check the LLD
  specifies (§12.1) — preserves `iam-org-membership`'s current unconditional-update behavior
  unchanged, per explicit instruction. The column and trigger bump exist; nothing gates on them yet.
- **`TAC-EVT-2` LLD deviation**: this service now consumes **two** event types, not the one the LLD
  specifies — see `EVENT_COMPATIBILITY_REPORT.md`'s reconciled invariant.
- The new `TenantMembershipRemoved` event has not been run through `iam-org-membership`'s
  `platform-schemagov` governance pipeline, and that repo's own `api/asyncapi.yaml` does not yet
  document it (`IMPLEMENTATION_GAP_ANALYSIS.md` Discrepancy 7).
- Nothing in Phases 2–3 has been deployed to, or soaked against, any real staging/production
  environment — verified only against testcontainers-backed test suites in both repositories.
