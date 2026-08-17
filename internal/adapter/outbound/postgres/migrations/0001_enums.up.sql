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
