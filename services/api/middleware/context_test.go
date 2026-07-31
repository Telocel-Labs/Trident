package middleware

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestWithAPIKeyIDRoundTrip(t *testing.T) {
	id := uuid.NewString()
	ctx := WithAPIKeyID(context.Background(), id)

	if got := APIKeyIDFromContext(ctx); got != id {
		t.Errorf("APIKeyIDFromContext = %q, want %q", got, id)
	}
}

func TestAPIKeyIDFromContext_Empty(t *testing.T) {
	if got := APIKeyIDFromContext(context.Background()); got != "" {
		t.Errorf("expected empty string for unauthenticated context, got %q", got)
	}
}

// TestWithAuthenticatedKey_SetsAuditContext guards against the audit_log
// api_key_id column silently going unpopulated: withAuthenticatedKey must set
// both the handler-facing (APIKeyIDFromContext) and audit-log-facing
// (AuditAPIKeyIDFromContext) context values from the same authenticated id.
func TestWithAuthenticatedKey_SetsAuditContext(t *testing.T) {
	id := uuid.New()
	ctx := withAuthenticatedKey(context.Background(), id.String(), "mainnet")

	if got := APIKeyIDFromContext(ctx); got != id.String() {
		t.Errorf("APIKeyIDFromContext = %q, want %q", got, id.String())
	}
	if got := NetworkFromContext(ctx); got != "mainnet" {
		t.Errorf("NetworkFromContext = %q, want mainnet", got)
	}
	auditID := AuditAPIKeyIDFromContext(ctx)
	if auditID == nil || *auditID != id {
		t.Errorf("AuditAPIKeyIDFromContext = %v, want %v", auditID, id)
	}
}

func TestWithAuthenticatedKey_NonUUIDIDSkipsAuditContext(t *testing.T) {
	ctx := withAuthenticatedKey(context.Background(), "not-a-uuid", "testnet")

	if got := APIKeyIDFromContext(ctx); got != "not-a-uuid" {
		t.Errorf("APIKeyIDFromContext = %q, want %q", got, "not-a-uuid")
	}
	if auditID := AuditAPIKeyIDFromContext(ctx); auditID != nil {
		t.Errorf("expected nil audit key id for a non-UUID id, got %v", auditID)
	}
}
