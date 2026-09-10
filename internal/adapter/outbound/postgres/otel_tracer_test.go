package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewOTelTracer_StartSpan_DoesNotPanic(t *testing.T) {
	tr := NewOTelTracer("tender-acl-test")
	ctx, end := tr.StartSpan(context.Background(), "db.query")
	assert.NotNil(t, ctx)
	assert.NotPanics(t, end)
}

func TestNewOTelTracer_EmptyServiceName_Defaults(t *testing.T) {
	tr := NewOTelTracer("")
	assert.NotNil(t, tr)
	ctx, end := tr.StartSpan(context.Background(), "db.query")
	assert.NotNil(t, ctx)
	end()
}
