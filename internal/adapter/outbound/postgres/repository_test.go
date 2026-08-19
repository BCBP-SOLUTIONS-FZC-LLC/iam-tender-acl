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
