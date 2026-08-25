package domain

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDomainError_Error_ReturnsMessage(t *testing.T) {
	e := &Error{Code: "some_code", Message: "something went wrong"}
	assert.Equal(t, "something went wrong", e.Error())
}

func TestNewError_SetsCodeAndMessage(t *testing.T) {
	e := NewError("invalid_request", "bad input")
	require.NotNil(t, e)
	assert.Equal(t, "invalid_request", e.Code)
	assert.Equal(t, "bad input", e.Message)
}

func TestCodeOf_DomainError_ReturnsCodeTrue(t *testing.T) {
	err := NewError(ErrCodeDuplicateGrant, "already granted")
	code, ok := CodeOf(err)
	require.True(t, ok)
	assert.Equal(t, ErrCodeDuplicateGrant, code)
}

func TestCodeOf_WrappedDomainError_ReturnsCodeTrue(t *testing.T) {
	inner := NewError(ErrCodeCoreUnavailable, "downstream down")
	wrapped := fmt.Errorf("layer: %w", inner)
	code, ok := CodeOf(wrapped)
	require.True(t, ok)
	assert.Equal(t, ErrCodeCoreUnavailable, code)
}

func TestCodeOf_NonDomainError_ReturnsFalse(t *testing.T) {
	_, ok := CodeOf(errors.New("plain error"))
	assert.False(t, ok)
}

func TestCodeOf_Nil_ReturnsFalse(t *testing.T) {
	_, ok := CodeOf(nil)
	assert.False(t, ok)
}
