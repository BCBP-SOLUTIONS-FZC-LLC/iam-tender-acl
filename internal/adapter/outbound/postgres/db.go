package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/port"
	pgcommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/pgcommon"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/puddle/v2"
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
// dsn is empty or PG_STATEMENT_TIMEOUT is unset. Idempotent: a DSN that
// already carries statement_timeout is returned unchanged so
// LedgerPoolConfig / MigrationDSNFromEnv can call this on a DSN that
// DSNFromEnv already processed. Mirrors iam-user-profile's and
// iam-org-membership's identical postgres.ApplyStatementTimeout.
func ApplyStatementTimeout(dsn string) string {
	if dsn == "" {
		return dsn
	}
	if t := os.Getenv("PG_STATEMENT_TIMEOUT"); t != "" {
		if d, err := time.ParseDuration(t); err == nil && d > 0 {
			if strings.Contains(dsn, "statement_timeout") {
				return dsn
			}
			dsn += fmt.Sprintf("&options=-c%%20statement_timeout%%3D%d", d.Milliseconds())
		}
	}
	return dsn
}

// LedgerPoolConfig returns pgcommon.Config for a no-GUC pool on dsn. Same
// helper pattern as iam-org-membership's / iam-realm-provisioner's
// SystemPoolConfig: copy ConfigFromEnv pool sizing, lifetimes, and
// SlowQueryThreshold, then clear GUCProvider and force PGBouncerMode true.
// A bare pgcommon.Config{DSN, Logger} literal would leave PGBouncerMode at
// the Go zero-value false and drop ConfigFromEnv pool sizing, breaking
// transaction-pooling deployments even when the app pool correctly forces
// PGBouncerMode.
//
// Production processed_events now shares the RLS-bound app pool so a
// cascade write and MarkProcessed can join one TxRunner transaction
// (IDEMP-2). This helper remains the SystemPoolConfig equivalent for tests
// and any future no-GUC pool. It is not a BYPASSRLS sysPool — tender-acl
// has no reconciler and no SYSTEM_DATABASE_URL. Tracer is left unset; the
// call site wires NewOTelTracer so db.query spans export through
// gincommon's TracerProvider.
func LedgerPoolConfig(dsn string, log port.Logger) pgcommon.Config {
	cfg, _ := pgcommon.ConfigFromEnv()
	cfg.DSN = ApplyStatementTimeout(dsn)
	cfg.GUCProvider = nil
	cfg.PGBouncerMode = true
	// Idle backends under transaction pooling pin session state; siblings
	// helm PG_MIN_CONNS=0 for the same reason. Forced here because
	// PGBouncerMode is forced true regardless of PG_MIN_CONNS env.
	cfg.MinConns = 0
	cfg.Tracer = nil
	if log != nil {
		cfg.Logger = NewLoggerAdapter(log)
	} else {
		cfg.Logger = nil
	}
	return cfg
}

// MigrationDSNFromEnv returns the DSN for schema migrations — the
// tender_acl_migrator (BYPASSRLS) role, LLD §7.4. Migrations must bypass
// PgBouncer because platform-pgcommon's migrate.Runner uses
// pg_advisory_lock, which is session-scoped and breaks under transaction
// pooling. MIGRATION_DATABASE_URL falls back to DSNFromEnv() for local dev,
// where a single-role setup keeps working. ApplyStatementTimeout is applied
// to the explicit migrator DSN as well, matching iam-org-membership /
// iam-realm-provisioner.
func MigrationDSNFromEnv() string {
	if dsn := os.Getenv("MIGRATION_DATABASE_URL"); dsn != "" {
		return ApplyStatementTimeout(dsn)
	}
	return DSNFromEnv()
}

type txKey struct{}

// WithTx stores the active pgx.Tx in ctx so withPool / withTenant join it.
func WithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

// TxFromContext retrieves the active pgx.Tx set by TxRunner.RunInTx.
func TxFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	return tx, ok
}

// TxRunner implements port.TxRunner. No EventPublisher — this service
// publishes zero events (TAC-EVT-1), unlike iam-org-membership /
// iam-realm-provisioner. Contended writes still retry via
// RunInTxWithRetryOpts, and wrapConnErr maps connectivity failures to 503.
type TxRunner struct {
	pool *pgcommon.Pool
}

// NewTxRunner constructs a TxRunner on pool.
func NewTxRunner(pool *pgcommon.Pool) *TxRunner {
	return &TxRunner{pool: pool}
}

var _ port.TxRunner = (*TxRunner)(nil)

// RunInTx runs fn inside a transaction, binds the pgx.Tx into ctx, and
// maps connectivity failures to dependency_unavailable.
func (r *TxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return wrapConnErr(pgcommon.RunInTxWithRetryOpts(ctx, r.pool, pgx.TxOptions{}, writeRetryOpts, func(ctx context.Context, tx pgx.Tx) error {
		return fn(WithTx(ctx, tx))
	}))
}

// withPool runs fn inside a transaction, joining an existing one if present
// in ctx. Used by ProcessedEvents so a mark inside TxRunner.RunInTx commits
// atomically with the cascade write (IDEMP-2 / realm-provisioner tenant
// consumer). Called outside a transaction it opens its own.
func withPool(ctx context.Context, pool *pgcommon.Pool, fn func(pgx.Tx) error) error {
	if tx, ok := TxFromContext(ctx); ok {
		return wrapConnErr(fn(tx))
	}
	return wrapConnErr(pgcommon.RunInTx(ctx, pool, pgx.TxOptions{}, func(_ context.Context, tx pgx.Tx) error {
		return fn(tx)
	}))
}

// wrapConnErr remaps SQLSTATE class 08/53/57/58 (availability failures)
// and puddle.ErrClosedPool to domain.ErrCodeDependencyUnavailable so
// respondACLError surfaces them as 503, matching LLD §12.3/§20.
//
// Everything else — including a plain Go error a caller's own RunInTx
// callback returns for its own business reasons — passes through
// completely unchanged. This function has no way to distinguish "the pool
// itself failed" from "fn's own business logic failed" for any error
// shape beyond the ones positively recognized above, since
// pgcommon.RunInTx returns both shapes identically; defaulting the
// unrecognized case to dependency_unavailable (as this used to) silently
// discarded the caller's real error under a misleading "database
// unavailable" 503 for every unrecognized failure — including deliberate
// business-rule errors a service intentionally returns from inside a
// transaction. respondACLError already re-classifies a leaked raw PgError of
// these same connectivity/resource classes into 503 independently, so a
// genuine low-level connectivity failure that somehow isn't positively
// recognized here still degrades no worse than a generic 500, never a
// masked/wrong business error.
//
// Mirrors iam-org-membership's / iam-realm-provisioner's identical
// wrapConnErr, adapted to this service's flat domain.Error taxonomy
// (ErrCodeDependencyUnavailable rather than ErrDBUnavailable).
func wrapConnErr(err error) error {
	if err == nil {
		return nil
	}
	var de *domain.Error
	if errors.As(err, &de) {
		return err
	}
	if pgcommon.IsConnectionException(err) || pgcommon.IsInsufficientResources(err) || isOperatorOrSystemErrorSQLState(err) || errors.Is(err, puddle.ErrClosedPool) {
		return domain.NewError(domain.ErrCodeDependencyUnavailable, "database unavailable")
	}
	// pgx returns Go-level network errors when the connection is dropped
	// mid-flight (Postgres container stop, ECONNRESET). These are not
	// *pgconn.PgError values so the SQLSTATE checks above miss them —
	// catch them here so callers get 503, not 500. Same helper
	// iam-realm-provisioner's wrapConnErr uses.
	if isNetworkError(err) {
		return domain.NewError(domain.ErrCodeDependencyUnavailable, "database unavailable")
	}
	return err
}

// writeRetryOpts is pgcommon's documented high-throughput OLTP preset.
// Deadlock (40P01) and serialization failure (40001) retry with exponential
// backoff + jitter — same values iam-org-membership / iam-realm-provisioner
// pass to RunInTxWithRetryOpts.
var writeRetryOpts = pgcommon.RetryOptions{
	MaxAttempts:    3,
	InitialWait:    10 * time.Millisecond,
	MaxWait:        500 * time.Millisecond,
	Multiplier:     2.0,
	JitterFraction: 0.25,
}

// isNetworkError reports whether err is a Go-level network/IO failure that
// pgx surfaces when the TCP connection to Postgres is lost mid-flight
// (container stop, network partition, ECONNRESET). These never reach the
// SQLSTATE classification path because pgx never received a protocol
// response — they are unambiguously availability failures (503).
func isNetworkError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	var sysErr syscall.Errno
	if errors.As(err, &sysErr) {
		switch sysErr { //nolint:exhaustive // only transient-connection codes
		case syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.EPIPE, syscall.ETIMEDOUT:
			return true
		}
	}
	msg := err.Error()
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "EOF")
}

// isOperatorOrSystemErrorSQLState reports whether err is a Postgres error
// in SQLSTATE class 57 or 58. pgcommon v1.3.0 has dedicated helpers for
// 08/53 but not these two; we classify via the pgconn Error() text
// ("… (SQLSTATE 57P01)") so callers never import pgconn.
func isOperatorOrSystemErrorSQLState(err error) bool {
	if !pgcommon.IsPgError(err) {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLSTATE 57") || strings.Contains(msg, "SQLSTATE 58")
}
