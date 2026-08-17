// Command tender-acl runs the Tender ACL Service: the system of record for
// tender_acl_entries, extracted from iam-org-membership per ADR-0007 Wave
// 3 (see tender-acl-service-lld.md). It exposes the tenant-admin
// grant/revoke/list surface (TAC-1/2/3) and the internal service-to-service
// access check (TAC-4/I-12), enforces a grant-time-only membership check
// against iam-org-membership in place of the composite FK this table lost,
// and consumes TenantOffboarded to cascade-delete an offboarded tenant's
// rows. It publishes zero events.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"

	_ "github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/docs/swagger"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/inbound/consumer"
	httpadapter "github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/inbound/http"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/outbound/membershipcheck"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/outbound/metrics"
	aclpostgres "github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/outbound/postgres"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/outbound/valkey"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/service"
	events "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-events/pkg/events"
	pgcommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/pgcommon"
)

// buildVersion is stamped at build time via -ldflags="-X main.buildVersion=...".
var buildVersion = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("tender-acl exited with error", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)

	baseCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tel, err := setupTelemetry(baseCtx, cfg)
	if err != nil {
		return fmt.Errorf("setup telemetry: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tel.Shutdown(shutdownCtx)
	}()

	if err = runMigrations(cfg.MigrationDatabaseURL); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	// PGBouncerMode+GUCProvider: app.tenant_id is injected as a
	// transaction-local GUC on every pgcommon.RunInTx call (see
	// internal/adapter/outbound/postgres.withTenant), never session-scoped — required
	// for correctness under PgBouncer transaction pooling (LLD §17.5 Case
	// 5, the no-GUC-leakage-across-pooled-connection test).
	pgPool, err := pgcommon.NewPool(baseCtx, pgcommon.Config{
		DSN:           cfg.DatabaseURL,
		PGBouncerMode: true,
		GUCProvider:   pgcommon.GUCSetFromContext,
	})
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer pgPool.Close()

	// processed_events carries no RLS (LLD §7.3) and is accessed only by the
	// tenant-offboarding consumer via the service role — a plain pgxpool.Pool
	// on the same DSN, entirely separate from the RLS-bound pgcommon.Pool
	// above (which has no exported accessor to an underlying raw pool).
	rawPool, err := pgxpool.New(baseCtx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect raw postgres pool: %w", err)
	}
	defer rawPool.Close()

	valkeyClient := valkey.NewClient(valkey.ClientConfig{Addr: cfg.ValkeyAddr, Password: cfg.ValkeyPassword})
	defer func() {
		if closeErr := valkeyClient.Close(); closeErr != nil {
			logger.Error("valkey client close failed", slog.String("error", closeErr.Error()))
		}
	}()
	aclCache := valkey.NewCache(valkeyClient)

	svcMetrics, err := metrics.NewMetrics(tel.Meter)
	if err != nil {
		return fmt.Errorf("build metrics: %w", err)
	}

	checker := membershipcheck.NewHTTPChecker(cfg.CoreInternalBaseURL, nil, cfg.MembershipCheckTimeout)

	repo := aclpostgres.NewTenderACLRepository(pgPool)
	// One ProcessedEvents instance per consumer — the composite (event_id,
	// consumer) key in processed_events discriminates between them, so
	// they safely share the one table.
	offboardingProcessedEvents := consumer.NewProcessedEvents(rawPool, "tenant_lifecycle_cleanup")
	memberRemovalProcessedEvents := consumer.NewProcessedEvents(rawPool, "member_removal")

	svc := service.NewACLService(repo, checker, aclCache, svcMetrics, logger, tel.Tracer)
	handler := httpadapter.NewHandler(svc)
	docs := httpadapter.DocsConfig{
		Environment: cfg.Environment,
		Enabled:     cfg.DocsEnabled,
		AuthToken:   cfg.DocsAuthToken,
	}
	router := httpadapter.NewRouter(handler, pgPool, aclCache, svcMetrics, logger, tel.Tracer, docs)

	httpServer := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           router.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsServer := &http.Server{
		Addr:              ":" + cfg.MetricsPort,
		Handler:           metricsMux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	offboardingConsumer := consumer.NewOffboardingConsumer(repo, offboardingProcessedEvents, svcMetrics, logger, tel.Tracer)
	eventsConsumer, err := events.NewSQSConsumer(
		events.SQSConfig{
			QueueURL: cfg.SQSQueueURL,
			Region:   cfg.AWSRegion,
		},
		offboardingConsumer.Handle,
		events.WithConcurrency(getEnvInt("CONSUMER_CONCURRENCY", 4)),
		events.WithMaxReceiveCount(5),
	)
	if err != nil {
		return fmt.Errorf("build tenant-lifecycle-tenderacl-q consumer: %w", err)
	}

	// ADR-0007 Wave 3 Phase 3 (O_AND_M_DELTA.md §5 Option B): a second,
	// independent SQS subscription for the per-user-removal ACL cascade —
	// deliberately a separate queue/consumer/DLQ from the tenant-
	// offboarding one above, not a discriminated payload on the same
	// queue, so each cascade's failure mode (and DLQ depth alert) stays
	// independently observable.
	memberRemovalConsumer := consumer.NewMemberRemovalConsumer(repo, memberRemovalProcessedEvents, svcMetrics, logger, tel.Tracer)
	memberRemovalEventsConsumer, err := events.NewSQSConsumer(
		events.SQSConfig{
			QueueURL: cfg.MemberRemovalQueueURL,
			Region:   cfg.AWSRegion,
		},
		memberRemovalConsumer.Handle,
		events.WithConcurrency(getEnvInt("CONSUMER_CONCURRENCY", 4)),
		events.WithMaxReceiveCount(5),
	)
	if err != nil {
		return fmt.Errorf("build member-removal-tenderacl-q consumer: %w", err)
	}

	group, gctx := errgroup.WithContext(baseCtx)

	group.Go(func() error {
		logger.Info("http server listening", slog.String("addr", httpServer.Addr))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	})

	group.Go(func() error {
		logger.Info("metrics server listening", slog.String("addr", metricsServer.Addr))
		if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("metrics server: %w", err)
		}
		return nil
	})

	group.Go(func() error {
		return eventsConsumer.Start(gctx)
	})

	group.Go(func() error {
		return memberRemovalEventsConsumer.Start(gctx)
	})

	group.Go(func() error {
		runProcessedEventsCleanup(gctx, offboardingProcessedEvents, cfg.ProcessedEventsCleanupInterval, logger)
		return nil
	})

	group.Go(func() error {
		runProcessedEventsCleanup(gctx, memberRemovalProcessedEvents, cfg.ProcessedEventsCleanupInterval, logger)
		return nil
	})

	group.Go(func() error {
		<-gctx.Done()
		// Deliberately derived from context.Background(), not gctx: gctx is
		// already canceled at this point, so shutdown needs its own bounded
		// deadline rather than inheriting a context that's already done.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil { //nolint:contextcheck // see above
			logger.Error("http server shutdown failed", slog.String("error", err.Error()))
		}
		if err := metricsServer.Shutdown(shutdownCtx); err != nil { //nolint:contextcheck // see above
			logger.Error("metrics server shutdown failed", slog.String("error", err.Error()))
		}
		if err := eventsConsumer.Stop(); err != nil {
			logger.Error("events consumer stop failed", slog.String("error", err.Error()))
		}
		if err := memberRemovalEventsConsumer.Stop(); err != nil {
			logger.Error("member removal events consumer stop failed", slog.String("error", err.Error()))
		}
		return nil
	})

	logger.Info("tender-acl started", slog.String("environment", cfg.Environment), slog.String("build_version", buildVersion))
	return group.Wait()
}

func newLogger(cfg config) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}

func runProcessedEventsCleanup(ctx context.Context, repo *consumer.ProcessedEvents, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			deleted, err := repo.CleanupExpired(ctx)
			if err != nil {
				logger.Error("processed_events cleanup failed", slog.String("error", err.Error()))
				continue
			}
			if deleted > 0 {
				logger.Info("processed_events cleanup complete", slog.Int64("deleted", deleted))
			}
		}
	}
}
