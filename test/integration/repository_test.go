//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
)

func TestRepository_Grant_PersistsTenantMembershipID(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantID, tenderID, userID, grantedBy, membershipID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()

	created, err := repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: tenderID, UserID: userID,
		TenantMembershipID: membershipID, AccessLevel: domain.ACLEdit, GrantedBy: grantedBy,
	})
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, created.ID)
	assert.Equal(t, membershipID, created.TenantMembershipID)
	assert.Equal(t, domain.ACLEdit, created.AccessLevel)
	assert.Equal(t, int64(1), created.RecordVersion)

	found, err := repo.FindActive(ctx, tenantID, tenderID, userID)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, created.ID, found.ID)
}

func TestRepository_Grant_DuplicateActiveEntry_ReturnsDuplicateGrant(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantID, tenderID, userID := uuid.New(), uuid.New(), uuid.New()
	entry := domain.TenderACLEntry{
		TenantID: tenantID, TenderID: tenderID, UserID: userID,
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLView, GrantedBy: uuid.New(),
	}

	_, err := repo.Grant(ctx, entry)
	require.NoError(t, err)

	_, err = repo.Grant(ctx, entry)
	require.Error(t, err)
	code, ok := domain.CodeOf(err)
	require.True(t, ok, "uq_tae_active_entry violation must map to a domain *Error")
	assert.Equal(t, domain.ErrCodeDuplicateGrant, code)
}

func TestRepository_Revoke_ThenReGrant_Succeeds(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantID, tenderID, userID := uuid.New(), uuid.New(), uuid.New()
	entry := domain.TenderACLEntry{
		TenantID: tenantID, TenderID: tenderID, UserID: userID,
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLView, GrantedBy: uuid.New(),
	}
	created, err := repo.Grant(ctx, entry)
	require.NoError(t, err)

	require.NoError(t, repo.Revoke(ctx, tenantID, tenderID, userID, created.RecordVersion))

	// TAE-2: after revoke (deleted_at set), a fresh grant for the same
	// (tenant, tender, user) must succeed — the partial unique index only
	// constrains deleted_at IS NULL rows.
	_, err = repo.Grant(ctx, entry)
	require.NoError(t, err)
}

// TestRepository_Revoke_NoActiveEntry_ReturnsOptimisticLockConflict covers
// LLD §11.2/§12.1: a row that was never granted matches zero rows in the
// version-gated UPDATE, which the repo cannot distinguish from a stale
// version — both surface identically as ErrCodeOptimisticLockConflict.
func TestRepository_Revoke_NoActiveEntry_ReturnsOptimisticLockConflict(t *testing.T) {
	cleanupTable(t)
	err := repo.Revoke(context.Background(), uuid.New(), uuid.New(), uuid.New(), 1)
	require.Error(t, err)
	code, ok := domain.CodeOf(err)
	require.True(t, ok)
	assert.Equal(t, domain.ErrCodeOptimisticLockConflict, code)
}

// TestRepository_Revoke_StaleVersion_ReturnsOptimisticLockConflict covers
// the case where the row IS active but the caller's record_version is
// behind the row's current value.
func TestRepository_Revoke_StaleVersion_ReturnsOptimisticLockConflict(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantID, tenderID, userID := uuid.New(), uuid.New(), uuid.New()
	created, err := repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: tenderID, UserID: userID,
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLView, GrantedBy: uuid.New(),
	})
	require.NoError(t, err)

	err = repo.Revoke(ctx, tenantID, tenderID, userID, created.RecordVersion+1)
	require.Error(t, err)
	code, ok := domain.CodeOf(err)
	require.True(t, ok)
	assert.Equal(t, domain.ErrCodeOptimisticLockConflict, code)
}

// TestRepository_Revoke_Idempotent_RetryWithStaleVersionConflicts covers
// LLD §12.1: touch_row() bumps record_version on the soft-delete itself
// (migration 0002), so a second Revoke call reusing the version that
// succeeded the first time now correctly conflicts rather than silently
// no-op'ing — a caller must re-read before retrying, exactly like TAC-2's
// existing optimistic-lock contract.
func TestRepository_Revoke_Idempotent_RetryWithStaleVersionConflicts(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantID, tenderID, userID := uuid.New(), uuid.New(), uuid.New()
	created, err := repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: tenderID, UserID: userID,
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLView, GrantedBy: uuid.New(),
	})
	require.NoError(t, err)

	require.NoError(t, repo.Revoke(ctx, tenantID, tenderID, userID, created.RecordVersion))

	err = repo.Revoke(ctx, tenantID, tenderID, userID, created.RecordVersion)
	require.Error(t, err, "retrying with the pre-revoke version must conflict, not silently no-op")
	code, ok := domain.CodeOf(err)
	require.True(t, ok)
	assert.Equal(t, domain.ErrCodeOptimisticLockConflict, code)
}

func TestRepository_FindActive_ExcludesExpiredEntries(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantID, tenderID, userID := uuid.New(), uuid.New(), uuid.New()
	past := time.Now().Add(-time.Hour)
	_, err := repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: tenderID, UserID: userID,
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLView, GrantedBy: uuid.New(),
		ExpiresAt: &past,
	})
	require.NoError(t, err)

	found, err := repo.FindActive(ctx, tenantID, tenderID, userID)
	require.NoError(t, err)
	assert.Nil(t, found, "TAE-3: a passively expired entry must not be returned as active")
}

func TestRepository_List_IncludesExpiredButNotRevoked(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantID, tenderID := uuid.New(), uuid.New()
	past := time.Now().Add(-time.Hour)

	_, err := repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: tenderID, UserID: uuid.New(),
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLView, GrantedBy: uuid.New(), ExpiresAt: &past,
	})
	require.NoError(t, err)

	revokedUser := uuid.New()
	revokedCreated, err := repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: tenderID, UserID: revokedUser,
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLView, GrantedBy: uuid.New(),
	})
	require.NoError(t, err)
	require.NoError(t, repo.Revoke(ctx, tenantID, tenderID, revokedUser, revokedCreated.RecordVersion))

	entries, err := repo.List(ctx, tenantID, tenderID, 100, 0)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "list must include the passively-expired entry (TAE-7) but not the revoked one (TAE-4)")
}

func TestRepository_CascadeDeleteForTenant_RemovesOnlyThatTenant(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantA, tenantB := uuid.New(), uuid.New()

	_, err := repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantA, TenderID: uuid.New(), UserID: uuid.New(),
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLView, GrantedBy: uuid.New(),
	})
	require.NoError(t, err)
	_, err = repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantB, TenderID: uuid.New(), UserID: uuid.New(),
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLView, GrantedBy: uuid.New(),
	})
	require.NoError(t, err)

	deleted, err := repo.CascadeDeleteForTenant(ctx, tenantA)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	remaining, err := repo.List(ctx, tenantB, uuid.Nil, 100, 0)
	require.NoError(t, err)
	_ = remaining // tenant B's row exists under a different tender_id; existence is what matters

	var count int
	err = adminPool.QueryRow(ctx, `SELECT count(*) FROM tender_acl_entries WHERE tenant_id = $1`, tenantA).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "cascade delete must be a hard delete, not soft-delete")

	err = adminPool.QueryRow(ctx, `SELECT count(*) FROM tender_acl_entries WHERE tenant_id = $1`, tenantB).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "cascade delete for tenant A must not touch tenant B's rows")
}

// TestRepository_CascadeDeleteForTenant_Idempotent confirms deleting an
// already-cleaned tenant is a safe no-op (LLD §10.1's idempotency claim).
func TestRepository_CascadeDeleteForTenant_Idempotent(t *testing.T) {
	cleanupTable(t)
	tenantID := uuid.New()
	deleted, err := repo.CascadeDeleteForTenant(context.Background(), tenantID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), deleted)
}

// TestRepository_SoftDeleteForUser_MarksOnlyThatUsersRowsDeleted confirms
// the per-user-removal ACL cascade (ADR-0007 Wave 3 Phase 3) is a SOFT
// delete (deleted_at set, row retained) — unlike CascadeDeleteForTenant's
// hard delete — and scoped to exactly (tenant, user), leaving another
// user's row in the same tenant untouched.
func TestRepository_SoftDeleteForUser_MarksOnlyThatUsersRowsDeleted(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userA, userB := uuid.New(), uuid.New()

	entryA, err := repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: uuid.New(), UserID: userA,
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLEdit, GrantedBy: uuid.New(),
	})
	require.NoError(t, err)
	_, err = repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: uuid.New(), UserID: userB,
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLView, GrantedBy: uuid.New(),
	})
	require.NoError(t, err)

	deleted, err := repo.SoftDeleteForUser(ctx, tenantID, userA)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	var aDeletedAt *time.Time
	require.NoError(t, adminPool.QueryRow(ctx,
		`SELECT deleted_at FROM tender_acl_entries WHERE id = $1`, entryA.ID,
	).Scan(&aDeletedAt))
	assert.NotNil(t, aDeletedAt, "SoftDeleteForUser must set deleted_at, not hard-delete the row")

	var bCount int
	require.NoError(t, adminPool.QueryRow(ctx,
		`SELECT count(*) FROM tender_acl_entries WHERE tenant_id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		tenantID, userB,
	).Scan(&bCount))
	assert.Equal(t, 1, bCount, "SoftDeleteForUser for userA must not touch userB's row")
}

// TestRepository_SoftDeleteForUser_Idempotent confirms a second call for a
// user with no remaining active rows is a safe no-op (0 rows affected,
// no error) — mirrors CascadeDeleteForTenant_Idempotent above.
func TestRepository_SoftDeleteForUser_Idempotent(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantID, userID := uuid.New(), uuid.New()

	_, err := repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: uuid.New(), UserID: userID,
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLView, GrantedBy: uuid.New(),
	})
	require.NoError(t, err)

	first, err := repo.SoftDeleteForUser(ctx, tenantID, userID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), first)

	second, err := repo.SoftDeleteForUser(ctx, tenantID, userID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), second, "second call must find no active rows left to soft-delete")
}

// ── helpers ───────────────────────────────────────────────────────────────────

func execAdmin(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := adminPool.Exec(context.Background(), sql, args...)
	require.NoError(t, err)
}

// ── Grant with non-empty Reason (covers the `reason = entry.Reason` branch) ──

func TestRepository_Grant_WithReason(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantID, tenderID, userID, grantedBy, membershipID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	reason := "approved by compliance"

	created, err := repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: tenderID, UserID: userID,
		TenantMembershipID: membershipID, AccessLevel: domain.ACLView,
		GrantedBy: grantedBy, Reason: reason,
	})
	require.NoError(t, err)
	assert.Equal(t, reason, created.Reason)
}

// ── Grant INSERT error (covers the non-unique fmt.Errorf branch) ──────────────

func TestRepository_Grant_InsertError(t *testing.T) {
	cleanupTable(t)
	execAdmin(t, `
		CREATE OR REPLACE FUNCTION _test_ins_fail() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'forced insert failure'; END; $$`)
	execAdmin(t, `CREATE TRIGGER _test_ins_fail_t BEFORE INSERT ON tender_acl_entries
				  FOR EACH ROW EXECUTE FUNCTION _test_ins_fail()`)
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(), `DROP TRIGGER IF EXISTS _test_ins_fail_t ON tender_acl_entries`)
		_, _ = adminPool.Exec(context.Background(), `DROP FUNCTION IF EXISTS _test_ins_fail()`)
	})

	_, err := repo.Grant(context.Background(), domain.TenderACLEntry{
		TenantID: uuid.New(), TenderID: uuid.New(), UserID: uuid.New(),
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLView, GrantedBy: uuid.New(),
	})
	assert.Error(t, err)
}

// ── List query error (covers `fmt.Errorf("query tender_acl_entries: %w", err)`) ─

func TestRepository_List_QueryError(t *testing.T) {
	execAdmin(t, `ALTER TABLE tender_acl_entries RENAME TO tender_acl_entries_bak`)
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(), `ALTER TABLE IF EXISTS tender_acl_entries_bak RENAME TO tender_acl_entries`)
	})

	_, err := repo.List(context.Background(), uuid.New(), uuid.New(), 100, 0)
	assert.Error(t, err)
}

// ── Revoke exec error (covers `fmt.Errorf("revoke tender_acl_entries: %w", err)`) ─

func TestRepository_Revoke_ExecError(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()

	// Grant a row first (before the trigger is installed).
	tenantID, tenderID, userID, grantedBy, membershipID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	created, err := repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: tenderID, UserID: userID,
		TenantMembershipID: membershipID, AccessLevel: domain.ACLView, GrantedBy: grantedBy,
	})
	require.NoError(t, err)

	// Now install the trigger so the UPDATE in Revoke will fail.
	execAdmin(t, `
		CREATE OR REPLACE FUNCTION _test_upd_fail() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'forced update failure'; END; $$`)
	execAdmin(t, `CREATE TRIGGER _test_upd_fail_t BEFORE UPDATE ON tender_acl_entries
				  FOR EACH ROW EXECUTE FUNCTION _test_upd_fail()`)
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(), `DROP TRIGGER IF EXISTS _test_upd_fail_t ON tender_acl_entries`)
		_, _ = adminPool.Exec(context.Background(), `DROP FUNCTION IF EXISTS _test_upd_fail()`)
	})

	err = repo.Revoke(ctx, tenantID, tenderID, userID, created.RecordVersion)
	assert.Error(t, err)
}

// ── FindActive query error (covers `fmt.Errorf("query active tender_acl_entries: %w", err)`) ─

func TestRepository_FindActive_QueryError(t *testing.T) {
	execAdmin(t, `ALTER TABLE tender_acl_entries RENAME TO tender_acl_entries_bak`)
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(), `ALTER TABLE IF EXISTS tender_acl_entries_bak RENAME TO tender_acl_entries`)
	})

	_, err := repo.FindActive(context.Background(), uuid.New(), uuid.New(), uuid.New())
	assert.Error(t, err)
}

// ── CascadeDeleteForTenant exec error ─────────────────────────────────────────

func TestRepository_CascadeDeleteForTenant_ExecError(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()

	tenantID, tenderID, userID, grantedBy, membershipID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err := repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: tenderID, UserID: userID,
		TenantMembershipID: membershipID, AccessLevel: domain.ACLView, GrantedBy: grantedBy,
	})
	require.NoError(t, err)

	execAdmin(t, `
		CREATE OR REPLACE FUNCTION _test_del_fail() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'forced delete failure'; END; $$`)
	execAdmin(t, `CREATE TRIGGER _test_del_fail_t BEFORE DELETE ON tender_acl_entries
				  FOR EACH ROW EXECUTE FUNCTION _test_del_fail()`)
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(), `DROP TRIGGER IF EXISTS _test_del_fail_t ON tender_acl_entries`)
		_, _ = adminPool.Exec(context.Background(), `DROP FUNCTION IF EXISTS _test_del_fail()`)
	})

	_, err = repo.CascadeDeleteForTenant(ctx, tenantID)
	assert.Error(t, err)
}

// ── SoftDeleteForUser exec error ──────────────────────────────────────────────

func TestRepository_SoftDeleteForUser_ExecError(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()

	tenantID, tenderID, userID, grantedBy, membershipID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err := repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: tenderID, UserID: userID,
		TenantMembershipID: membershipID, AccessLevel: domain.ACLView, GrantedBy: grantedBy,
	})
	require.NoError(t, err)

	// Reuse the same update trigger function if it exists from a prior test — use CREATE OR REPLACE.
	execAdmin(t, `
		CREATE OR REPLACE FUNCTION _test_upd_fail() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'forced update failure'; END; $$`)
	execAdmin(t, `CREATE TRIGGER _test_sdu_fail_t BEFORE UPDATE ON tender_acl_entries
				  FOR EACH ROW EXECUTE FUNCTION _test_upd_fail()`)
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(), `DROP TRIGGER IF EXISTS _test_sdu_fail_t ON tender_acl_entries`)
		_, _ = adminPool.Exec(context.Background(), `DROP FUNCTION IF EXISTS _test_upd_fail()`)
	})

	_, err = repo.SoftDeleteForUser(ctx, tenantID, userID)
	assert.Error(t, err)
}
