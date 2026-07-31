package grpc

import (
	"testing"
)

// TestTransportCredentials_DefaultIsInsecure asserts that with
// GRPC_MTLS_ENABLED unset (the default), transportCredentials returns
// plaintext credentials rather than erroring or requiring cert files.
func TestTransportCredentials_DefaultIsInsecure(t *testing.T) {
	t.Setenv("GRPC_MTLS_ENABLED", "")

	creds, err := transportCredentials()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.Info().SecurityProtocol != "insecure" {
		t.Fatalf("want insecure transport, got %q", creds.Info().SecurityProtocol)
	}
}

// TestTransportCredentials_EnabledWithoutCertsErrors asserts that enabling
// mTLS without providing cert paths fails loudly instead of silently
// falling back to plaintext.
func TestTransportCredentials_EnabledWithoutCertsErrors(t *testing.T) {
	t.Setenv("GRPC_MTLS_ENABLED", "true")
	t.Setenv("GRPC_MTLS_CA_CERT", "")
	t.Setenv("GRPC_MTLS_CLIENT_CERT", "")
	t.Setenv("GRPC_MTLS_CLIENT_KEY", "")

	if _, err := transportCredentials(); err == nil {
		t.Fatal("want error when GRPC_MTLS_ENABLED=true but cert paths are unset")
	}
}

// TestTransportCredentials_EnabledWithBadPathsErrors asserts a configured but
// unreadable cert path surfaces an error rather than silently disabling
// verification.
func TestTransportCredentials_EnabledWithBadPathsErrors(t *testing.T) {
	t.Setenv("GRPC_MTLS_ENABLED", "true")
	t.Setenv("GRPC_MTLS_CA_CERT", "/nonexistent/ca.crt")
	t.Setenv("GRPC_MTLS_CLIENT_CERT", "/nonexistent/client.crt")
	t.Setenv("GRPC_MTLS_CLIENT_KEY", "/nonexistent/client.key")

	if _, err := transportCredentials(); err == nil {
		t.Fatal("want error when cert files do not exist")
	}
}
