package middleware

import (
	"context"
	"net/http"

	"github.com/Depo-dev/trident/services/api/internal/httputil"
)

// ScopeAdmin and ScopeRead are the two scope values a database-issued API key
// can carry (issue #314). This is a separate mechanism from the ADMIN_API_KEY
// env-var gate used by the /v1/api-keys* and /v1/admin/* management
// endpoints (requireAdmin in handlers) — that gate is unchanged. Scope
// distinguishes read vs admin among ordinary database-issued keys for
// regular data routes.
const (
	ScopeRead  = "read"
	ScopeAdmin = "admin"
)

const contextKeyScope contextKey = "api_key_scope"

// ScopeFromContext returns the scope associated with the authenticated
// request. Requests authenticated via the legacy env-var path (API_KEY_HASHES)
// or with no scope recorded default to ScopeAdmin, preserving the pre-#314
// behavior that every valid key had full power — only newly created,
// database-issued keys are scoped down (default ScopeRead).
func ScopeFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(contextKeyScope).(string); ok && v != "" {
		return v
	}
	return ScopeAdmin
}

// withScope attaches scope to ctx. Unexported: only auth.go's NewDBAuth
// should set this value, since it is the sole source of truth for what an
// authenticated request is permitted to do.
func withScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, contextKeyScope, scope)
}

// RequireScope returns middleware that rejects a request with 403 unless the
// authenticated API key's scope (as attached by NewDBAuth) is at least
// `required`. The only recognized required value today is ScopeAdmin — an
// admin-scoped key satisfies any requirement, a read-scoped key satisfies
// only a ScopeRead requirement.
//
// This is deliberately independent of requireAdmin/ADMIN_API_KEY: it lets
// ordinary database-issued keys be split into read vs admin for regular data
// routes, without touching the separate admin-key gate used by key/contract
// management endpoints.
func RequireScope(required string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scope := ScopeFromContext(r.Context())
			if required == ScopeAdmin && scope != ScopeAdmin {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, httputil.FORBIDDEN,
					"this operation requires an admin-scoped API key")
				return
			}
			next.ServeHTTP(w, r.WithContext(r.Context()))
		})
	}
}
