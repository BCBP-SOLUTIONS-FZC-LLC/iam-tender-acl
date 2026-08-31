package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
)

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

func TestWrapConnErr_PgError_PassesThrough(t *testing.T) {
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

func TestWrapConnErr_UnknownError_WrapsDependencyUnavailable(t *testing.T) {
	err := wrapConnErr(errors.New("connection refused"))
	code, ok := domain.CodeOf(err)
	require.True(t, ok)
	assert.Equal(t, domain.ErrCodeDependencyUnavailable, code)
}

// mockRows implements pgx.Rows minimally — only Next and Scan are called by collectEntries.
type mockRows struct {
	called bool
}

func (r *mockRows) Next() bool {
	if !r.called {
		r.called = true
		return true
	}
	return false
}
func (r *mockRows) Scan(_ ...any) error                          { return errors.New("mock scan error") }
func (r *mockRows) Err() error                                   { return nil }
func (r *mockRows) Close()                                       {}
func (r *mockRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *mockRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *mockRows) Values() ([]any, error)                       { return nil, nil }
func (r *mockRows) RawValues() [][]byte                          { return nil }
func (r *mockRows) Conn() *pgx.Conn                              { return nil }

func TestCollectEntries_ScanError(t *testing.T) {
	_, err := collectEntries(&mockRows{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scan tender_acl_entries row")
}
