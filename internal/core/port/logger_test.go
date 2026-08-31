package port

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// captureLogger records the last call made to it, so tests can assert on
// level, message, and fields without any real I/O.
type captureLogger struct {
	level  string
	msg    string
	fields map[string]any
}

func (cl *captureLogger) Debug(msg string, fields map[string]any) {
	cl.level = "debug"
	cl.msg = msg
	cl.fields = fields
}
func (cl *captureLogger) Info(msg string, fields map[string]any) {
	cl.level = "info"
	cl.msg = msg
	cl.fields = fields
}
func (cl *captureLogger) Warn(msg string, fields map[string]any) {
	cl.level = "warn"
	cl.msg = msg
	cl.fields = fields
}
func (cl *captureLogger) Error(msg string, fields map[string]any) {
	cl.level = "error"
	cl.msg = msg
	cl.fields = fields
}

// ── SlogStyleLogger routing ───────────────────────────────────────────────────

func TestNewSlogStyleLogger_RoutesDebug(t *testing.T) {
	cl := &captureLogger{}
	s := NewSlogStyleLogger(cl)
	s.Debug("dmsg", "k", "v")
	assert.Equal(t, "debug", cl.level)
	assert.Equal(t, "dmsg", cl.msg)
	assert.Equal(t, "v", cl.fields["k"])
}

func TestSlogStyleLogger_Info(t *testing.T) {
	cl := &captureLogger{}
	s := NewSlogStyleLogger(cl)
	s.Info("imsg", "x", 42)
	assert.Equal(t, "info", cl.level)
	assert.Equal(t, "imsg", cl.msg)
	assert.Equal(t, 42, cl.fields["x"])
}

func TestSlogStyleLogger_Warn(t *testing.T) {
	cl := &captureLogger{}
	s := NewSlogStyleLogger(cl)
	s.Warn("wmsg")
	assert.Equal(t, "warn", cl.level)
}

func TestSlogStyleLogger_Error(t *testing.T) {
	cl := &captureLogger{}
	s := NewSlogStyleLogger(cl)
	s.Error("emsg")
	assert.Equal(t, "error", cl.level)
}

func TestSlogStyleLogger_DebugContext(t *testing.T) {
	cl := &captureLogger{}
	s := NewSlogStyleLogger(cl)
	s.DebugContext(context.Background(), "dmsg")
	assert.Equal(t, "debug", cl.level)
}

func TestSlogStyleLogger_InfoContext(t *testing.T) {
	cl := &captureLogger{}
	s := NewSlogStyleLogger(cl)
	s.InfoContext(context.Background(), "imsg")
	assert.Equal(t, "info", cl.level)
}

func TestSlogStyleLogger_WarnContext(t *testing.T) {
	cl := &captureLogger{}
	s := NewSlogStyleLogger(cl)
	s.WarnContext(context.Background(), "wmsg")
	assert.Equal(t, "warn", cl.level)
}

func TestSlogStyleLogger_ErrorContext(t *testing.T) {
	cl := &captureLogger{}
	s := NewSlogStyleLogger(cl)
	s.ErrorContext(context.Background(), "emsg")
	assert.Equal(t, "error", cl.level)
}

// ── Zero-value (nil log) — must not panic ─────────────────────────────────────

func TestSlogStyleLogger_NilLogger_Debug(t *testing.T) {
	var s SlogStyleLogger
	assert.NotPanics(t, func() { s.Debug("x") })
}

func TestSlogStyleLogger_NilLogger_Info(t *testing.T) {
	var s SlogStyleLogger
	assert.NotPanics(t, func() { s.Info("x") })
}

func TestSlogStyleLogger_NilLogger_Warn(t *testing.T) {
	var s SlogStyleLogger
	assert.NotPanics(t, func() { s.Warn("x") })
}

func TestSlogStyleLogger_NilLogger_Error(t *testing.T) {
	var s SlogStyleLogger
	assert.NotPanics(t, func() { s.Error("x") })
}

func TestSlogStyleLogger_NilLogger_InfoContext(t *testing.T) {
	var s SlogStyleLogger
	assert.NotPanics(t, func() { s.InfoContext(context.Background(), "x") })
}

// ── With — prepend base fields ────────────────────────────────────────────────

func TestSlogStyleLogger_With_PrependBaseFields(t *testing.T) {
	cl := &captureLogger{}
	s := NewSlogStyleLogger(cl).With("base", "bval")
	s.Info("msg", "extra", "eval")
	assert.Equal(t, "bval", cl.fields["base"])
	assert.Equal(t, "eval", cl.fields["extra"])
}

// ── OTel trace_id injection ───────────────────────────────────────────────────

func TestSlogStyleLogger_ContextWithValidSpan_AttachesTraceID(t *testing.T) {
	cl := &captureLogger{}
	s := NewSlogStyleLogger(cl)

	tp := sdktrace.NewTracerProvider()
	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "op")
	defer span.End()

	s.InfoContext(ctx, "traced")
	assert.Contains(t, cl.fields, "trace_id", "expected trace_id field when span is valid")
}

// ── default: branch (slog.Level not matching any case) ───────────────────────

func TestSlogStyleLogger_DefaultLevel_RoutesToError(t *testing.T) {
	cl := &captureLogger{}
	s := NewSlogStyleLogger(cl)
	// slog.Level(100) does not match Debug/Info/Warn/Error — hits default: branch.
	s.log4(context.Background(), slog.Level(100), "msg", nil, false)
	assert.Equal(t, "error", cl.level)
}

// ── kvToFields ────────────────────────────────────────────────────────────────

func TestKvToFields_Empty(t *testing.T) {
	m := kvToFields(nil)
	assert.Empty(t, m)
}

func TestKvToFields_Pairs(t *testing.T) {
	m := kvToFields([]any{"a", 1, "b", "two"})
	assert.Equal(t, 1, m["a"])
	assert.Equal(t, "two", m["b"])
}

func TestKvToFields_OddArgs_LastKeyDropped(t *testing.T) {
	m := kvToFields([]any{"a", 1, "dangling"})
	assert.Equal(t, 1, m["a"])
	_, present := m["dangling"]
	assert.False(t, present)
}

func TestKvToFields_NonStringKey_Dropped(t *testing.T) {
	m := kvToFields([]any{42, "val"})
	assert.Empty(t, m)
}
