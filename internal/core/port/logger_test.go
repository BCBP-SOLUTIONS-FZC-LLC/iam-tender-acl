package port

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

type fakeLogger struct {
	level  string
	msg    string
	fields map[string]any
}

func (f *fakeLogger) Debug(msg string, fields map[string]any) {
	f.level, f.msg, f.fields = "debug", msg, fields
}
func (f *fakeLogger) Info(msg string, fields map[string]any) {
	f.level, f.msg, f.fields = "info", msg, fields
}
func (f *fakeLogger) Warn(msg string, fields map[string]any) {
	f.level, f.msg, f.fields = "warn", msg, fields
}
func (f *fakeLogger) Error(msg string, fields map[string]any) {
	f.level, f.msg, f.fields = "error", msg, fields
}

var _ Logger = (*fakeLogger)(nil)

func TestNewSlogStyleLogger_WrapsLogger(t *testing.T) {
	fl := &fakeLogger{}
	s := NewSlogStyleLogger(fl)
	s.Info("hello", "k", "v")
	assert.Equal(t, "info", fl.level)
	assert.Equal(t, "hello", fl.msg)
	assert.Equal(t, map[string]any{"k": "v"}, fl.fields)
}

func TestSlogStyleLogger_Debug(t *testing.T) {
	fl := &fakeLogger{}
	s := NewSlogStyleLogger(fl)
	s.Debug("dbg", "a", 1)
	assert.Equal(t, "debug", fl.level)
	assert.Equal(t, map[string]any{"a": 1}, fl.fields)
}

func TestSlogStyleLogger_Warn(t *testing.T) {
	fl := &fakeLogger{}
	s := NewSlogStyleLogger(fl)
	s.Warn("wrn", "a", 1)
	assert.Equal(t, "warn", fl.level)
}

func TestSlogStyleLogger_Error(t *testing.T) {
	fl := &fakeLogger{}
	s := NewSlogStyleLogger(fl)
	s.Error("err", "a", 1)
	assert.Equal(t, "error", fl.level)
}

func TestSlogStyleLogger_With_MergesBaseFields(t *testing.T) {
	fl := &fakeLogger{}
	s := NewSlogStyleLogger(fl).With("tenant_id", "t1")
	s.Info("hello", "k", "v")
	assert.Equal(t, map[string]any{"tenant_id": "t1", "k": "v"}, fl.fields)
}

func TestSlogStyleLogger_With_Chained(t *testing.T) {
	fl := &fakeLogger{}
	s := NewSlogStyleLogger(fl).With("a", 1).With("b", 2)
	s.Info("hello")
	assert.Equal(t, map[string]any{"a": 1, "b": 2}, fl.fields)
}

func TestSlogStyleLogger_DebugContext_NoSpan_NoTraceID(t *testing.T) {
	fl := &fakeLogger{}
	s := NewSlogStyleLogger(fl)
	s.DebugContext(context.Background(), "dbg", "a", 1)
	assert.Equal(t, "debug", fl.level)
	_, ok := fl.fields["trace_id"]
	assert.False(t, ok, "no span in context → no trace_id field")
}

func TestSlogStyleLogger_InfoContext_NoSpan(t *testing.T) {
	fl := &fakeLogger{}
	s := NewSlogStyleLogger(fl)
	s.InfoContext(context.Background(), "info-msg")
	assert.Equal(t, "info", fl.level)
}

func TestSlogStyleLogger_WarnContext_NoSpan(t *testing.T) {
	fl := &fakeLogger{}
	s := NewSlogStyleLogger(fl)
	s.WarnContext(context.Background(), "warn-msg")
	assert.Equal(t, "warn", fl.level)
}

func TestSlogStyleLogger_ErrorContext_WithValidSpan_AttachesTraceID(t *testing.T) {
	fl := &fakeLogger{}
	s := NewSlogStyleLogger(fl)

	traceID, err := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("1112131415161718")
	require.NoError(t, err)
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	s.ErrorContext(ctx, "boom", "k", "v")
	assert.Equal(t, "error", fl.level)
	assert.Equal(t, "0102030405060708090a0b0c0d0e0f10", fl.fields["trace_id"])
	assert.Equal(t, "v", fl.fields["k"])
}

func TestSlogStyleLogger_NilLog_FallsBackToTopLevelSlog(t *testing.T) {
	// The zero value (no Logger injected) must not panic — it falls back
	// to the top-level slog functions.
	var s SlogStyleLogger
	assert.NotPanics(t, func() {
		s.Info("hello", "k", "v")
		s.ErrorContext(context.Background(), "boom")
	})
}

func TestSlogStyleLogger_UnknownLevel_FallsBackToError(t *testing.T) {
	// log4 is unexported and only ever called with the four slog.Level
	// constants above, but its default branch (any other level) must still
	// route somewhere sane rather than silently dropping the log line.
	fl := &fakeLogger{}
	s := NewSlogStyleLogger(fl)
	s.log4(context.Background(), 99, "weird level", nil, false)
	assert.Equal(t, "error", fl.level)
}

func TestKVToFields_PairsAlternatingArgs(t *testing.T) {
	got := kvToFields([]any{"a", 1, "b", "two"})
	assert.Equal(t, map[string]any{"a": 1, "b": "two"}, got)
}

func TestKVToFields_TrailingUnpairedArg_Dropped(t *testing.T) {
	got := kvToFields([]any{"a", 1, "trailing"})
	assert.Equal(t, map[string]any{"a": 1}, got)
}

func TestKVToFields_NonStringKey_Dropped(t *testing.T) {
	got := kvToFields([]any{42, "value", "a", 1})
	assert.Equal(t, map[string]any{"a": 1}, got)
}

func TestKVToFields_Empty(t *testing.T) {
	got := kvToFields(nil)
	assert.Equal(t, map[string]any{}, got)
}
