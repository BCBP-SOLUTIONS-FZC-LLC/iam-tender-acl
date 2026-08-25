-- =============================================================================
-- mock_setup.sql — iam-tender-acl manual test seed
-- =============================================================================
-- Run BEFORE manual testing:
--   psql "postgres://tender_acl:tender_acl@localhost:5536/tender_acl" \
--        -f scripts/manual-test/mock_setup.sql
--
-- Must use the superuser / BYPASSRLS role (tender_acl or tender_acl_migrator)
-- because tender_acl_app is NOBYPASSRLS — direct inserts would be rejected
-- by the RLS policy without a valid app.tenant_id GUC.
--
-- Teardown: run mock_teardown.sql after all tests are complete.
-- =============================================================================

-- ── TEST UUID LEGEND ─────────────────────────────────────────────────────────
--
--  TENANTS
--    TENANT_A  aaaa0001-0001-0001-0001-000000000001   main test tenant
--    TENANT_B  aaaa0002-0002-0002-0002-000000000002   cross-tenant isolation
--
--  TENDERS (UUIDs only — no tender table in this DB; Tender Service owns them)
--    TENDER_1  bbbb0001-0001-0001-0001-000000000001   main tender of TENANT_A
--    TENDER_2  bbbb0002-0002-0002-0002-000000000002   second tender of TENANT_A
--
--  USERS (UUIDs only — no users table in this DB)
--    ADMIN     cccc0001-0001-0001-0001-000000000001   API caller (tender_admin)
--    USER_A    cccc0002-0002-0002-0002-000000000002   grantee: view  on TENDER_1
--    USER_B    cccc0003-0003-0003-0003-000000000003   grantee: edit  on TENDER_1
--    USER_C    cccc0004-0004-0004-0004-000000000004   grantee: approve on TENDER_1
--    USER_D    cccc0005-0005-0005-0005-000000000005   grantee: expired grant
--    USER_E    cccc0006-0006-0006-0006-000000000006   reserved: TAC-3 revoke target
--    USER_F    cccc0007-0007-0007-0007-000000000007   soft-deleted (revoked) entry
--    USER_G    cccc0008-0008-0008-0008-000000000008   TENANT_B grantee (isolation)
--
--  FAKE MEMBERSHIP IDs (audit-only, not FK-validated)
--    MEM_BASE  dddd0001-0001-0001-0001-000000000001   reused for all seed rows
--
--  NOTE: record_version DEFAULT = 1 (DB schema). All seed rows start at 1.
--        The touch_row() trigger bumps to 2 on first UPDATE (revoke).
--        TAC-3 revoke curl must send {"record_version": 1} for USER_E.
-- =============================================================================

BEGIN;

-- ── 1. TENANT_A / TENDER_1 — active grants (TAC-1 list + TAC-4 check) ───────

-- USER_A: view level  (TAC1-HP-01, TAC4-HP-01, TAC4-HP-03)
INSERT INTO tender_acl_entries
    (id, tenant_id, tender_id, user_id, tenant_membership_id,
     access_level, granted_by, reason)
VALUES (
    'eeee0001-0001-0001-0001-000000000001',
    'aaaa0001-0001-0001-0001-000000000001',
    'bbbb0001-0001-0001-0001-000000000001',
    'cccc0002-0002-0002-0002-000000000002',
    'dddd0001-0001-0001-0001-000000000001',
    'view',
    'cccc0001-0001-0001-0001-000000000001',
    'seed: view access for manual test'
);

-- USER_B: edit level  (TAC1-HP-01, TAC4-HP-03)
INSERT INTO tender_acl_entries
    (id, tenant_id, tender_id, user_id, tenant_membership_id,
     access_level, granted_by, reason)
VALUES (
    'eeee0002-0002-0002-0002-000000000002',
    'aaaa0001-0001-0001-0001-000000000001',
    'bbbb0001-0001-0001-0001-000000000001',
    'cccc0003-0003-0003-0003-000000000003',
    'dddd0001-0001-0001-0001-000000000001',
    'edit',
    'cccc0001-0001-0001-0001-000000000001',
    'seed: edit access for manual test'
);

-- USER_C: approve level  (TAC4-HP-04)
INSERT INTO tender_acl_entries
    (id, tenant_id, tender_id, user_id, tenant_membership_id,
     access_level, granted_by)
VALUES (
    'eeee0003-0003-0003-0003-000000000003',
    'aaaa0001-0001-0001-0001-000000000001',
    'bbbb0001-0001-0001-0001-000000000001',
    'cccc0004-0004-0004-0004-000000000004',
    'dddd0001-0001-0001-0001-000000000001',
    'approve',
    'cccc0001-0001-0001-0001-000000000001'
);

-- ── 2. Expired grant — NOT revoked, just past expires_at (TAC1-HP-03, TAC4-TAE-03-01) ──

-- USER_D: expired 1 hour ago — TAC-1 SHOWS it (deleted_at IS NULL),
--         TAC-4 returns has_access:false
INSERT INTO tender_acl_entries
    (id, tenant_id, tender_id, user_id, tenant_membership_id,
     access_level, granted_by, reason, expires_at)
VALUES (
    'eeee0004-0004-0004-0004-000000000004',
    'aaaa0001-0001-0001-0001-000000000001',
    'bbbb0001-0001-0001-0001-000000000001',
    'cccc0005-0005-0005-0005-000000000005',
    'dddd0001-0001-0001-0001-000000000001',
    'view',
    'cccc0001-0001-0001-0001-000000000001',
    'seed: expired grant — visible in TAC-1, false in TAC-4',
    now() - interval '1 hour'
);

-- ── 3. TAC-3 revoke target — active, record_version = 1 ─────────────────────

-- USER_E: reserved for revoke test.
-- TAC-3 curl MUST send {"record_version": 1}
INSERT INTO tender_acl_entries
    (id, tenant_id, tender_id, user_id, tenant_membership_id,
     access_level, granted_by, reason)
VALUES (
    'eeee0005-0005-0005-0005-000000000005',
    'aaaa0001-0001-0001-0001-000000000001',
    'bbbb0001-0001-0001-0001-000000000001',
    'cccc0006-0006-0006-0006-000000000006',
    'dddd0001-0001-0001-0001-000000000001',
    'view',
    'cccc0001-0001-0001-0001-000000000001',
    'seed: TAC-3 revoke target — use record_version=1 in curl'
);

-- ── 4. Soft-deleted (revoked) entry — excluded from TAC-1 list (TAC1-HP-04) ─

-- USER_F: deleted_at set → must NOT appear in TAC-1 list response
INSERT INTO tender_acl_entries
    (id, tenant_id, tender_id, user_id, tenant_membership_id,
     access_level, granted_by, deleted_at)
VALUES (
    'eeee0006-0006-0006-0006-000000000006',
    'aaaa0001-0001-0001-0001-000000000001',
    'bbbb0001-0001-0001-0001-000000000001',
    'cccc0007-0007-0007-0007-000000000007',
    'dddd0001-0001-0001-0001-000000000001',
    'view',
    'cccc0001-0001-0001-0001-000000000001',
    now() - interval '30 minutes'
);

-- ── 5. TENANT_A / TENDER_2 — second tender (EU-FLOW, multi-tender isolation) ─

-- USER_A on TENDER_2: same user, different tender
INSERT INTO tender_acl_entries
    (id, tenant_id, tender_id, user_id, tenant_membership_id,
     access_level, granted_by)
VALUES (
    'eeee0007-0007-0007-0007-000000000007',
    'aaaa0001-0001-0001-0001-000000000001',
    'bbbb0002-0002-0002-0002-000000000002',
    'cccc0002-0002-0002-0002-000000000002',
    'dddd0001-0001-0001-0001-000000000001',
    'edit',
    'cccc0001-0001-0001-0001-000000000001'
);

-- ── 6. TENANT_B — cross-tenant isolation (SEC-IDOR-01, TAC1-AUTH-05) ─────────

-- USER_G on TENANT_B / TENDER_1: must NEVER appear in TENANT_A queries
INSERT INTO tender_acl_entries
    (id, tenant_id, tender_id, user_id, tenant_membership_id,
     access_level, granted_by)
VALUES (
    'eeee0008-0008-0008-0008-000000000008',
    'aaaa0002-0002-0002-0002-000000000002',
    'bbbb0001-0001-0001-0001-000000000001',
    'cccc0008-0008-0008-0008-000000000008',
    'dddd0001-0001-0001-0001-000000000001',
    'view',
    'cccc0001-0001-0001-0001-000000000001'
);

-- ── 7. Consumer cascade test data ─────────────────────────────────────────────

-- Three extra entries for TENANT_A across both tenders:
-- used by EC-OFF-HP-01 (offboarding cascade deletes ALL of tenant_a's entries)
INSERT INTO tender_acl_entries
    (id, tenant_id, tender_id, user_id, tenant_membership_id,
     access_level, granted_by)
VALUES
    ('ffff0001-0001-0001-0001-000000000001',
     'aaaa0001-0001-0001-0001-000000000001',
     'bbbb0001-0001-0001-0001-000000000001',
     'cccc0001-0001-0001-0001-000000000001',
     'dddd0001-0001-0001-0001-000000000001',
     'approve',
     'cccc0001-0001-0001-0001-000000000001'),

    ('ffff0002-0002-0002-0002-000000000002',
     'aaaa0001-0001-0001-0001-000000000001',
     'bbbb0002-0002-0002-0002-000000000002',
     'cccc0003-0003-0003-0003-000000000003',
     'dddd0001-0001-0001-0001-000000000001',
     'view',
     'cccc0001-0001-0001-0001-000000000001'),

    ('ffff0003-0003-0003-0003-000000000003',
     'aaaa0001-0001-0001-0001-000000000001',
     'bbbb0002-0002-0002-0002-000000000002',
     'cccc0004-0004-0004-0004-000000000004',
     'dddd0001-0001-0001-0001-000000000001',
     'edit',
     'cccc0001-0001-0001-0001-000000000001');

-- ── 8. processed_events — idempotency seed (EC-OFF-IDEMP-01, EC-MEM-IDEMP-01) ─

-- Pre-mark one event as already processed to test duplicate delivery skip
INSERT INTO processed_events (event_id, consumer)
VALUES (
    'a1b2c3d4-0000-0000-0000-000000000001',
    'offboarding'
),
(
    'a1b2c3d4-0000-0000-0000-000000000002',
    'member_removal'
);

COMMIT;

-- ── VERIFY ───────────────────────────────────────────────────────────────────
\echo ''
\echo '=== tender_acl_entries seeded ==================================='
SELECT
    id,
    left(tenant_id::text, 8) AS tenant,
    left(tender_id::text, 8) AS tender,
    left(user_id::text, 8)   AS user,
    access_level,
    expires_at IS NOT NULL    AS has_expiry,
    deleted_at IS NOT NULL    AS revoked,
    record_version
FROM tender_acl_entries
WHERE tenant_id IN (
    'aaaa0001-0001-0001-0001-000000000001',
    'aaaa0002-0002-0002-0002-000000000002'
)
ORDER BY tenant_id, tender_id, created_at;

\echo ''
\echo '=== processed_events seeded ====================================='
SELECT event_id, consumer, processed_at FROM processed_events
WHERE event_id IN (
    'a1b2c3d4-0000-0000-0000-000000000001',
    'a1b2c3d4-0000-0000-0000-000000000002'
);

\echo ''
\echo 'mock_setup.sql complete — ready to test.'
\echo 'Run mock_teardown.sql after all tests are done.'
