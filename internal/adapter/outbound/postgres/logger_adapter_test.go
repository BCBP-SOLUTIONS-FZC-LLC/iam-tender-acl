package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/port"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/domain"
)

// capLogger records the last structured log call for assertion.
type capLogger struct {
	level  string
	msg    string
	fields map[string]any
}

func (c *capLogger) Debug(msg string, fields map[string]any) {
	c.level = "debug"
	c.msg = msg
	c.fields = fields
}
func (c *capLogger) Info(msg string, fields map[string]any) {
	c.level = "info"
	c.msg = msg
	c.fields = fields
}
func (c *capLogger) Warn(msg string, fields map[string]any) {
	c.level = "warn"
	c.msg = msg
	c.fields = fields
}
func (c *capLogger) Error(msg string, fields map[string]any) {
	c.level = "error"
	c.msg = msg
	c.fields = fields
}

var _ port.Logger = (*capLogger)(nil)

// ── LoggerAdapter ─────────────────────────────────────────────────────────────

func TestNewLoggerAdapter_ReturnsAdapter(t *testing.T) {
	a := NewLoggerAdapter(&capLogger{})
	assert.NotZero(t, a)
}

func TestLoggerAdapter_Debug(t *testing.T) {
	cl := &capLogger{}
	a := NewLoggerAdapter(cl)
	a.Debug("dmsg", domain.Field{Key: "k", Value: "v"})
	assert.Equal(t, "debug", cl.level)
	assert.Equal(t, "dmsg", cl.msg)
	assert.Equal(t, "v", cl.fields["k"])
}

func TestLoggerAdapter_Info(t *testing.T) {
	cl := &capLogger{}
	a := NewLoggerAdapter(cl)
	a.Info("imsg")
	assert.Equal(t, "info", cl.level)
	assert.Equal(t, "imsg", cl.msg)
}

func TestLoggerAdapter_Warn(t *testing.T) {
	cl := &capLogger{}
	a := NewLoggerAdapter(cl)
	a.Warn("wmsg")
	assert.Equal(t, "warn", cl.level)
}

func TestLoggerAdapter_Error(t *testing.T) {
	cl := &capLogger{}
	a := NewLoggerAdapter(cl)
	a.Error("emsg", domain.Field{Key: "err", Value: "oops"})
	assert.Equal(t, "error", cl.level)
	assert.Equal(t, "oops", cl.fields["err"])
}

// ── fieldMap ──────────────────────────────────────────────────────────────────

func TestFieldMap_Empty(t *testing.T) {
	m := fieldMap(nil)
	assert.Empty(t, m)
}

func TestFieldMap_MultipleFields(t *testing.T) {
	m := fieldMap([]domain.Field{
		{Key: "a", Value: 1},
		{Key: "b", Value: "two"},
	})
	assert.Equal(t, 1, m["a"])
	assert.Equal(t, "two", m["b"])
}
