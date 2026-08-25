-- =============================================================================
-- mock_teardown.sql — iam-tender-acl manual test cleanup
-- =============================================================================
-- Run AFTER all manual testing is complete:
--   psql "postgres://tender_acl:tender_acl@localhost:5536/tender_acl" \
--        -f scripts/manual-test/mock_teardown.sql
--
-- Must use the superuser / BYPASSRLS role (tender_acl or tender_acl_migrator).
-- Deletes ALL rows created by mock_setup.sql plus any rows created by TAC-2
-- (Grant) curls during testing — identified by our test tenant/tender UUIDs.
-- =============================================================================

BEGIN;

-- ── Delete ALL tender_acl_entries for test tenants ───────────────────────────
-- Catches seed rows + any rows created by TAC-2 Grant curls during testing
DELETE FROM tender_acl_entries
WHERE tenant_id IN (
    'aaaa0001-0001-0001-0001-000000000001',   -- TENANT_A
    'aaaa0002-0002-0002-0002-000000000002'    -- TENANT_B
);

-- ── Delete idempotency seed rows ─────────────────────────────────────────────
DELETE FROM processed_events
WHERE event_id IN (
    'a1b2c3d4-0000-0000-0000-000000000001',
    'a1b2c3d4-0000-0000-0000-000000000002'
);

-- Also clean up any processed_events written by consumer tests
DELETE FROM processed_events
WHERE consumer IN ('offboarding', 'member_removal')
  AND processed_at >= now() - interval '1 day';

COMMIT;

-- ── VERIFY — both tables must be empty for our test UUIDs ────────────────────
\echo ''
\echo '=== tender_acl_entries remaining for test tenants (expect 0) ===='
SELECT count(*) AS remaining
FROM tender_acl_entries
WHERE tenant_id IN (
    'aaaa0001-0001-0001-0001-000000000001',
    'aaaa0002-0002-0002-0002-000000000002'
);

\echo ''
\echo '=== processed_events remaining for test events (expect 0) ======='
SELECT count(*) AS remaining
FROM processed_events
WHERE event_id IN (
    'a1b2c3d4-0000-0000-0000-000000000001',
    'a1b2c3d4-0000-0000-0000-000000000002'
);

\echo ''
\echo 'mock_teardown.sql complete — database is clean.'
