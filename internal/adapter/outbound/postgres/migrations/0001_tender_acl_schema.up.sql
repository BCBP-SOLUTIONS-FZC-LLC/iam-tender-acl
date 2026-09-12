-- Single consolidated migration for this service's entire schema. This
-- service has never been deployed, so there is no prior release to stay
-- backward-compatible with — the schema is expressed as its final shape
-- rather than as an incremental history of how it got there.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- Local copy of the shared tender_acl_level vocabulary (same pattern as
-- tenant_plan/dept_role/tenant_role in sibling IAM services). Cross-DB enum
-- drift between this copy and any other service's copy is unenforced —
-- tracked as TAC-Q6, deliberately deferred (LLD §19).
CREATE TYPE tender_acl_level AS ENUM (
    'view',
    'edit',
    'approve'
);

-- touch_row() is installed as a BEFORE UPDATE ... FOR EACH ROW trigger,
-- guarded by "WHEN (OLD.* IS DISTINCT FROM NEW.*)", so it only fires when
-- the row actually changed. Identical to iam-group-mapping's own
-- touch_row() function.
CREATE FUNCTION touch_row()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at := now();
    NEW.record_version := OLD.record_version + 1;
    RETURN NEW;
END;
$$;

-- tender_acl_entries, physically relocated from iam-org-membership
-- unchanged in shape (LLD §7.2.1), minus two FKs that could not survive the
-- database split:
--   fk_tae_tenant              FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
--   fk_tae_tenant_membership   FOREIGN KEY (tenant_membership_id, tenant_id, user_id)
--                              REFERENCES tenant_memberships (id, tenant_id, user_id)
-- The first is replaced by the tenant-lifecycle-tenderacl-q consumer
-- cascade (LLD §7.6.4/§10.1). The second is replaced by a synchronous,
-- grant-time-only membership-existence check via internal/adapter/outbound/membershipcheck
-- (LLD §7.6.2) — tenant_membership_id remains as an audit-only reference,
-- no longer DB-validated against a live row.
--
-- Two additions beyond the source schema, both explicitly called for by
-- the LLD:
--   record_version — an optimistic-lock token per LLD §7.2.1/§12.1,
--     enforced on Revoke (repository.go's Revoke gates the UPDATE on
--     record_version = $N) — the trigger bump above keeps it current on
--     every write.
--   reason CHECK (char_length <= 500) — the source schema left this an
--     unbounded text column; the LLD (§7.2.1) explicitly calls for a
--     database-layer cap, not just API-layer validation.
CREATE TABLE tender_acl_entries (
    id                   uuid NOT NULL PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            uuid NOT NULL,
    tender_id            uuid NOT NULL,                          -- no FK: cross-service, Tender Service owns tenders
    user_id              uuid NOT NULL,
    tenant_membership_id uuid NOT NULL,                          -- audit-only reference, see above
    access_level         tender_acl_level NOT NULL DEFAULT 'view',
    granted_by           uuid NOT NULL,
    reason               text CHECK (reason IS NULL OR char_length(reason) <= 500),
    expires_at           timestamptz,
    record_version       bigint NOT NULL DEFAULT 1 CHECK (record_version > 0),
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    deleted_at           timestamptz
);

-- TAE-1: at most one active grant per (tenant, tender, user).
CREATE UNIQUE INDEX uq_tae_active_entry ON tender_acl_entries (tenant_id, tender_id, user_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_tae_tenant_tender ON tender_acl_entries (tenant_id, tender_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_tae_user          ON tender_acl_entries (tenant_id, user_id)   WHERE deleted_at IS NULL;
CREATE INDEX idx_tae_membership    ON tender_acl_entries (tenant_membership_id);

-- Non-partial, unlike the three indexes above: the tenant-offboarding
-- cascade delete (repository.go's CascadeDeleteForTenant, the
-- TenantMembershipsPurged consumer) deletes ALL of a tenant's rows,
-- including already-soft-deleted ones retained for audit (LLD §18) — a
-- partial WHERE deleted_at IS NULL index cannot be used to locate those.
-- Without this, a high-churn tenant's offboarding forces a sequential scan
-- under the cascade's write transaction.
CREATE INDEX idx_tae_tenant_all ON tender_acl_entries (tenant_id);

-- Trigger name per LLD §7.5 verbatim (trg_touch_tae) — differs from
-- iam-org-membership's trg_touch_tender_acl_entries; the LLD name wins per
-- "spec is authoritative".
CREATE TRIGGER trg_touch_tae
    BEFORE UPDATE ON tender_acl_entries
    FOR EACH ROW
    WHEN (OLD.* IS DISTINCT FROM NEW.*)
    EXECUTE FUNCTION touch_row();

ALTER TABLE tender_acl_entries ENABLE ROW LEVEL SECURITY;
ALTER TABLE tender_acl_entries FORCE ROW LEVEL SECURITY;
REVOKE ALL ON tender_acl_entries FROM PUBLIC;

-- TAC-D8: unlike iam-org-membership's rls_check_tenant()/rls_violation_log
-- forensic-logging wrapper, this policy is deliberately plain (mirrors
-- iam-group-mapping's GM-D8). NULLIF(...,'') hardens against PgBouncer
-- transaction-pooling GUC-leakage: current_setting(..., true) can return ''
-- (not NULL) for a session that referenced app.tenant_id before without
-- binding it, and a bare ''::uuid cast raises an error rather than
-- excluding rows. Normalizing to NULL first makes a missing GUC compare as
-- "tenant_id = NULL" — fail closed (zero rows / every write rejected),
-- never an error and never a leak — proven necessary by
-- TestRLS_NoGUCLeakageAcrossPooledConnection.
CREATE POLICY tenant_isolation ON tender_acl_entries
    FOR ALL
    USING      (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

-- processed_events is the idempotency ledger backing both the
-- tenant-offboarding cascade consumer and the per-user-removal cascade
-- consumer (LLD §7.2.2). Not tenant-facing through any API and not subject
-- to Row-Level Security. Composite primary key (event_id, consumer) rather
-- than event_id alone, so the two consumers dedup independently instead of
-- colliding on the same event_id from an unrelated relay.
--
-- event_id is uuid, not text — mirroring iam-group-mapping's own
-- processed_events table: this service's event ids are always genuine
-- UUIDs (validated via uuid.Parse in both OffboardingConsumer and
-- MemberRemovalConsumer before ever reaching this store), so the stronger
-- column type is strictly more correct here.
--
-- No event_type or expires_at column: retention is enforced by batched-
-- delete pruning against processed_at at cleanup time (8 days, see
-- ProcessedEvents.CleanupExpired), not a precomputed per-row expiry.
CREATE TABLE processed_events (
    event_id     uuid NOT NULL,
    consumer     text NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (event_id, consumer)
);

-- Backs the 8-day retention cleanup sweep (LLD §7.2.2/§18).
CREATE INDEX idx_processed_events_prune ON processed_events (processed_at);

-- The runtime application role. It must NEVER be granted BYPASSRLS: every
-- statement it issues against tender_acl_entries is subject to the FORCE
-- ROW LEVEL SECURITY policy above, with no exceptions.
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
