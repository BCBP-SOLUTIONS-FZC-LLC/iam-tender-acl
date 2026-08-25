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
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/service"
	eventsconfig "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-events/pkg/config"
	events "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-events/pkg/events"
	gincommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-gincommon/pkg/gincommon"
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
	mapLogger := slogMapLogger{l: logger}

	// SQS_QUEUE_URL and every other tenant-lifecycle-tenderacl-q tunable
	// (SQS_MAX_MESSAGES/SQS_WAIT_SECONDS/SQS_VISIBILITY_TIMEOUT/
	// SQS_CONCURRENCY/SQS_MAX_RECEIVE_COUNT) are loaded from
	// platform-events/pkg/config rather than hand-rolled here, matching
	// pgcommon.ConfigFromEnv's precedent below — this is the queue LoadSQS
	// was designed for (a single queue per service); member-removal-
	// tenderacl-q below has no such helper (LoadSQS has no concept of a
	// second queue) and stays hand-rolled in config.go/loadConfig.
	sqsEnv := eventsconfig.LoadSQS()
	if validateErr := sqsEnv.Validate(); validateErr != nil {
		return fmt.Errorf("load SQS config: %w", validateErr)
	}
	eventsconfig.LogWarningsTo(mapLogger, sqsEnv.Warnings)

	baseCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Tracing is entirely gincommon's: httpadapter.NewRouter's
	// gincommon.ObservabilityMiddlewares call lazily installs the real OTel
	// TracerProvider (via the Tracing options below) the first time it
	// runs — there is no separate hand-rolled TracerProvider/exporter setup
	// in this process. otel.Tracer returns a delegating handle that's safe
	// to obtain before that installation happens and use afterward (the
	// OTel SDK's documented global-provider swap behavior).
	insecure := cfg.Environment != "production"
	sampleRatio := 1.0
	if cfg.Environment == "production" {
		sampleRatio = 0.1
	}
	tracing := &gincommon.TracingOptions{
		Endpoint:    cfg.OTELExporterOTLPEndpoint,
		Insecure:    &insecure,
		SampleRatio: &sampleRatio,
	}
	tracer := otel.Tracer("tender-acl")
	defer func() {
		if shutdownErr := gincommon.Shutdown(nil); shutdownErr != nil {
			logger.Error("telemetry shutdown failed", slog.String("error", shutdownErr.Error()))
		}
	}()

	if err = aclpostgres.RunMigrations(baseCtx, cfg.MigrationDatabaseURL, slogDomainLogger{l: logger}); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	pgCfg, pgWarnings := pgcommon.ConfigFromEnv()
	for _, w := range pgWarnings {
		logger.Warn("postgres config warning", slog.String("key", w.Key), slog.String("reason", w.Reason))
	}
	// GUCProvider: app.tenant_id is injected as a transaction-local GUC on
	// every pgcommon.RunInTx call (see
	// internal/adapter/outbound/postgres.withTenant), never session-scoped.
	pgCfg.GUCProvider = pgcommon.GUCSetFromContext
	// PGBouncerMode is forced true unconditionally — not env-driven via
	// PG_BOUNCER_MODE — because transaction-scoped GUCs are required for
	// correctness here regardless of deployment topology (LLD §17.5 Case 5,
	// the no-GUC-leakage-across-pooled-connection test); this is not a
	// tunable, so it isn't left to configuration to get right.
	pgCfg.PGBouncerMode = true
	pgCfg.Logger = slogDomainLogger{l: logger}
	pgCfg.Tracer = otelSpanTracer{tracer: tracer}
	pgPool, err := pgcommon.NewPool(baseCtx, pgCfg)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	// DrainAndClose, not a bare Close — LLD §16.4's documented shutdown
	// sequence. Registered before rawPool's below so it runs last (LIFO):
	// this is the RLS-bound pool every TAC-1/2/3/4 request and both
	// consumers' repository calls go through, so it should be the last
	// thing to stop accepting new work. In practice httpServer.Shutdown and
	// both consumers' Stop() (the shutdown goroutine below) already wait
	// for in-flight work to finish before this defer ever runs, so the
	// drain here should always complete immediately — DrainAndClose is
	// still the correct call over Close, as defense in depth against that
	// ordering ever changing.
	defer func() {
		drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if drainErr := pgPool.DrainAndClose(drainCtx); drainErr != nil {
			logger.Error("postgres pool drain failed", slog.String("error", drainErr.Error()))
		}
	}()

	// processed_events carries no RLS (LLD §7.3) and is accessed only by the
	// two consumers via the service role — a second pgcommon.Pool on the
	// same DSN, entirely separate from the RLS-bound pool above (which has
	// no exported accessor to an underlying raw pool) but still going
	// through pgcommon.NewPool rather than a bare pgxpool.New: no
	// GUCProvider is set, so no GUC injection is ever attempted for this
	// table, but this pool still gets the same slow-query logging and OTel
	// query spans as the main pool, instead of being entirely invisible to
	// both (a prior version used a bare *pgxpool.Pool here and had neither).
	rawPoolCfg := pgcommon.Config{
		DSN:           cfg.DatabaseURL,
		PGBouncerMode: pgCfg.PGBouncerMode,
		Logger:        slogDomainLogger{l: logger},
		Tracer:        otelSpanTracer{tracer: tracer},
	}
	rawPool, err := pgcommon.NewPool(baseCtx, rawPoolCfg)
	if err != nil {
		return fmt.Errorf("connect raw postgres pool: %w", err)
	}
	// Same DrainAndClose reasoning as pgPool above — this defer runs before
	// pgPool's (LIFO), so processed_events stops accepting new WithConn
	// calls slightly ahead of the main pool.
	defer func() {
		drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if drainErr := rawPool.DrainAndClose(drainCtx); drainErr != nil {
			logger.Error("raw postgres pool drain failed", slog.String("error", drainErr.Error()))
		}
	}()

	valkeyClient := valkey.NewClient(valkey.ClientConfig{Addr: cfg.ValkeyAddr, Password: cfg.ValkeyPassword})
	defer func() {
		if closeErr := valkeyClient.Close(); closeErr != nil {
			logger.Error("valkey client close failed", slog.String("error", closeErr.Error()))
		}
	}()
	aclCache := valkey.NewCache(valkeyClient)

	// gincommon.MetricsRegisterer() is safe to call before
	// ObservabilityMiddlewares runs (falls back to prometheus.DefaultRegisterer
	// until gincommon's own Init runs, which is exactly the registry these
	// collectors were already implicitly using) — same precedent as
	// iam-user-profile's cmd/server/main.go.
	svcMetrics, err := metrics.NewMetrics(gincommon.MetricsRegisterer())
	if err != nil {
		return fmt.Errorf("build metrics: %w", err)
	}

	// Activates platform-events' own consumer-side Prometheus metrics —
	// events_consumed_total, events_consume_duration_seconds, and
	// sqs_receive/delete/visibility_extension_errors_total (the latter two
	// the library's own docs flag as causing duplicate delivery when
	// non-zero, exactly the failure mode this service's processed_events
	// idempotency ledger exists to survive). No other wiring needed:
	// internal/adapter/outbound/sqs's consumer already calls the
	// corresponding Record* functions unconditionally at every relevant
	// point for both queues below — they no-op until Init/InitWithRegisterer
	// runs once, which a prior version of this process never called.
	// InitWithRegisterer (not Init, which always uses
	// prometheus.DefaultRegisterer) to match the explicit-registerer
	// convention above.
	events.InitWithRegisterer("tender-acl", buildVersion, gincommon.MetricsRegisterer())

	checker := membershipcheck.NewHTTPChecker(cfg.CoreInternalBaseURL, nil, cfg.MembershipCheckTimeout)

	repo := aclpostgres.NewTenderACLRepository(pgPool)
	// One ProcessedEvents instance per consumer — the composite (event_id,
	// consumer) key in processed_events discriminates between them, so
	// they safely share the one table.
	offboardingProcessedEvents := consumer.NewProcessedEvents(rawPool, "tenant_lifecycle_cleanup")
	memberRemovalProcessedEvents := consumer.NewProcessedEvents(rawPool, "member_removal")

	svc := service.NewACLService(repo, checker, aclCache, svcMetrics, logger, tracer)
	handler := httpadapter.NewHandler(svc)
	docs := httpadapter.DocsConfig{
		Environment: cfg.Environment,
		Enabled:     cfg.DocsEnabled,
		AuthToken:   cfg.DocsAuthToken,
	}
	router := httpadapter.NewRouter(handler, pgPool, aclCache, logger, tracing, docs)

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

	offboardingConsumer := consumer.NewOffboardingConsumer(repo, offboardingProcessedEvents, svcMetrics, logger, tracer)
	eventsConsumer, err := events.NewSQSConsumer(
		eventsconfig.SQSConfigFromEnv(sqsEnv, mapLogger),
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
	memberRemovalConsumer := consumer.NewMemberRemovalConsumer(repo, memberRemovalProcessedEvents, svcMetrics, logger, tracer)
	memberRemovalEventsConsumer, err := events.NewSQSConsumer(
		events.SQSConfig{
			QueueURL: cfg.MemberRemovalQueueURL,
			Region:   cfg.AWSRegion,
			Logger:   mapLogger,
		},
		memberRemovalConsumer.Handle,
		events.WithConcurrency(getEnvInt("CONSUMER_CONCURRENCY", 4)),
		// See the offboarding consumer above: redrive is member-removal-tenderacl-q's
		// own SQS redrive policy, not an in-process WithDeadLetterHandler.
		//
		// This queue stays hand-rolled rather than going through
		// platform-events/pkg/config like the offboarding consumer above:
		// LoadSQS()/SQSConfigFromEnv/SQSConsumerOptions read one fixed,
		// unparameterized set of env var names (SQS_QUEUE_URL,
		// SQS_CONCURRENCY, ...) with no concept of a second queue, so they
		// can only serve one of this service's two independent
		// subscriptions.
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
