-- The runtime application role. It must NEVER be granted BYPASSRLS: every
-- statement it issues against tender_acl_entries is subject to the FORCE
-- ROW LEVEL SECURITY policy installed in 0003, with no exceptions.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tender_acl_app') THEN
        -- Dev-only password, matching the credential wired through
        -- docker-compose.yml for local development. Production deployments
        -- must rotate this out of band — never by editing this checked-in
        -- migration.
        CREATE ROLE tender_acl_app LOGIN PASSWORD 'tender_acl_app_dev_password' NOBYPASSRLS;
    END IF;
END
$$;

GRANT USAGE ON SCHEMA public TO tender_acl_app;

GRANT SELECT, INSERT, UPDATE, DELETE ON
    tender_acl_entries,
    processed_events
TO tender_acl_app;

-- The migration/admin role (LLD §7.4): applies schema migrations and backs
-- any cross-tenant admin access path, deliberately exempt from RLS. Unlike
-- iam-group-mapping (whose migrator role is provisioned externally, not by
-- a checked-in migration), the LLD explicitly calls for
-- "ALTER ROLE tender_acl_migrator BYPASSRLS" (§7.4), so it is created here.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tender_acl_migrator') THEN
        CREATE ROLE tender_acl_migrator LOGIN PASSWORD 'tender_acl_migrator_dev_password';
    END IF;
END
$$;

ALTER ROLE tender_acl_migrator BYPASSRLS;
GRANT ALL PRIVILEGES ON SCHEMA public TO tender_acl_migrator;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO tender_acl_migrator;
