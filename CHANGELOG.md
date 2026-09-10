# Changelog

All notable changes to this project are documented in this file. Format based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

This service has never been deployed and has no tagged release yet — everything below describes
the **current state** of `main`, not a history of prior releases. Once a `v1.0.0` ships, future
entries will describe what actually changed between tags (see [VERSIONING.md](VERSIONING.md)).

## [Unreleased]

### Added

Extracted from `iam-org-membership` per ADR-0007 Wave 3 — the tender-scoped ACL overlay
(`tender_acl_entries`), interim pending a Wave 4 merge into the Tender Service.

- **TAC-1** `GET /api/v1/tenants/:id/tenders/:tender_id/acl` — admin-gated list, paginated
  (`?limit=`/`?offset=`, default 100, max 500).
- **TAC-2** `POST /api/v1/tenants/:id/tenders/:tender_id/acl` — admin-gated grant. Blocks on a
  synchronous membership-existence check against `iam-org-membership`; fails **closed**
  (`503 core_unavailable`) if that check can't be performed.
- **TAC-3** `DELETE /api/v1/tenants/:id/tenders/:tender_id/acl/:user_id` — admin-gated revoke
  (soft-delete), optimistic-locked on the caller's last-read `record_version`
  (`409 optimistic_lock_conflict` on mismatch).
- **TAC-4** `GET /internal/tenants/:id/tenders/:tender_id/acl/:user_id` — mesh-only (mTLS, no
  role/JWT check) authorization check. Never returns `404`; a missing grant is a valid, cacheable
  `has_access:false` answer. Cached in Valkey (`tac:acl:{tenant}:{tender}:{user}`, 30s TTL).
- Row-Level Security on `tender_acl_entries`, enforced at the database layer as a backstop.
- Two independent inbound SQS consumers, each idempotency-gated via a `processed_events` ledger:
  tenant-offboarding cascade-delete (`TenantMembershipsPurged`) and per-user-removal cascade
  soft-delete (`MembershipRevoked`, ADR-0007 Wave 3 Phase 3).
- `GET /swagger` and `GET /asyncapi`/`GET /asyncapi.yaml` — REST and event contract doc viewers,
  gated by `DOCS_ENABLED`/`DOCS_AUTH_TOKEN` outside dev.
- Logs, metrics, and tracing via `platform-gincommon`; database access via `platform-pgcommon`
  (RLS GUC injection, connection-error classification, migrations); SQS consumers via
  `platform-events` — the same shared libraries every sibling IAM service uses.
- Clean Architecture / Ports-and-Adapters package layout
  (`internal/core/{domain,port,service}` + `internal/adapter/{inbound,outbound}`), matching every
  sibling IAM service.
- CI/CD: build, unit/integration/RLS/e2e test suites, lint, vulnerability scanning, and a real
  smoke test that boots the built image against real Postgres/Valkey/LocalStack and exercises
  `/readyz`/`/healthz`/TAC-4. Coverage gate at 85% (real merged coverage ~92%).
- Helm chart (`deploy/helm/tender-acl/`): one `Deployment`, no CronJobs — a single binary runs the
  HTTP server and both SQS consumers in-process.
- ADR-0007 Wave 3 cutover against `iam-org-membership` executed: its P-21/P-22/P-23/I-12 code path
  removed, `iam-authz-enrichment` repointed to call TAC-4 directly, and `tender_acl_entries`/
  `tender_acl_level` dropped from `iam-org-membership`'s schema — this service is now the sole
  system of record for tender ACL data.

### Known gaps

- `MembershipRevoked` hasn't been run through `iam-org-membership`'s `platform-schemagov`
  governance pipeline yet, and that repo's own `api/asyncapi.yaml` doesn't document it.
- `deploy/helm/tender-acl/Chart.yaml`'s `version` (`0.1.0`) and `appVersion` (`1.0.0`) are
  inconsistent — resolve both to match the first real tag before cutting one (see
  [VERSIONING.md](VERSIONING.md)).
- `go-arch-lint check` currently fails: `internal/adapter/inbound/http/asyncapi.go` imports `api/`
  without a declared component, and `api/embed.go` is unattached to any component in
  `.go-arch-lint.yml`.
- Nothing here has been deployed to, or soaked against, a real staging/production environment —
  verified only against testcontainers-backed test suites.
