//go:build integration

// Package integration exercises internal/adapter/outbound/postgres.
// TenderACLRepository and internal/adapter/outbound/valkey.Cache
// against real Postgres and Valkey containers — no mocks — plus the
// tenant-offboarding consumer's cascade-delete and processed_events
// idempotency ledger against the same real database.
//
// Run with: go test -tags=integration ./test/integration/...
package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/inbound/consumer"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/outbound/metrics"
	aclpostgres "github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/outbound/postgres"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/outbound/valkey"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/port"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/test/testutil"
	pgcommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/pgcommon"
)

var (
	repo                         *aclpostgres.TenderACLRepository
	adminPool                    *pgxpool.Pool
	valkeyCache                  *valkey.Cache
	valkeyClient                 *redis.Client
	processedEvents              *consumer.ProcessedEvents
	memberRemovalProcessedEvents *consumer.ProcessedEvents
	appPgcommonPool              *pgcommon.Pool
	// sharedMetrics is constructed once here, not per-test (see testMetrics
	// in service_test.go) — prometheus.Register (unlike the previous
	// OTel-meter-backed instruments) rejects a second registration of the
	// same metric name against the process-wide default registry.
	sharedMetrics *metrics.Metrics
)

func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pg, pgTeardown, err := testutil.StartPostgres(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start postgres:", err)
		return 1
	}
	defer pgTeardown()

	valkeyAddr, valkeyTeardown, err := testutil.StartValkey(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start valkey:", err)
		return 1
	}
	defer valkeyTeardown()

	admin, err := pgxpool.New(ctx, pg.AdminDSN)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect admin pool:", err)
		return 1
	}
	adminPool = admin
	defer adminPool.Close()

	pool, err := pgcommon.NewPool(ctx, pgcommon.Config{
		DSN:           pg.AppDSN,
		PGBouncerMode: true,
		GUCProvider:   pgcommon.GUCSetFromContext,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect app pgcommon pool:", err)
		return 1
	}
	appPgcommonPool = pool
	defer pool.Close()

	repo = aclpostgres.NewTenderACLRepository(pool)
	processedEvents = consumer.NewProcessedEvents(adminPool, "tenant_lifecycle_cleanup")
	memberRemovalProcessedEvents = consumer.NewProcessedEvents(adminPool, "member_removal")

	valkeyClient = valkey.NewClient(valkey.ClientConfig{Addr: valkeyAddr})
	defer func() { _ = valkeyClient.Close() }()
	valkeyCache = valkey.NewCache(valkeyClient)

	m2, err := metrics.NewMetrics()
	if err != nil {
		fmt.Fprintln(os.Stderr, "build metrics:", err)
		return 1
	}
	sharedMetrics = m2

	return m.Run()
}

var _ port.TenderACLRepository = (*aclpostgres.TenderACLRepository)(nil)

func cleanupTable(t *testing.T) {
	t.Helper()
	if _, err := adminPool.Exec(context.Background(), `TRUNCATE tender_acl_entries, processed_events`); err != nil {
		t.Fatalf("truncate tables: %v", err)
	}
}
