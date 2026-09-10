package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"

	pgdomain "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/domain"
)

type recordingPortLogger struct {
	level  string
	msg    string
	fields map[string]any
}

func (f *recordingPortLogger) Debug(msg string, fields map[string]any) {
	f.level, f.msg, f.fields = "debug", msg, fields
}
func (f *recordingPortLogger) Info(msg string, fields map[string]any) {
	f.level, f.msg, f.fields = "info", msg, fields
}
func (f *recordingPortLogger) Warn(msg string, fields map[string]any) {
	f.level, f.msg, f.fields = "warn", msg, fields
}
func (f *recordingPortLogger) Error(msg string, fields map[string]any) {
	f.level, f.msg, f.fields = "error", msg, fields
}

func TestNewLoggerAdapter_WrapsLogger(t *testing.T) {
	fl := &recordingPortLogger{}
	a := NewLoggerAdapter(fl)
	a.Info("hello", pgdomain.Field{Key: "k", Value: "v"})
	assert.Equal(t, "info", fl.level)
	assert.Equal(t, "hello", fl.msg)
	assert.Equal(t, map[string]any{"k": "v"}, fl.fields)
}

func TestLoggerAdapter_Debug(t *testing.T) {
	fl := &recordingPortLogger{}
	a := NewLoggerAdapter(fl)
	a.Debug("dbg", pgdomain.Field{Key: "a", Value: 1})
	assert.Equal(t, "debug", fl.level)
	assert.Equal(t, map[string]any{"a": 1}, fl.fields)
}

func TestLoggerAdapter_Warn(t *testing.T) {
	fl := &recordingPortLogger{}
	a := NewLoggerAdapter(fl)
	a.Warn("wrn", pgdomain.Field{Key: "a", Value: 1})
	assert.Equal(t, "warn", fl.level)
}

func TestLoggerAdapter_Error(t *testing.T) {
	fl := &recordingPortLogger{}
	a := NewLoggerAdapter(fl)
	a.Error("err", pgdomain.Field{Key: "a", Value: 1})
	assert.Equal(t, "error", fl.level)
}

func TestFieldMap_MultipleFields(t *testing.T) {
	got := fieldMap([]pgdomain.Field{{Key: "a", Value: 1}, {Key: "b", Value: "two"}})
	assert.Equal(t, map[string]any{"a": 1, "b": "two"}, got)
}

func TestFieldMap_Empty(t *testing.T) {
	got := fieldMap(nil)
	assert.Equal(t, map[string]any{}, got)
}

// TestLoggerAdapter_Info_NoFields covers Info called with zero variadic
// fields (fieldMap(nil) via the empty-slice path), distinct from
// TestNewLoggerAdapter_WrapsLogger which always passes at least one field.
func TestLoggerAdapter_Info_NoFields(t *testing.T) {
	fl := &recordingPortLogger{}
	a := NewLoggerAdapter(fl)
	a.Info("imsg")
	assert.Equal(t, "info", fl.level)
	assert.Equal(t, "imsg", fl.msg)
}
