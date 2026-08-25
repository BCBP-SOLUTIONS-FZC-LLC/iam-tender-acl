package postgres

import (
	"fmt"
	"os"
	"time"

	pgcommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/pgcommon"
)

// DSNFromEnv builds a PostgreSQL connection URL for the application pool by
// delegating host/port/user/password/dbname/sslmode parsing and DSN assembly
// to pgcommon.ConfigFromEnv() — the same env vars (DATABASE_URL/PG_HOST/
// PG_PORT/PG_USER/PG_PASSWORD/PG_DBNAME/PG_SSLMODE) platform-pgcommon itself
// reads to build the pool Config used by cmd/tender-acl/main.go, so there is
// exactly one DSN-assembly implementation instead of two drifting in
// parallel. Mirrors iam-user-profile's and iam-org-membership's identical
// postgres.DSNFromEnv. Warnings from ConfigFromEnv are surfaced at the call
// site that owns a logger (cmd/tender-acl/main.go); this helper only returns
// the DSN string.
func DSNFromEnv() string {
	cfg, _ := pgcommon.ConfigFromEnv()
	if os.Getenv("DATABASE_URL") != "" {
		// DATABASE_URL is returned verbatim by pgcommon.ConfigFromEnv — set
		// statement_timeout via its own query string, not appended here.
		return cfg.DSN
	}
	return ApplyStatementTimeout(cfg.DSN)
}

// ApplyStatementTimeout appends a server-side statement_timeout option to dsn
// so a hung query releases its pool connection instead of holding it for the
// full request lifetime. PG_STATEMENT_TIMEOUT accepts a Go duration string
// (e.g. "5s", "500ms"). This has no pgcommon equivalent — pgcommon.Config has
// no statement-timeout field — so it remains a small extension layered on
// top of the pgcommon-built DSN rather than a full DSN builder. Ignored when
// dsn is empty or PG_STATEMENT_TIMEOUT is unset. Mirrors iam-user-profile's
// and iam-org-membership's identical postgres.ApplyStatementTimeout.
func ApplyStatementTimeout(dsn string) string {
	if dsn == "" {
		return dsn
	}
	if t := os.Getenv("PG_STATEMENT_TIMEOUT"); t != "" {
		if d, err := time.ParseDuration(t); err == nil && d > 0 {
			dsn += fmt.Sprintf("&options=-c%%20statement_timeout%%3D%d", d.Milliseconds())
		}
	}
	return dsn
}

// MigrationDSNFromEnv returns the DSN for schema migrations — the
// tender_acl_migrator (BYPASSRLS) role, LLD §7.4. Migrations must bypass
// PgBouncer because platform-pgcommon's migrate.Runner uses
// pg_advisory_lock, which is session-scoped and breaks under transaction
// pooling. MIGRATION_DATABASE_URL falls back to DSNFromEnv() for local dev,
// where a single-role setup keeps working. Mirrors iam-user-profile's and
// iam-org-membership's identical postgres.MigrationDSNFromEnv.
func MigrationDSNFromEnv() string {
	if dsn := os.Getenv("MIGRATION_DATABASE_URL"); dsn != "" {
		return dsn
	}
	return DSNFromEnv()
}
