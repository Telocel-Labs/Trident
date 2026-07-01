package middleware

import "context"

type contextKey string

const (
	contextKeyAPIKeyID contextKey = "api_key_id"
	contextKeyNetwork  contextKey = "network"
)

// APIKeyIDFromContext returns the authenticated API key UUID, or empty string
// when the request was authenticated via the legacy env-var path.
func APIKeyIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(contextKeyAPIKeyID).(string)
	return v
}

// NetworkFromContext returns the network associated with the authenticated API
// key. Defaults to "testnet" when the request is unauthenticated or the key
// has no network set.
func NetworkFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(contextKeyNetwork).(string); ok && v != "" {
		return v
	}
	return "testnet"
}
