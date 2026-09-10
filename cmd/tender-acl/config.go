package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	aclpostgres "github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/outbound/postgres"
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
	// string used by the runtime pool — aclpostgres.DSNFromEnv(), so DSN
	// assembly and PG_STATEMENT_TIMEOUT application go through the same
	// single implementation iam-user-profile/iam-org-membership use,
	// instead of a second one hand-rolled here.
	DatabaseURL string
	// MigrationDatabaseURL is the tender_acl_migrator (BYPASSRLS)
	// connection string used only for the startup migration run (LLD
	// §7.4) — aclpostgres.MigrationDSNFromEnv(), which falls back to
	// DSNFromEnv() for local dev.
	MigrationDatabaseURL string

	ValkeyAddr     string
	ValkeyPassword string

	// CoreInternalBaseURL is iam-org-membership's internal base URL (LLD
	// §15: coreInternalBaseURL), consulted only at TAC-2 grant time.
	CoreInternalBaseURL    string
	MembershipCheckTimeout time.Duration

	// MemberRemovalQueueURL backs member-removal-tenderacl-q (ADR-0007 Wave
	// 3 Phase 3) — the per-user-removal ACL cascade, separate from the
	// tenant-offboarding cascade's queue. SQS_QUEUE_URL and every other
	// SQS_* tunable for queue #1 are loaded from platform-events/pkg/config
	// LoadSQS(); main.go clones that env and overrides QueueURL +
	// concurrency so queue #2 still goes through SQSConfigFromEnv /
	// SQSConsumerOptions. Only the second queue's URL lives here.
	MemberRemovalQueueURL string

	Environment string

	ProcessedEventsCleanupInterval time.Duration

	// DocsEnabled opts the Swagger UI docs surface into production (it's
	// always on outside production regardless of this flag). DocsAuthToken,
	// if set, additionally gates it behind a bearer token in production.
	DocsEnabled   bool
	DocsAuthToken string
}

func loadConfig() (config, error) {
	membershipCheckTimeoutMS, err := getEnvInt("MEMBERSHIP_CHECK_TIMEOUT_MS", 300)
	if err != nil {
		return config{}, err
	}

	cfg := config{
		HTTPPort:               getEnv("HTTP_PORT", "8080"),
		MetricsPort:            getEnv("METRICS_PORT", "9090"),
		DatabaseURL:            aclpostgres.DSNFromEnv(),
		MigrationDatabaseURL:   aclpostgres.MigrationDSNFromEnv(),
		ValkeyAddr:             getEnv("VALKEY_ADDR", "localhost:6379"),
		ValkeyPassword:         os.Getenv("VALKEY_PASSWORD"),
		CoreInternalBaseURL:    getEnv("CORE_INTERNAL_BASE_URL", "http://org-membership.iam.svc.cluster.local"),
		MembershipCheckTimeout: time.Duration(membershipCheckTimeoutMS) * time.Millisecond,
		MemberRemovalQueueURL:  os.Getenv("MEMBER_REMOVAL_SQS_QUEUE_URL"),

		Environment: getEnv("ENVIRONMENT", "production"),

		ProcessedEventsCleanupInterval: time.Hour,

		// strings.EqualFold rather than =="true": DOCS_ENABLED=TRUE/True
		// must not silently resolve to disabled.
		DocsEnabled:   strings.EqualFold(getEnv("DOCS_ENABLED", "false"), "true"),
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

// getEnvInt returns fallback when key is unset, but fails loudly (rather
// than silently falling back) when key IS set to something that doesn't
// parse as an integer — a typo'd operator-set env var should abort startup,
// not silently resolve to a default the operator never sees.
func getEnvInt(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid integer %q: %w", key, raw, err)
	}
	return v, nil
}
