package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// config holds every environment-variable-driven setting for this process.
// DATABASE_URL and MEMBER_REMOVAL_SQS_QUEUE_URL have no safe default and are
// required. SQS_QUEUE_URL (the tenant-offboarding queue) is validated
// separately in main.go's run(), via platform-events/pkg/config's own
// SQSConfigEnv.Validate() — see the SQSQueueURL removal note below.
type config struct {
	HTTPPort    string
	MetricsPort string

	// DatabaseURL is the tender_acl_app (RLS-bound, NOBYPASSRLS) connection
	// string used by the runtime pool.
	DatabaseURL string
	// MigrationDatabaseURL is the tender_acl_migrator (BYPASSRLS)
	// connection string used only for the startup migration run (LLD
	// §7.4). Falls back to DatabaseURL for local dev.
	MigrationDatabaseURL string

	ValkeyAddr     string
	ValkeyPassword string

	// CoreInternalBaseURL is iam-org-membership's internal base URL (LLD
	// §15: coreInternalBaseURL), consulted only at TAC-2 grant time.
	CoreInternalBaseURL    string
	MembershipCheckTimeout time.Duration

	// MemberRemovalQueueURL backs member-removal-tenderacl-q (ADR-0007 Wave
	// 3 Phase 3) — the per-user-removal ACL cascade, separate from the
	// tenant-offboarding cascade's queue. No
	// field for that one here: SQS_QUEUE_URL and every other SQS_* tunable
	// for it are loaded directly from platform-events/pkg/config.LoadSQS()
	// in main.go instead of being duplicated into this struct — that
	// package has no concept of a second queue, so MemberRemovalQueueURL
	// stays hand-rolled here.
	MemberRemovalQueueURL string
	// AWSRegion is shared by both SQS queues; also duplicated into
	// LoadSQS()'s own SQSConfigEnv.Region for the first (same env var,
	// same default, read twice — not a source of drift).
	AWSRegion string

	OTELExporterOTLPEndpoint string

	LogLevel    string
	Environment string

	ProcessedEventsCleanupInterval time.Duration

	// DocsEnabled opts the Swagger UI docs surface into production (it's
	// always on outside production regardless of this flag). DocsAuthToken,
	// if set, additionally gates it behind a bearer token in production.
	DocsEnabled   bool
	DocsAuthToken string
}

func loadConfig() (config, error) {
	cfg := config{
		HTTPPort:               getEnv("HTTP_PORT", "8080"),
		MetricsPort:            getEnv("METRICS_PORT", "9090"),
		DatabaseURL:            os.Getenv("DATABASE_URL"),
		MigrationDatabaseURL:   getEnv("MIGRATION_DATABASE_URL", os.Getenv("DATABASE_URL")),
		ValkeyAddr:             getEnv("VALKEY_ADDR", "localhost:6379"),
		ValkeyPassword:         os.Getenv("VALKEY_PASSWORD"),
		CoreInternalBaseURL:    getEnv("CORE_INTERNAL_BASE_URL", "http://org-membership.iam.svc.cluster.local"),
		MembershipCheckTimeout: time.Duration(getEnvInt("MEMBERSHIP_CHECK_TIMEOUT_MS", 300)) * time.Millisecond,
		MemberRemovalQueueURL:  os.Getenv("MEMBER_REMOVAL_SQS_QUEUE_URL"),
		AWSRegion:              getEnv("AWS_REGION", "us-east-1"),

		OTELExporterOTLPEndpoint: os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),

		LogLevel:    getEnv("LOG_LEVEL", "info"),
		Environment: getEnv("ENVIRONMENT", "production"),

		ProcessedEventsCleanupInterval: time.Hour,

		DocsEnabled:   getEnv("DOCS_ENABLED", "false") == "true",
		DocsAuthToken: os.Getenv("DOCS_AUTH_TOKEN"),
	}

	if cfg.DatabaseURL == "" {
		return config{}, fmt.Errorf("DATABASE_URL is required")
	}
	// SQS_QUEUE_URL is validated in main.go's run(), via
	// platform-events/pkg/config's own SQSConfigEnv.Validate().
	if cfg.MemberRemovalQueueURL == "" {
		return config{}, fmt.Errorf("MEMBER_REMOVAL_SQS_QUEUE_URL is required")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}
