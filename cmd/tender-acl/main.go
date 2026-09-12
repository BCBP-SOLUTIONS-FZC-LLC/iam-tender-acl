// Command tender-acl runs the Tender ACL Service: the system of record for
// tender_acl_entries, extracted from iam-org-membership per ADR-0007 Wave
// 3 (see docs/lld/iam-lld-tender-acl-service.md). It exposes the tenant-admin
// grant/revoke/list surface (TAC-1/2/3) and the internal service-to-service
// access check (TAC-4/I-12), enforces a grant-time-only membership check
// against iam-org-membership in place of the composite FK this table lost,
// and consumes TenantMembershipsPurged to cascade-delete an offboarded
// tenant's rows. It publishes zero events.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"golang.org/x/sync/errgroup"

	_ "github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/docs/swagger"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/inbound/consumer"
	httpadapter "github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/inbound/http"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/outbound/membershipcheck"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/outbound/metrics"
	aclpostgres "github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/outbound/postgres"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/outbound/valkey"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/port"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/service"
	eventsconfig "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-events/pkg/config"
	events "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-events/pkg/events"
	gincommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-gincommon/pkg/gincommon"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-gincommon/pkg/logger"
	pgcommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/pgcommon"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/pgmetrics"
)

// buildVersion is stamped at build time via -ldflags="-X main.buildVersion=...".
var buildVersion = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "tender-acl exited with error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// log is the single Zap-backed sink every log line in this process
	// flows through — HTTP/consumer middleware (via gincommon.Config.Logger
	// below), pgcommon's slow-query/migration logging (via
	// aclpostgres.LoggerAdapter), platform-events' SQS warnings, and the
	// core service/consumer layers (via port.SlogStyleLogger) — matching
	// iam-user-profile's and iam-org-membership's identical
	// logger.NewLogger(cfg.Environment) convention. No local JSON handler
	// or slog.SetDefault: nothing in this process reaches for package-level
	// slog anymore.
	log, err := logger.NewLogger(cfg.Environment)
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	svcLog := port.NewSlogStyleLogger(log)

	// Same composition-root order as iam-org-membership / iam-realm-provisioner:
	//   1. InitTracing so in-process spans get valid trace IDs even before
	//      the HTTP engine is built (migrations, pool connect, consumers).
	//   2. ObservabilityMiddlewares as gincommon's public metrics-init API
	//      BEFORE any collector registration, so business / events metrics
	//      land on gincommon.MetricsRegisterer with matching {service,
	//      version} const labels.
	// NewRouter applies the same middleware slice to the Gin engine;
	// gincommon's metrics.Init is sync.Once, EnsureInitTelemetry is a
	// no-op once the SDK provider is installed.
	serviceName := "tender-acl"
	version := getEnv("BUILD_VERSION", buildVersion)
	ginCfg := gincommon.Config{
		Logger:       log,
		ServiceName:  serviceName,
		BuildVersion: version,
	}
	shutdownTracing := gincommon.InitTracing(serviceName, version, os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	defer func() {
		shutdownTracing()
		if shutdownErr := gincommon.Shutdown(log); shutdownErr != nil {
			fmt.Fprintf(os.Stderr, "telemetry shutdown failed: %v\n", shutdownErr)
		}
	}()
	_ = gincommon.ObservabilityMiddlewares(ginCfg)
	tracer := otel.Tracer(serviceName)

	// platform-events consumer metrics and platform-pgcommon query/pool
	// metrics share gincommon's registerer so a single /metrics scrape
	// (promhttp on METRICS_PORT) serves HTTP + business + consume + pg
	// collectors together — same order as iam-org-membership /
	// iam-realm-provisioner (ObservabilityMiddlewares first, then
	// collectors). The SQS consumer internals already call Record*
	// unconditionally; they no-op until InitWithRegisterer runs once.
	events.InitWithRegisterer(ginCfg.ServiceName, ginCfg.BuildVersion, gincommon.MetricsRegisterer())
	pgmetrics.InitWithRegisterer(ginCfg.ServiceName, ginCfg.BuildVersion, gincommon.MetricsRegisterer())

	// SQS_QUEUE_URL and every other tenant-lifecycle-tenderacl-q tunable
	// (SQS_MAX_MESSAGES/SQS_WAIT_SECONDS/SQS_VISIBILITY_TIMEOUT/
	// SQS_CONCURRENCY/SQS_MAX_RECEIVE_COUNT) are loaded from
	// platform-events/pkg/config rather than hand-rolled here, matching
	// pgcommon.ConfigFromEnv's precedent below. member-removal-tenderacl-q
	// clones this env and overrides QueueURL + concurrency so both
	// subscriptions still go through SQSConfigFromEnv / SQSConsumerOptions.
	sqsEnv := eventsconfig.LoadSQS()
	if validateErr := sqsEnv.Validate(); validateErr != nil {
		return fmt.Errorf("load SQS config: %w", validateErr)
	}
	eventsconfig.LogWarningsTo(log, sqsEnv.Warnings)

	baseCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err = aclpostgres.RunMigrations(baseCtx, cfg.MigrationDatabaseURL, aclpostgres.NewLoggerAdapter(log)); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	pgCfg, pgWarnings := pgcommon.ConfigFromEnv()
	for _, w := range pgWarnings {
		log.Warn("postgres config warning", map[string]any{"key": w.Key, "reason": w.Reason})
	}
	// cfg.DatabaseURL (aclpostgres.DSNFromEnv(), set in loadConfig) rather
	// than ConfigFromEnv's own pgCfg.DSN directly: DSNFromEnv additionally
	// applies PG_STATEMENT_TIMEOUT, matching iam-user-profile's/
	// iam-org-membership's identical pgCfg.DSN = dsn assignment.
	pgCfg.DSN = cfg.DatabaseURL
	// GUCProvider: app.tenant_id is injected as a transaction-local GUC on
	// every pgcommon.RunInTx call (see
	// internal/adapter/outbound/postgres.withTenant), never session-scoped.
	pgCfg.GUCProvider = pgcommon.GUCSetFromContext
	// PGBouncerMode is forced true unconditionally — not env-driven via
	// PG_BOUNCER_MODE — because transaction-scoped GUCs are required for
	// correctness here regardless of deployment topology (LLD §17.5 Case 5,
	// the no-GUC-leakage-across-pooled-connection test); this is not a
	// tunable, so it isn't left to configuration to get right. MinConns is
	// forced 0 with it: idle backends under transaction pooling pin
	// session state (same helm default as iam-org-membership /
	// iam-realm-provisioner).
	pgCfg.PGBouncerMode = true
	pgCfg.MinConns = 0
	pgCfg.Logger = aclpostgres.NewLoggerAdapter(log)
	pgCfg.Tracer = aclpostgres.NewOTelTracer(serviceName)
	pgPool, err := pgcommon.NewPool(baseCtx, pgCfg)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	// Close is the safety net siblings register immediately after NewPool
	// (panic / early-return). DrainAndClose is LLD §16.4's documented
	// shutdown sequence and is registered next so it runs first (LIFO);
	// Close is then idempotent.
	defer pgPool.Close()
	defer func() {
		drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if drainErr := pgPool.DrainAndClose(drainCtx); drainErr != nil {
			log.Error("postgres pool drain failed", map[string]any{"error": drainErr.Error()})
		}
	}()

	valkeyClient := valkey.NewClient(valkey.ClientConfig{Addr: cfg.ValkeyAddr, Password: cfg.ValkeyPassword})
	defer func() {
		if closeErr := valkeyClient.Close(); closeErr != nil {
			log.Error("valkey client close failed", map[string]any{"error": closeErr.Error()})
		}
	}()
	aclCache := valkey.NewCache(valkeyClient)

	// ObservabilityMiddlewares already ran above, so MetricsRegisterer and
	// MetricsConstLabels are gincommon's real registry / {service, version}
	// labels — same order as iam-org-membership / iam-realm-provisioner.
	svcMetrics, err := metrics.NewMetrics(gincommon.MetricsRegisterer())
	if err != nil {
		return fmt.Errorf("build metrics: %w", err)
	}

	checker := membershipcheck.NewHTTPChecker(cfg.CoreInternalBaseURL, nil, cfg.MembershipCheckTimeout)

	repo := aclpostgres.NewTenderACLRepository(pgPool)
	txRunner := aclpostgres.NewTxRunner(pgPool)
	// One ProcessedEvents instance per consumer, on the same pool as
	// TenderACLRepository so cascade + MarkProcessed join one TxRunner
	// transaction (IDEMP-2). The composite (event_id, consumer) key
	// discriminates between them.
	offboardingProcessedEvents := aclpostgres.NewProcessedEvents(pgPool, "tenant_lifecycle_cleanup")
	memberRemovalProcessedEvents := aclpostgres.NewProcessedEvents(pgPool, "member_removal")

	svc := service.NewACLService(repo, checker, aclCache, svcMetrics, svcLog, tracer)
	handler := httpadapter.NewHandler(svc)
	docs := httpadapter.DocsConfig{
		Environment: cfg.Environment,
		Enabled:     cfg.DocsEnabled,
		AuthToken:   cfg.DocsAuthToken,
	}
	router := httpadapter.NewRouter(handler, pgPool, aclCache, log, ginCfg.Tracing, docs)

	httpServer := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           router.Handler(),
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      35 * time.Second, // 30s TimeoutMiddleware + 5s buffer
		IdleTimeout:       60 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
	}

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsServer := &http.Server{
		Addr:              ":" + cfg.MetricsPort,
		Handler:           metricsMux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	offboardingConsumer := consumer.NewOffboardingConsumer(repo, offboardingProcessedEvents, txRunner, svcMetrics, svcLog, tracer)
	eventsConsumer, err := events.NewSQSConsumer(
		eventsconfig.SQSConfigFromEnv(sqsEnv, log),
		offboardingConsumer.Handle,
		// SQSConsumerOptions returns WithConcurrency/WithVisibilityTimeout
		// unconditionally, plus WithMaxReceiveCount only when
		// SQS_MAX_RECEIVE_COUNT is set (it isn't, anywhere in this
		// service's env files) — so this can never produce the
		// WithMaxReceiveCount-without-a-paired-WithDeadLetterHandler no-op
		// a prior version of this call had. Redrive after maxReceiveCount=5
		// is handled entirely by tenant-lifecycle-tenderacl-q's own SQS
		// redrive policy (deploy/iam/README.md — this pod has no SQS
		// permissions on either DLQ), not an in-process dead-letter handler.
		eventsconfig.SQSConsumerOptions(sqsEnv)...,
	)
	if err != nil {
		return fmt.Errorf("build tenant-lifecycle-tenderacl-q consumer: %w", err)
	}

	// ADR-0007 Wave 3 Phase 3: a second, independent SQS subscription for
	// the per-user-removal ACL cascade —
	// deliberately a separate queue/consumer/DLQ from the tenant-
	// offboarding one above, not a discriminated payload on the same
	// queue, so each cascade's failure mode (and DLQ depth alert) stays
	// independently observable.
	memberRemovalConsumer := consumer.NewMemberRemovalConsumer(repo, memberRemovalProcessedEvents, txRunner, svcMetrics, svcLog, tracer)
	// LoadSQS covers one queue's env names. Clone it and override URL +
	// concurrency so queue #2 still goes through SQSConfigFromEnv /
	// SQSConsumerOptions (same MaxMessages/WaitSeconds/VisibilityTimeout
	// as queue #1) rather than a hand-rolled events.SQSConfig literal.
	memberRemovalSQS := sqsEnv
	memberRemovalSQS.QueueURL = cfg.MemberRemovalQueueURL
	consumerConcurrency, err := getEnvInt("CONSUMER_CONCURRENCY", 4)
	if err != nil {
		return err
	}
	memberRemovalConcurrency, err := getEnvInt("MEMBER_REMOVAL_SQS_CONCURRENCY", consumerConcurrency)
	if err != nil {
		return err
	}
	memberRemovalSQS.Concurrency = memberRemovalConcurrency
	memberRemovalSQS.Warnings = nil
	memberRemovalEventsConsumer, err := events.NewSQSConsumer(
		eventsconfig.SQSConfigFromEnv(memberRemovalSQS, log),
		memberRemovalConsumer.Handle,
		// See the offboarding consumer above: redrive is member-removal-tenderacl-q's
		// own SQS redrive policy, not an in-process WithDeadLetterHandler.
		eventsconfig.SQSConsumerOptions(memberRemovalSQS)...,
	)
	if err != nil {
		return fmt.Errorf("build member-removal-tenderacl-q consumer: %w", err)
	}

	group, gctx := errgroup.WithContext(baseCtx)

	group.Go(func() error {
		svcLog.Info("http server listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	})

	group.Go(func() error {
		svcLog.Info("metrics server listening", "addr", metricsServer.Addr)
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
		runProcessedEventsCleanup(gctx, offboardingProcessedEvents, cfg.ProcessedEventsCleanupInterval, svcLog)
		return nil
	})

	group.Go(func() error {
		runProcessedEventsCleanup(gctx, memberRemovalProcessedEvents, cfg.ProcessedEventsCleanupInterval, svcLog)
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
			svcLog.Error("http server shutdown failed", "error", err.Error()) //nolint:contextcheck // see above
		}
		if err := metricsServer.Shutdown(shutdownCtx); err != nil { //nolint:contextcheck // see above
			svcLog.Error("metrics server shutdown failed", "error", err.Error()) //nolint:contextcheck // see above
		}
		if err := eventsConsumer.Stop(); err != nil {
			svcLog.Error("events consumer stop failed", "error", err.Error()) //nolint:contextcheck // see above
		}
		if err := memberRemovalEventsConsumer.Stop(); err != nil {
			svcLog.Error("member removal events consumer stop failed", "error", err.Error()) //nolint:contextcheck // see above
		}
		return nil
	})

	svcLog.Info("tender-acl started", "environment", cfg.Environment, "build_version", buildVersion)
	return group.Wait()
}

func runProcessedEventsCleanup(ctx context.Context, repo *aclpostgres.ProcessedEvents, interval time.Duration, log port.SlogStyleLogger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			deleted, err := repo.CleanupExpired(ctx)
			if err != nil {
				log.ErrorContext(ctx, "processed_events cleanup failed", "error", err.Error())
				continue
			}
			if deleted > 0 {
				log.InfoContext(ctx, "processed_events cleanup complete", "deleted", deleted)
			}
		}
	}
}
