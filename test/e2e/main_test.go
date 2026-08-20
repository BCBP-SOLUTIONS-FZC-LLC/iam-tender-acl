//go:build e2e

// Package e2e exercises the full LLD §17.4 chain against real Postgres and
// real Valkey containers, through the actual HTTP router (real gin
// engine, real gincommon middlewares, real service/repository/cache
// wiring) — the same composition cmd/tender-acl/main.go performs, minus
// the SQS consumer and OTel exporters, which are exercised separately by
// test/integration.
//
// Run with: go test -tags=e2e ./test/e2e/...
package e2e

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	pgcommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/pgcommon"

	httpadapter "github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/inbound/http"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/outbound/metrics"
	aclpostgres "github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/outbound/postgres"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/outbound/valkey"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/service"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/test/testutil"

	nooptrace "go.opentelemetry.io/otel/trace/noop"
)

var (
	handler    http.Handler
	rawRedis   *redis.Client
	fakeChecks = &fakeChecker{}
)

// fakeChecker always reports an active membership with a fixed
// tenant_membership_id — TAC-2's grant-time membership check (LLD
// §7.6.2) is exercised in isolation by
// internal/adapter/outbound/membershipcheck's own unit tests and by
// test/integration; this e2e suite's concern is the grant->check->
// revoke->check chain, not the membershipcheck HTTP hop.
type fakeChecker struct{ membershipID uuid.UUID }

func (f *fakeChecker) Exists(_ context.Context, _, _ uuid.UUID) (bool, uuid.UUID, error) {
	if f.membershipID == uuid.Nil {
		f.membershipID = uuid.New()
	}
	return true, f.membershipID, nil
}

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

	// tender_acl_app (RLS-bound), matching what cmd/tender-acl/main.go
	// connects as in production — this suite's assertions depend on RLS
	// actually being enforced (the composition root sets app.tenant_id as
	// a transaction-local GUC via pgcommon.RunInTx, exactly like production).
	pgPool, err := pgcommon.NewPool(ctx, pgcommon.Config{
		DSN:           pg.AppDSN,
		PGBouncerMode: true,
		GUCProvider:   pgcommon.GUCSetFromContext,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect postgres:", err)
		return 1
	}
	defer pgPool.Close()

	rawRedis = valkey.NewClient(valkey.ClientConfig{Addr: valkeyAddr})
	defer rawRedis.Close()
	aclCache := valkey.NewCache(rawRedis)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svcMetrics, err := metrics.NewMetrics()
	if err != nil {
		fmt.Fprintln(os.Stderr, "build metrics:", err)
		return 1
	}
	tracer := nooptrace.NewTracerProvider().Tracer("e2e")

	repo := aclpostgres.NewTenderACLRepository(pgPool)
	svc := service.NewACLService(repo, fakeChecks, aclCache, svcMetrics, logger, tracer)
	h := httpadapter.NewHandler(svc)
	router := httpadapter.NewRouter(h, pgPool, aclCache, logger, nil, httpadapter.DocsConfig{Environment: "development"})
	handler = router.Handler()

	return m.Run()
}
