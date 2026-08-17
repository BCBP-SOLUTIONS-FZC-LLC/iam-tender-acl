//go:build rls

// Package rls exercises the Row-Level Security guarantees required by
// tender-acl-service-lld.md §17.5 directly against PostgreSQL, independent
// of the Go repository layer: fail-closed behavior with no tenant GUC
// bound, cross-tenant read/write/update denial, and the absence of any GUC
// leakage across transactions sharing a pooled connection (the PgBouncer
// transaction-pooling scenario). Mirrors iam-group-mapping's test/rls
// package structure, adapted to the single tender_acl_entries table.
//
// Run with: go test -tags=rls ./test/rls/...
package rls

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/test/testutil"
)

var (
	adminDB *pgxpool.Pool
	appDSN  string
)

func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pg, teardown, err := testutil.StartPostgres(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start postgres:", err)
		return 1
	}
	defer teardown()

	admin, err := pgxpool.New(ctx, pg.AdminDSN)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect admin pool:", err)
		return 1
	}
	adminDB = admin
	defer adminDB.Close()

	appDSN = pg.AppDSN

	return m.Run()
}

// newAppPool builds a fresh app-role pool with exactly one connection, so
// sequential Acquire calls are guaranteed to reuse the SAME physical
// connection — this is what makes it possible to test for GUC leakage
// across "different" transactions the way PgBouncer's transaction pooling
// mode would multiplex them onto one server connection.
func newAppPool(ctx context.Context, t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(appDSN)
	if err != nil {
		t.Fatalf("parse app dsn: %v", err)
	}
	cfg.MaxConns = 1
	cfg.MinConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect app pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
