# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 1.x     | ✅ Active |

Older major versions are not patched. Deploy the latest 1.x image. This service is itself interim (ADR-0007 Option D — scheduled to merge into the Tender Service, see `MIGRATION_RUNBOOK.md` §Wave 4); "supported" means supported until that merge, not long-term.

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Email: vijay@bcbpsolutions.com
Subject: `[iam-tender-acl] Security vulnerability`

Include in your report:
- Description of the vulnerability and the affected component (HTTP handler, RLS policy, membership-check client, tenant-offboarding consumer, cache, etc.)
- Steps to reproduce
- Potential impact (RLS bypass, tenant data leakage, unauthorized ACL grant/revoke, privilege escalation, DoS, etc.)
- Suggested fix or patch (if any)

### Response timeline

| Step | Target |
|------|--------|
| Initial acknowledgement | 48 hours |
| Severity assessment | 5 business days |
| Patch release (critical/high) | 14 days |
| Public disclosure | After patch ships |

We follow responsible disclosure. Reporters will be credited in release notes unless anonymity is requested.

## Scope

Areas of particular sensitivity in this service:

- **Row-Level Security (RLS)** — tenant isolation for `tender_acl_entries` is enforced entirely at the Postgres layer via `FORCE ROW LEVEL SECURITY`; a bypass would expose or let one tenant grant/revoke/view another tenant's tender ACL entries. Unlike the source `iam-org-membership` table this was extracted from, this service deliberately does **not** carry over the `rls_check_tenant()`/`rls_violation_log` forensic-logging wrapper (TAC-D8) — RLS enforcement itself is unchanged, only the audit-trail-on-violation behavior was intentionally dropped.
- **GUC binding under connection pooling** — `app.tenant_id` is bound transaction-locally via `pgcommon.WithTenantTx` (`SELECT set_config('app.tenant_id', $1, true)`). An incorrect or leaked value under PgBouncer-style connection reuse would allow cross-tenant access; see `test/rls/` for the fail-closed and no-leak test cases this guards against.
- **TAC-4 internal authorization-check endpoint** — `GET /internal/tenants/:id/tenders/:tender_id/acl/:user_id` is mesh-only mTLS with no JWT validation by design, and is on a **live authorization decision path** (Tender Service and AuthZ Enrichment call it to decide access). Any exposure of this route beyond the mesh, or any bug that returns `has_access:true` incorrectly, is a critical finding.
- **Grant-time membership check (fail-closed)** — TAC-2 (grant) calls `iam-org-membership`'s `GET /internal/tenants/:id/members/:user_id/exists` synchronously before writing a new ACL entry. This client must **fail closed**: if the call errors or times out, the grant is blocked (`503 core_unavailable`), never defaulted to allow. A regression that flips this to fail-open would let ACLs be granted to non-members.
- **Tenant offboarding cascade** — the `TenantOffboarded` consumer (`tenant-lifecycle-tenderacl-q`) deletes all of a tenant's ACL entries. An attacker able to forge or replay this event for an arbitrary `tenant_id` could destroy another tenant's ACL grants; idempotency (`processed_events`) is a correctness/dedup mechanism, not an authentication boundary — the authentication boundary is the SQS queue's IAM policy (see `deploy/iam/`).
- **Optimistic-lock discrepancy on revoke** — see `IMPLEMENTATION_GAP_ANALYSIS.md`: the LLD describes a `record_version`-checked `409 optimistic_lock_conflict` on TAC-3, matching this implementation; historically the source `iam-org-membership` code this was extracted from had no such check. Do not silently reintroduce the source's weaker behavior.
