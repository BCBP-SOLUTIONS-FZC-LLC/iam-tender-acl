package postgres

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ── DSNFromEnv ────────────────────────────────────────────────────────────────

// TestDSNFromEnv_WithDatabaseURL_SkipsStatementTimeout verifies that when
// DATABASE_URL is set, DSNFromEnv returns the DSN verbatim without appending
// statement_timeout (the if-DATABASE_URL branch returns cfg.DSN directly).
func TestDSNFromEnv_WithDatabaseURL_SkipsStatementTimeout(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost/db")
	t.Setenv("PG_STATEMENT_TIMEOUT", "5s")
	result := DSNFromEnv()
	assert.NotContains(t, result, "statement_timeout")
}

// TestDSNFromEnv_WithoutDatabaseURL_NoTimeout verifies no panic when both vars
// are absent (result is effectively empty / derived from empty env).
func TestDSNFromEnv_WithoutDatabaseURL_NoTimeout(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("PG_STATEMENT_TIMEOUT", "")
	assert.NotPanics(t, func() { DSNFromEnv() })
}

// ── ApplyStatementTimeout ─────────────────────────────────────────────────────

func TestApplyStatementTimeout_EmptyDSN(t *testing.T) {
	result := ApplyStatementTimeout("")
	assert.Equal(t, "", result)
}

func TestApplyStatementTimeout_NoPGStatementTimeout(t *testing.T) {
	t.Setenv("PG_STATEMENT_TIMEOUT", "")
	dsn := "postgres://localhost/db"
	result := ApplyStatementTimeout(dsn)
	assert.Equal(t, dsn, result)
}

func TestApplyStatementTimeout_ValidTimeout(t *testing.T) {
	t.Setenv("PG_STATEMENT_TIMEOUT", "5s")
	dsn := "postgres://localhost/db"
	result := ApplyStatementTimeout(dsn)
	assert.True(t, strings.HasPrefix(result, dsn), "result should start with original dsn")
	assert.Contains(t, result, "statement_timeout")
	assert.Contains(t, result, "5000") // 5s = 5000 ms
}

func TestApplyStatementTimeout_InvalidTimeout(t *testing.T) {
	t.Setenv("PG_STATEMENT_TIMEOUT", "not-a-duration")
	dsn := "postgres://localhost/db"
	result := ApplyStatementTimeout(dsn)
	assert.Equal(t, dsn, result)
}

func TestApplyStatementTimeout_ZeroDuration(t *testing.T) {
	t.Setenv("PG_STATEMENT_TIMEOUT", "0s")
	dsn := "postgres://localhost/db"
	result := ApplyStatementTimeout(dsn)
	// d > 0 check fails for zero duration — dsn returned unchanged.
	assert.Equal(t, dsn, result)
}

// ── MigrationDSNFromEnv ───────────────────────────────────────────────────────

func TestMigrationDSNFromEnv_WithMigrationURL(t *testing.T) {
	t.Setenv("MIGRATION_DATABASE_URL", "postgres://migrator@localhost/db")
	result := MigrationDSNFromEnv()
	assert.Equal(t, "postgres://migrator@localhost/db", result)
}

// TestMigrationDSNFromEnv_WithoutMigrationURL falls back to DSNFromEnv.  When
// DATABASE_URL is set that branch returns cfg.DSN directly (no
// statement_timeout), so the result must not contain "statement_timeout".
func TestMigrationDSNFromEnv_WithoutMigrationURL(t *testing.T) {
	t.Setenv("MIGRATION_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "postgres://app@localhost/db")
	result := MigrationDSNFromEnv()
	assert.NotContains(t, result, "statement_timeout")
}
