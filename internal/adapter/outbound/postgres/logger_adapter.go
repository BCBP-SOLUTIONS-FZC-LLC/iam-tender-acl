package postgres

import (
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/port"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/domain"
)

// LoggerAdapter implements platform-pgcommon's pkg/domain.Logger (Debug/
// Info/Warn/Error(msg, ...domain.Field)) on top of a port.Logger. Wired into
// pgcommon.Config.Logger and RunMigrations so slow-query logging and
// migration log output are routed into the same Zap-backed sink
// (platform-gincommon's logger.NewLogger, built once in cmd/tender-acl/
// main.go) as every other structured log line in this process, instead of
// going nowhere. Mirrors iam-user-profile's and iam-org-membership's
// identical postgres.LoggerAdapter.
type LoggerAdapter struct {
	log port.Logger
}

var _ domain.Logger = LoggerAdapter{}

// NewLoggerAdapter wraps log as a pgcommon domain.Logger.
func NewLoggerAdapter(log port.Logger) LoggerAdapter {
	return LoggerAdapter{log: log}
}

// Debug logs at debug level with structured fields.
func (a LoggerAdapter) Debug(msg string, fields ...domain.Field) { a.log.Debug(msg, fieldMap(fields)) }

// Info logs at info level with structured fields.
func (a LoggerAdapter) Info(msg string, fields ...domain.Field) { a.log.Info(msg, fieldMap(fields)) }

// Warn logs at warn level with structured fields.
func (a LoggerAdapter) Warn(msg string, fields ...domain.Field) { a.log.Warn(msg, fieldMap(fields)) }

// Error logs at error level with structured fields.
func (a LoggerAdapter) Error(msg string, fields ...domain.Field) { a.log.Error(msg, fieldMap(fields)) }

func fieldMap(fields []domain.Field) map[string]any {
	m := make(map[string]any, len(fields))
	for _, f := range fields {
		m[f.Key] = f.Value
	}
	return m
}
