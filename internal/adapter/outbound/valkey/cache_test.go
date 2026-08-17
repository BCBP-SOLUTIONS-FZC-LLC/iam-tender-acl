package valkey

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// TestKeyFormat locks in the tac:acl:{tenant}:{tender}:{user} key shape
// (LLD §9) — a real Valkey round-trip is exercised by the testcontainers-
// backed integration suite (test/integration), not here.
func TestKeyFormat(t *testing.T) {
	tenantID, tenderID, userID := uuid.New(), uuid.New(), uuid.New()
	got := key(tenantID, tenderID, userID)
	want := "tac:acl:" + tenantID.String() + ":" + tenderID.String() + ":" + userID.String()
	assert.Equal(t, want, got)
}
