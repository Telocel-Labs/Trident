package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// idempotentKeyMintingRoutes are routes that mint or replace a live API key
// or webhook secret, and so must sit behind middleware.Idempotency: a
// retried request (client timeout, network blip, double-click) must return
// the original credential rather than minting a second, distinct one
// (issue #683).
//
// POST /v1/api-keys was already wrapped before this issue was filed; the
// issue's own comparison point was never actually missing it. The real gaps
// were the three remaining routes below, each of which mints or replaces a
// credential exactly as sensitively as API key creation does, but had no
// equivalent wrapping.
//
// newReq builds a request for the route that reaches a cacheable 4xx
// (middleware.Idempotency never caches a 5xx) without needing a live
// Postgres connection or admin key, so this test can probe the real
// middleware wiring without standing up either dependency.
var idempotentKeyMintingRoutes = []struct {
	method, path string
	newReq       func() *http.Request
}{
	{
		http.MethodPost, "/v1/api-keys",
		func() *http.Request {
			// No X-Admin-Key header: handlers.requireAdmin 403s before
			// touching cfg.DB (apikeys.go), regardless of whether it is nil.
			return httptest.NewRequest(http.MethodPost, "/v1/api-keys", nil)
		},
	},
	{
		http.MethodPost, "/v1/api-keys/{id}/rotate",
		func() *http.Request {
			return httptest.NewRequest(http.MethodPost, "/v1/api-keys/{id}/rotate", nil)
		},
	},
	{
		http.MethodPost, "/v1/webhooks",
		func() *http.Request {
			// createWebhookHandler checks db == nil before parsing the body,
			// so db must be non-nil to reach the 400 this test wants. A pgx
			// *sql.DB opened against an unreachable address never dials
			// until a query runs (sql.Open is lazy) -- the missing
			// targetUrl/contractId 400 fires first and no query ever runs.
			req := httptest.NewRequest(http.MethodPost, "/v1/webhooks",
				strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			return req
		},
	},
	{
		http.MethodPost, "/v1/webhooks/{id}/rotate-secret",
		func() *http.Request {
			// ValidateUUID on the path's {id} rejects before the handler's
			// own db == nil check, so this one 400s even with db == nil.
			return httptest.NewRequest(http.MethodPost, "/v1/webhooks/not-a-uuid/rotate-secret", nil)
		},
	},
}

func TestKeyMintingRoutesAreIdempotencyWrapped(t *testing.T) {
	bindings := routeBindings()

	webhookDB, err := sql.Open("pgx", "postgres://unused@127.0.0.1:1/unused")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = webhookDB.Close() }()

	for _, want := range idempotentKeyMintingRoutes {
		t.Run(want.method+" "+want.path, func(t *testing.T) {
			var found bool
			for _, b := range bindings {
				if b.route.Method != want.method || b.route.Path != want.path {
					continue
				}
				found = true

				server := miniredis.RunT(t)
				rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
				defer func() { _ = rdb.Close() }()

				probe := b.handler(routeDeps{redisClient: rdb, webhookDB: webhookDB})

				req := want.newReq()
				req.Header.Set("Idempotency-Key", "wiring-probe-"+want.path)
				rec1 := httptest.NewRecorder()
				probe.ServeHTTP(rec1, req)

				if rec1.Code >= 500 {
					t.Fatalf("%s %s: probe request itself returned %d, not a cacheable 4xx -- "+
						"fix idempotentKeyMintingRoutes' newReq for this route rather than trusting "+
						"this test's result", want.method, want.path, rec1.Code)
				}

				req2 := want.newReq()
				req2.Header.Set("Idempotency-Key", "wiring-probe-"+want.path)
				rec2 := httptest.NewRecorder()
				probe.ServeHTTP(rec2, req2)

				if rec2.Header().Get("Idempotent-Replayed") != "true" {
					t.Errorf("%s %s: second request with the same Idempotency-Key was not "+
						"replayed (missing Idempotent-Replayed header) -- this route mints or "+
						"replaces a credential and must be wrapped in middleware.Idempotency (issue #683)",
						want.method, want.path)
				}
				if rec1.Code != rec2.Code {
					t.Errorf("%s %s: replayed response status %d does not match original %d",
						want.method, want.path, rec2.Code, rec1.Code)
				}
			}
			if !found {
				t.Errorf("route %s %s not found in routeBindings(); update idempotentKeyMintingRoutes if it moved",
					want.method, want.path)
			}
		})
	}
}
