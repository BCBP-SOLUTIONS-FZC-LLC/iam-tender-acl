// Command tender-acl runs the Tender ACL Service: the system of record for
// tender_acl_entries, extracted from iam-org-membership per ADR-0007 Wave
// 3 (see tender-acl-service-lld.md). It exposes the tenant-admin
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
	eventsconfig.LogWarningsTo(log, sqsEnv.Warnings)

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
			log.Error("telemetry shutdown failed", map[string]any{"error": shutdownErr.Error()})
		}
	}()

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
	// tunable, so it isn't left to configuration to get right.
	pgCfg.PGBouncerMode = true
	pgCfg.Logger = aclpostgres.NewLoggerAdapter(log)
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
			log.Error("postgres pool drain failed", map[string]any{"error": drainErr.Error()})
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
		Logger:        aclpostgres.NewLoggerAdapter(log),
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
			log.Error("raw postgres pool drain failed", map[string]any{"error": drainErr.Error()})
		}
	}()

	valkeyClient := valkey.NewClient(valkey.ClientConfig{Addr: cfg.ValkeyAddr, Password: cfg.ValkeyPassword})
	defer func() {
		if closeErr := valkeyClient.Close(); closeErr != nil {
			log.Error("valkey client close failed", map[string]any{"error": closeErr.Error()})
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

	svc := service.NewACLService(repo, checker, aclCache, svcMetrics, svcLog, tracer)
	handler := httpadapter.NewHandler(svc)
	docs := httpadapter.DocsConfig{
		Environment: cfg.Environment,
		Enabled:     cfg.DocsEnabled,
		AuthToken:   cfg.DocsAuthToken,
	}
	router := httpadapter.NewRouter(handler, pgPool, aclCache, log, tracing, docs)

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

	offboardingConsumer := consumer.NewOffboardingConsumer(repo, offboardingProcessedEvents, svcMetrics, svcLog, tracer)
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
	memberRemovalConsumer := consumer.NewMemberRemovalConsumer(repo, memberRemovalProcessedEvents, svcMetrics, svcLog, tracer)
	memberRemovalEventsConsumer, err := events.NewSQSConsumer(
		events.SQSConfig{
			QueueURL: cfg.MemberRemovalQueueURL,
			Region:   cfg.AWSRegion,
			Logger:   log,
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

func runProcessedEventsCleanup(ctx context.Context, repo *consumer.ProcessedEvents, interval time.Duration, log port.SlogStyleLogger) {
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
