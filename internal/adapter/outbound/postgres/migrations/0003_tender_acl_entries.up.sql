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
--     record_version = $N; see IMPLEMENTATION_GAP_ANALYSIS.md Discrepancy 1,
--     resolved) — the trigger bump above keeps it current on every write.
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

-- Trigger name per LLD §7.5 verbatim (trg_touch_tae) — note this differs
-- from iam-org-membership's current trg_touch_tender_acl_entries; the LLD
-- name wins per "spec is authoritative" (see IMPLEMENTATION_GAP_ANALYSIS.md).
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
-- never an error and never another tenant's rows. (Hardening beyond the
-- LLD's literal current_setting(...)::uuid text — matches
-- iam-group-mapping's own migration exactly, proven necessary by
-- TestRLS_NoGUCLeakageAcrossPooledConnection.)
CREATE POLICY tenant_isolation ON tender_acl_entries
    FOR ALL
    USING      (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
