package postgres

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/puddle/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/port"
)

// ── DSNFromEnv ─────────────────────────────────────────────────────────

func TestDSNFromEnv_UsesDatabaseURLShortcutWhenSet(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x:y@override.example:5432/appdb?sslmode=disable")
	assert.Equal(t,
		"postgres://x:y@override.example:5432/appdb?sslmode=disable",
		DSNFromEnv(),
		"DATABASE_URL shortcut short-circuits before any component parsing")
}

func TestDSNFromEnv_BuildsFromComponents(t *testing.T) {
	_ = os.Unsetenv("DATABASE_URL")
	_ = os.Unsetenv("PG_STATEMENT_TIMEOUT")
	t.Setenv("PG_HOST", "db.internal")
	t.Setenv("PG_PORT", "6432")
	t.Setenv("PG_USER", "tender_acl_app")
	t.Setenv("PG_PASSWORD", "secret")
	t.Setenv("PG_DBNAME", "tender_acl")
	t.Setenv("PG_SSLMODE", "require")

	dsn := DSNFromEnv()
	assert.True(t, strings.HasPrefix(dsn, "postgres://"))
	assert.Contains(t, dsn, "tender_acl_app:secret")
	assert.Contains(t, dsn, "@db.internal:6432/tender_acl")
	assert.Contains(t, dsn, "sslmode=require")
}

func TestDSNFromEnv_AppendsStatementTimeoutWhenSet(t *testing.T) {
	_ = os.Unsetenv("DATABASE_URL")
	t.Setenv("PG_USER", "tender_acl_app")
	t.Setenv("PG_DBNAME", "tender_acl")
	t.Setenv("PG_STATEMENT_TIMEOUT", "5s")
	dsn := DSNFromEnv()
	assert.Contains(t, dsn, "statement_timeout%3D5000",
		"5s must translate to statement_timeout=5000 in URL-encoded options")
}

func TestDSNFromEnv_InvalidStatementTimeoutIgnored(t *testing.T) {
	_ = os.Unsetenv("DATABASE_URL")
	t.Setenv("PG_USER", "tender_acl_app")
	t.Setenv("PG_DBNAME", "tender_acl")
	t.Setenv("PG_STATEMENT_TIMEOUT", "not-a-duration")
	dsn := DSNFromEnv()
	assert.NotContains(t, dsn, "statement_timeout",
		"invalid duration must silently fall through, not crash the process")
}

func TestDSNFromEnv_ZeroStatementTimeoutIgnored(t *testing.T) {
	_ = os.Unsetenv("DATABASE_URL")
	t.Setenv("PG_USER", "tender_acl_app")
	t.Setenv("PG_DBNAME", "tender_acl")
	t.Setenv("PG_STATEMENT_TIMEOUT", "0s")
	dsn := DSNFromEnv()
	assert.NotContains(t, dsn, "statement_timeout",
		"zero timeout is a no-op — the code requires d > 0")
}

// ── MigrationDSNFromEnv ────────────────────────────────────────────────

func TestMigrationDSNFromEnv_UsesMigrationVarWhenSet(t *testing.T) {
	_ = os.Unsetenv("PG_STATEMENT_TIMEOUT")
	t.Setenv("MIGRATION_DATABASE_URL", "postgres://m:x@migrations.example:5432/tender_acl")
	assert.Equal(t,
		"postgres://m:x@migrations.example:5432/tender_acl",
		MigrationDSNFromEnv())
}

func TestMigrationDSNFromEnv_AppliesStatementTimeout(t *testing.T) {
	t.Setenv("MIGRATION_DATABASE_URL", "postgres://m:x@migrations.example:5432/tender_acl?sslmode=disable")
	t.Setenv("PG_STATEMENT_TIMEOUT", "5s")
	assert.Contains(t, MigrationDSNFromEnv(), "statement_timeout%3D5000")
}

func TestMigrationDSNFromEnv_FallsBackToDSNFromEnv(t *testing.T) {
	_ = os.Unsetenv("MIGRATION_DATABASE_URL")
	t.Setenv("DATABASE_URL", "postgres://a:b@app.example:5432/tender_acl")
	assert.Equal(t, DSNFromEnv(), MigrationDSNFromEnv(),
		"unset MIGRATION_DATABASE_URL falls through to the app DSN")
}

// ── LedgerPoolConfig ───────────────────────────────────────────────────

func TestLedgerPoolConfig_ForcesPGBouncerMode(t *testing.T) {
	t.Setenv("PG_BOUNCER_MODE", "false")
	t.Setenv("PG_MAX_CONNS", "20")
	t.Setenv("PG_MIN_CONNS", "2")
	t.Setenv("PG_SLOW_QUERY_THRESHOLD", "200ms")
	_ = os.Unsetenv("PG_STATEMENT_TIMEOUT")

	cfg := LedgerPoolConfig("postgres://ledger@host/db", nil)
	assert.Equal(t, "postgres://ledger@host/db", cfg.DSN)
	assert.True(t, cfg.PGBouncerMode, "ledger pool must force PGBouncerMode:true — zero-value false breaks PgBouncer txn pooling")
	assert.Equal(t, int32(0), cfg.MinConns, "ledger pool must force MinConns:0 under transaction pooling")
	assert.Nil(t, cfg.GUCProvider, "ledger pool must not inject tenant GUCs")
	assert.Nil(t, cfg.Tracer, "Tracer is wired by the call site, not LedgerPoolConfig")
	assert.Nil(t, cfg.Logger, "nil log must leave Logger unset")
	assert.Equal(t, int32(20), cfg.MaxConns, "ledger pool inherits pool sizing from ConfigFromEnv")
}

func TestLedgerPoolConfig_NonNilLoggerWiresAdapter(t *testing.T) {
	_ = os.Unsetenv("PG_STATEMENT_TIMEOUT")
	cfg := LedgerPoolConfig("postgres://ledger@host/db", &fakePortLogger{})
	assert.NotNil(t, cfg.Logger, "non-nil port.Logger must be wrapped via NewLoggerAdapter")
}

func TestLedgerPoolConfig_AppliesStatementTimeout(t *testing.T) {
	t.Setenv("PG_STATEMENT_TIMEOUT", "5s")
	cfg := LedgerPoolConfig("postgres://ledger@host/db?sslmode=disable", nil)
	assert.Contains(t, cfg.DSN, "statement_timeout%3D5000")
}

func TestApplyStatementTimeout_Idempotent(t *testing.T) {
	t.Setenv("PG_STATEMENT_TIMEOUT", "5s")
	once := ApplyStatementTimeout("postgres://u@h/db?sslmode=disable")
	assert.Equal(t, once, ApplyStatementTimeout(once))
}

// ── wrapConnErr ──────────────────────────────────────────────────────────

func TestWrapConnErr_NilError_ReturnsNil(t *testing.T) {
	assert.NoError(t, wrapConnErr(nil))
}

func TestWrapConnErr_DomainError_PassesThrough(t *testing.T) {
	de := domain.NewError(domain.ErrCodeDuplicateGrant, "already granted")
	err := wrapConnErr(de)
	code, ok := domain.CodeOf(err)
	require.True(t, ok)
	assert.Equal(t, domain.ErrCodeDuplicateGrant, code)
}

func TestWrapConnErr_ConstraintPgError_PassesThrough(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "23505"}
	err := wrapConnErr(pgErr)
	assert.Same(t, pgErr, err) //nolint:errorlint // asserting identity, not just chain membership
}

func TestWrapConnErr_ErrNoRows_PassesThrough(t *testing.T) {
	err := wrapConnErr(pgx.ErrNoRows)
	assert.ErrorIs(t, err, pgx.ErrNoRows)
	_, ok := domain.CodeOf(err)
	assert.False(t, ok, "must not be classified as a domain error")
}

func TestWrapConnErr_ContextCanceled_PassesThrough(t *testing.T) {
	err := wrapConnErr(context.Canceled)
	assert.ErrorIs(t, err, context.Canceled)
	_, ok := domain.CodeOf(err)
	assert.False(t, ok)
}

func TestWrapConnErr_ContextDeadlineExceeded_PassesThrough(t *testing.T) {
	err := wrapConnErr(context.DeadlineExceeded)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	_, ok := domain.CodeOf(err)
	assert.False(t, ok)
}

func TestWrapConnErr_AvailabilitySQLState_MapsToDependencyUnavailable(t *testing.T) {
	for _, code := range []string{"08006", "53300", "57P01", "58030"} {
		t.Run(code, func(t *testing.T) {
			got := wrapConnErr(&pgconn.PgError{Code: code})
			gotCode, ok := domain.CodeOf(got)
			require.True(t, ok)
			assert.Equal(t, domain.ErrCodeDependencyUnavailable, gotCode)
		})
	}
}

func TestWrapConnErr_ClosedPool_MapsToDependencyUnavailable(t *testing.T) {
	got := wrapConnErr(puddle.ErrClosedPool)
	code, ok := domain.CodeOf(got)
	require.True(t, ok)
	assert.Equal(t, domain.ErrCodeDependencyUnavailable, code)
}

func TestWrapConnErr_NetworkError_MapsToDependencyUnavailable(t *testing.T) {
	for _, err := range []error{
		io.EOF,
		io.ErrUnexpectedEOF,
		&net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")},
		syscall.ECONNRESET,
		syscall.ECONNREFUSED,
		errors.New("dial tcp: connection refused"),
	} {
		t.Run(err.Error(), func(t *testing.T) {
			got := wrapConnErr(err)
			code, ok := domain.CodeOf(got)
			require.True(t, ok, "network error %v must map to dependency_unavailable", err)
			assert.Equal(t, domain.ErrCodeDependencyUnavailable, code)
		})
	}
}

func TestApplyStatementTimeout_EmptyDSNPassesThrough(t *testing.T) {
	t.Setenv("PG_STATEMENT_TIMEOUT", "5s")
	assert.Empty(t, ApplyStatementTimeout(""))
}

func TestApplyStatementTimeout_UnsetEnvLeavesDSNUnchanged(t *testing.T) {
	_ = os.Unsetenv("PG_STATEMENT_TIMEOUT")
	dsn := "postgres://u@h/db?sslmode=disable"
	assert.Equal(t, dsn, ApplyStatementTimeout(dsn))
}

// CRITICAL: an unrecognized plain Go error — the shape a caller's own
// RunInTx callback returns for its own business reasons — must pass
// through completely unchanged, not get silently reclassified as
// dependency_unavailable. wrapConnErr has no way to distinguish "the pool
// itself failed" from "fn's own business logic failed" for anything it
// can't positively identify as a connectivity/resource failure.
func TestWrapConnErr_UnrecognizedGenericError_PassesThroughUnchanged(t *testing.T) {
	businessErr := errors.New("grant already exists for this tender")
	got := wrapConnErr(businessErr)
	assert.Same(t, businessErr, got)
	_, ok := domain.CodeOf(got)
	assert.False(t, ok)
}

type fakePortLogger struct{}

func (l *fakePortLogger) Debug(_ string, _ map[string]any) {}
func (l *fakePortLogger) Info(_ string, _ map[string]any)  {}
func (l *fakePortLogger) Warn(_ string, _ map[string]any)  {}
func (l *fakePortLogger) Error(_ string, _ map[string]any) {}

var _ port.Logger = (*fakePortLogger)(nil)
