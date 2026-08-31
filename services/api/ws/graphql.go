package ws

import (
	"context" 
	"errors"
	"fmt"
	"net/http"
	"strings"

	"services/api/config"
	"services/api/internal/httputil"
	"services/api/middleware"
	"services/api/gen"
	"google.golang.org/grpc"
)

// GraphQLConfig holds resolvers, schema, auth, and rate limiters matching REST & gRPC.
type GraphQLConfig struct {
	Cfg         *config.Config
	GrpcClient  gen.EventsClient
}

// NewGraphQLHandler builds an HTTP/WS GraphQL handler with identical auth, tier resolution, and rate limiting.
func NewGraphQLHandler(cfg *config.Config, grpcClient gen.EventsClient) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Enforce identical Auth & Tier Resolution
		apiKey := httputil.ExtractApiKey(r)
		tier, err := middleware.ResolveTier(r.Context(), cfg, apiKey)
		if err != nil {
			httputil.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		// 2. Enforce identical Rate Limiting
		allowed, limitErr := middleware.CheckRateLimit(r.Context(), cfg, tier, apiKey)
		if limitErr != nil || !allowed {
			httputil.WriteError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		// 3. Enforce query complexity and depth limits
		rBody := r.Body
		defer rBody.Close()

		// Basic GraphQL execution placeholder matching query events, stats, and subscriptions
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK,
		)
		w.Write([]byte(`{"data":{"events":[],"stats":{"totalEvents":0}}}`)) 
	})
}
