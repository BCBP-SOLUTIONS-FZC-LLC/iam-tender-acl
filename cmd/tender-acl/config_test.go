package main

import "testing"

func TestGetEnvInt_Unset_ReturnsFallback(t *testing.T) {
	v, err := getEnvInt("TAC_TEST_UNSET_INT_VAR", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 42 {
		t.Fatalf("got %d, want fallback 42", v)
	}
}

func TestGetEnvInt_ValidValue_ReturnsParsedInt(t *testing.T) {
	t.Setenv("TAC_TEST_VALID_INT_VAR", "123")
	v, err := getEnvInt("TAC_TEST_VALID_INT_VAR", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 123 {
		t.Fatalf("got %d, want 123", v)
	}
}

// TestGetEnvInt_MalformedValue_ReturnsError guards against a typo'd
// operator-set env var silently resolving to the fallback instead of
// aborting startup loudly.
func TestGetEnvInt_MalformedValue_ReturnsError(t *testing.T) {
	t.Setenv("TAC_TEST_MALFORMED_INT_VAR", "not-an-int")
	_, err := getEnvInt("TAC_TEST_MALFORMED_INT_VAR", 42)
	if err == nil {
		t.Fatal("expected an error for a malformed integer env var, got nil")
	}
}

func TestLoadConfig_RequiredEnvVarsSet_MalformedMembershipCheckTimeout_Fails(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/tender_acl?sslmode=disable")
	t.Setenv("MEMBER_REMOVAL_SQS_QUEUE_URL", "https://sqs.example.com/000000000000/member-removal-tenderacl-q")
	t.Setenv("MEMBERSHIP_CHECK_TIMEOUT_MS", "not-a-number")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected loadConfig to fail fast on a malformed MEMBERSHIP_CHECK_TIMEOUT_MS, got nil error")
	}
}

func TestLoadConfig_MissingDatabaseURL_Fails(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("MEMBER_REMOVAL_SQS_QUEUE_URL", "https://sqs.example.com/000000000000/member-removal-tenderacl-q")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected loadConfig to fail fast on a missing DATABASE_URL, got nil error")
	}
}

func TestLoadConfig_MissingMemberRemovalQueueURL_Fails(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/tender_acl?sslmode=disable")
	t.Setenv("MEMBER_REMOVAL_SQS_QUEUE_URL", "")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected loadConfig to fail fast on a missing MEMBER_REMOVAL_SQS_QUEUE_URL, got nil error")
	}
}

func TestLoadConfig_DocsEnabled_IsCaseInsensitive(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/tender_acl?sslmode=disable")
	t.Setenv("MEMBER_REMOVAL_SQS_QUEUE_URL", "https://sqs.example.com/000000000000/member-removal-tenderacl-q")
	t.Setenv("DOCS_ENABLED", "TRUE")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.DocsEnabled {
		t.Fatal("expected DOCS_ENABLED=TRUE to be treated as enabled")
	}
}
