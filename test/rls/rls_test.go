//go:build rls

package rls

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func seedACLEntry(ctx context.Context, t *testing.T, tenantID, tenderID, userID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := adminDB.QueryRow(ctx,
		`INSERT INTO tender_acl_entries (tenant_id, tender_id, user_id, tenant_membership_id, access_level, granted_by)
		 VALUES ($1, $2, $3, $4, 'view', $5) RETURNING id`,
		tenantID, tenderID, userID, uuid.New(), uuid.New(),
	).Scan(&id)
	if err != nil {
		t.Fatalf("seed acl entry as admin: %v", err)
	}
	return id
}

// Case 1: missing tenant GUC => zero rows (fail closed).
func TestRLS_MissingTenantGUC_ReturnsZeroRows(t *testing.T) {
	ctx := context.Background()
	pool := newAppPool(ctx, t)
	tenantID := uuid.New()
	seedACLEntry(ctx, t, tenantID, uuid.New(), uuid.New())

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	// Deliberately do NOT bind app.tenant_id for this transaction.
	rows, err := tx.Query(ctx, `SELECT id FROM tender_acl_entries WHERE tenant_id = $1`, tenantID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 rows with no tenant GUC bound, got %d", count)
	}
}

// Case 2: cross-tenant read denied, even when the query explicitly filters
// by the target tenant's id — RLS wins regardless of the WHERE clause.
func TestRLS_CrossTenantRead_Denied(t *testing.T) {
	ctx := context.Background()
	pool := newAppPool(ctx, t)
	tenantA := uuid.New()
	tenantB := uuid.New()
	seedACLEntry(ctx, t, tenantA, uuid.New(), uuid.New())

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, bindErr := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantB.String()); bindErr != nil {
		t.Fatalf("bind guc: %v", bindErr)
	}

	rows, err := tx.Query(ctx, `SELECT id FROM tender_acl_entries WHERE tenant_id = $1`, tenantA)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if count != 0 {
		t.Fatalf("expected 0 rows reading tenant A's data while scoped to tenant B, got %d", count)
	}
}

// Case 3: cross-tenant write denied — inserting a row tagged for a
// different tenant than the bound GUC violates the WITH CHECK policy.
func TestRLS_CrossTenantWrite_Denied(t *testing.T) {
	ctx := context.Background()
	pool := newAppPool(ctx, t)
	tenantA := uuid.New()
	tenantB := uuid.New()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, bindErr := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantB.String()); bindErr != nil {
		t.Fatalf("bind guc: %v", bindErr)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO tender_acl_entries (tenant_id, tender_id, user_id, tenant_membership_id, access_level, granted_by)
		 VALUES ($1, $2, $3, $4, 'view', $5)`,
		tenantA, uuid.New(), uuid.New(), uuid.New(), uuid.New(),
	)
	if err == nil {
		t.Fatal("expected an insert scoped to a different tenant than the bound GUC to be rejected")
	}
}

// Case 4: cross-tenant update denied — a row belonging to tenant A is
// invisible to an UPDATE issued under tenant B's GUC, so it affects zero
// rows rather than silently modifying another tenant's data.
func TestRLS_CrossTenantUpdate_Denied(t *testing.T) {
	ctx := context.Background()
	pool := newAppPool(ctx, t)
	tenantA := uuid.New()
	tenantB := uuid.New()
	id := seedACLEntry(ctx, t, tenantA, uuid.New(), uuid.New())

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, bindErr := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantB.String()); bindErr != nil {
		t.Fatalf("bind guc: %v", bindErr)
	}

	tag, err := tx.Exec(ctx, `UPDATE tender_acl_entries SET deleted_at = now() WHERE id = $1`, id)
	if err != nil {
		// An outright policy rejection is an equally valid way for
		// Postgres to deny this; either outcome satisfies "denied".
		return
	}
	if tag.RowsAffected() != 0 {
		t.Fatalf("expected an update scoped to tenant B to affect 0 rows belonging to tenant A, affected %d", tag.RowsAffected())
	}

	// Confirm tenant A's row was genuinely untouched.
	var deletedAt *string
	if err := adminDB.QueryRow(ctx, `SELECT deleted_at::text FROM tender_acl_entries WHERE id = $1`, id).Scan(&deletedAt); err != nil {
		t.Fatalf("verify row as admin: %v", err)
	}
	if deletedAt != nil {
		t.Fatalf("expected tenant A's row to remain un-revoked, got deleted_at=%v", *deletedAt)
	}
}

// Case 5: no GUC leakage between two transactions that share the same
// pooled physical connection — the scenario PgBouncer's transaction
// pooling mode creates by handing one server connection to different
// client transactions in sequence (LLD §17.5 Case 5).
func TestRLS_NoGUCLeakageAcrossPooledConnection(t *testing.T) {
	ctx := context.Background()
	pool := newAppPool(ctx, t) // MaxConns=1 forces connection reuse below.
	tenantA := uuid.New()
	seedACLEntry(ctx, t, tenantA, uuid.New(), uuid.New())

	tx1, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx1: %v", err)
	}
	if _, bindErr := tx1.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantA.String()); bindErr != nil {
		t.Fatalf("bind guc in tx1: %v", bindErr)
	}

	var boundInTx1 string
	if scanErr := tx1.QueryRow(ctx, `SELECT current_setting('app.tenant_id', true)`).Scan(&boundInTx1); scanErr != nil {
		t.Fatalf("read guc in tx1: %v", scanErr)
	}
	if boundInTx1 != tenantA.String() {
		t.Fatalf("expected tx1 to see its own bound tenant, got %q", boundInTx1)
	}
	if commitErr := tx1.Commit(ctx); commitErr != nil {
		t.Fatalf("commit tx1: %v", commitErr)
	}

	// tx2 is a brand-new transaction that never calls set_config itself.
	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx2: %v", err)
	}
	defer tx2.Rollback(ctx)

	// Postgres registers a session-level placeholder for a custom GUC the
	// first time it's ever referenced; once that has happened on this
	// connection, an un-bound transaction observes '' rather than a true
	// SQL NULL for the rest of the session. Both are "unset" — the only
	// leak that would matter is tx2 observing tenantA's actual value.
	var leaked *string
	if scanErr := tx2.QueryRow(ctx, `SELECT current_setting('app.tenant_id', true)`).Scan(&leaked); scanErr != nil {
		t.Fatalf("read guc in tx2: %v", scanErr)
	}
	if leaked != nil && *leaked == tenantA.String() {
		t.Fatalf("app.tenant_id leaked from tx1 into tx2: got %q", *leaked)
	}
	if leaked != nil && *leaked != "" {
		t.Fatalf("expected tx2's app.tenant_id to be unset (nil or empty), got %q", *leaked)
	}

	// And the RLS policy itself must still fail closed for tx2: it must
	// not see tenant A's row despite the connection having just carried
	// tenant A's transaction (step 3 of LLD §17.5 Case 5).
	rows, err := tx2.Query(ctx, `SELECT id FROM tender_acl_entries WHERE tenant_id = $1`, tenantA)
	if err != nil {
		t.Fatalf("query in tx2: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if count != 0 {
		t.Fatalf("expected tx2 to see 0 rows for tenant A despite connection reuse, got %d", count)
	}

	// Step 4 of LLD §17.5 Case 5: as tx2 (no GUC / different tenant),
	// attempting to update tenant A's row must affect 0 rows.
	tag, err := tx2.Exec(ctx, `UPDATE tender_acl_entries SET deleted_at = now() WHERE tenant_id = $1`, tenantA)
	if err == nil && tag.RowsAffected() != 0 {
		t.Fatalf("expected update of tenant A's row from tx2 (unbound GUC) to affect 0 rows, affected %d", tag.RowsAffected())
	}
}

// Confirms tender_acl_entries has both rowsecurity and forcerowsecurity
// enabled (LLD §7.4 CI check), and that the app role lacks BYPASSRLS while
// the migrator role has it.
func TestRLS_TableAndRoleConfiguration(t *testing.T) {
	ctx := context.Background()

	var rowSecurity, forceRowSecurity bool
	err := adminDB.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = 'tender_acl_entries'::regclass`,
	).Scan(&rowSecurity, &forceRowSecurity)
	if err != nil {
		t.Fatalf("query pg_class: %v", err)
	}
	if !rowSecurity {
		t.Fatal("expected rowsecurity=true on tender_acl_entries")
	}
	if !forceRowSecurity {
		t.Fatal("expected forcerowsecurity=true on tender_acl_entries")
	}

	var appBypass bool
	if err := adminDB.QueryRow(ctx, `SELECT rolbypassrls FROM pg_roles WHERE rolname = 'tender_acl_app'`).Scan(&appBypass); err != nil {
		t.Fatalf("query tender_acl_app role: %v", err)
	}
	if appBypass {
		t.Fatal("tender_acl_app must NOT have BYPASSRLS")
	}

	var migratorBypass bool
	if err := adminDB.QueryRow(ctx, `SELECT rolbypassrls FROM pg_roles WHERE rolname = 'tender_acl_migrator'`).Scan(&migratorBypass); err != nil {
		t.Fatalf("query tender_acl_migrator role: %v", err)
	}
	if !migratorBypass {
		t.Fatal("tender_acl_migrator must have BYPASSRLS (LLD §7.4)")
	}
}

// CI also greps for a bare (non-LOCAL) SET app.tenant_id as a forbidden
// pattern (LLD §17.5) — enforced by the repository-layer contract that
// every statement runs inside pgcommon.WithTenantTx, which binds via
// SET LOCAL only. This is a static-analysis concern rather than something
// exercised at the SQL level, so it is not re-asserted here; see
// .golangci.yml/CI for the actual grep.
