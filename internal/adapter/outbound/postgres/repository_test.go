package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRows implements pgx.Rows minimally — only Next and Scan are called by
// collectEntries. Forcing a Scan error this way is the only practical route
// to that branch: a real Postgres row for this schema's well-typed columns
// cannot itself produce a scan error.
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
