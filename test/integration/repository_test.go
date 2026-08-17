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
	_, err := repo.Grant(ctx, entry)
	require.NoError(t, err)

	found, err := repo.Revoke(ctx, tenantID, tenderID, userID)
	require.NoError(t, err)
	assert.True(t, found)

	// TAE-2: after revoke (deleted_at set), a fresh grant for the same
	// (tenant, tender, user) must succeed — the partial unique index only
	// constrains deleted_at IS NULL rows.
	_, err = repo.Grant(ctx, entry)
	require.NoError(t, err)
}

func TestRepository_Revoke_NoActiveEntry_ReturnsFoundFalseNotError(t *testing.T) {
	cleanupTable(t)
	found, err := repo.Revoke(context.Background(), uuid.New(), uuid.New(), uuid.New())
	require.NoError(t, err)
	assert.False(t, found)
}

func TestRepository_Revoke_Idempotent_SecondCallReturnsFoundFalse(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantID, tenderID, userID := uuid.New(), uuid.New(), uuid.New()
	_, err := repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: tenderID, UserID: userID,
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLView, GrantedBy: uuid.New(),
	})
	require.NoError(t, err)

	found1, err := repo.Revoke(ctx, tenantID, tenderID, userID)
	require.NoError(t, err)
	assert.True(t, found1)

	found2, err := repo.Revoke(ctx, tenantID, tenderID, userID)
	require.NoError(t, err)
	assert.False(t, found2, "revoking an already-revoked entry is not an error, per LLD §12.2")
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
	_, err = repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: tenderID, UserID: revokedUser,
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLView, GrantedBy: uuid.New(),
	})
	require.NoError(t, err)
	_, err = repo.Revoke(ctx, tenantID, tenderID, revokedUser)
	require.NoError(t, err)

	entries, err := repo.List(ctx, tenantID, tenderID)
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

	remaining, err := repo.List(ctx, tenantB, uuid.Nil)
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
