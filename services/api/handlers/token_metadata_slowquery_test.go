package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// slowQueryDB is a DBPool whose QueryRow blocks until the caller's context
// is done, simulating a runaway/slow query for issue #238's "test
// demonstrating slow query cancellation and pool recovery" acceptance
// criterion.
type slowQueryDB struct{}

func (slowQueryDB) Ping(_ context.Context) error { return nil }

func (slowQueryDB) QueryRow(ctx context.Context, _ string, _ ...any) pgx.Row {
	return slowQueryRow{ctx: ctx}
}

func (slowQueryDB) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return nil, nil
}

type slowQueryRow struct{ ctx context.Context }

func (r slowQueryRow) Scan(_ ...any) error {
	<-r.ctx.Done()
	return r.ctx.Err()
}

// fastQueryDB is a DBPool that resolves immediately with a not-found result,
// standing in for a healthy pool connection.
type fastQueryDB struct{}

func (fastQueryDB) Ping(_ context.Context) error { return nil }

func (fastQueryDB) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return fastQueryRow{}
}

func (fastQueryDB) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return nil, nil
}

type fastQueryRow struct{}

func (fastQueryRow) Scan(_ ...any) error { return pgx.ErrNoRows }

// TestTokenMetadata_SlowQueryIsCancelledByTimeout demonstrates issue #238's
// per-call deadline: even though the incoming request context has no
// deadline of its own (httptest.NewRequest's default), the handler's own
// tokenMetadataQueryTimeout bounds the DB call — a runaway query can't hold
// the connection indefinitely.
func TestTokenMetadata_SlowQueryIsCancelledByTimeout(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/contracts/"+validSchemaContractID+"/metadata", nil)
	req.SetPathValue("id", validSchemaContractID)
	rr := httptest.NewRecorder()

	start := time.Now()
	TokenMetadata(slowQueryDB{}).ServeHTTP(rr, req)
	elapsed := time.Since(start)

	if elapsed > tokenMetadataQueryTimeout+2*time.Second {
		t.Fatalf("handler took %v, want bounded near the %v query timeout — deadline was not applied", elapsed, tokenMetadataQueryTimeout)
	}
	if elapsed < tokenMetadataQueryTimeout-500*time.Millisecond {
		t.Fatalf("handler returned after only %v, want it to have waited out the %v query timeout", elapsed, tokenMetadataQueryTimeout)
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 on a cancelled query, got %d", rr.Code)
	}
}

// TestTokenMetadata_PoolRecoversAfterSlowQuery demonstrates issue #238's
// "pool recovery" criterion: a request against a healthy connection
// immediately after a cancelled slow query succeeds normally and quickly —
// the earlier timeout does not leave the handler path wedged.
func TestTokenMetadata_PoolRecoversAfterSlowQuery(t *testing.T) {
	slowReq := httptest.NewRequest(http.MethodGet, "/v1/contracts/"+validSchemaContractID+"/metadata", nil)
	slowReq.SetPathValue("id", validSchemaContractID)
	TokenMetadata(slowQueryDB{}).ServeHTTP(httptest.NewRecorder(), slowReq)

	fastReq := httptest.NewRequest(http.MethodGet, "/v1/contracts/"+validSchemaContractID+"/metadata", nil)
	fastReq.SetPathValue("id", validSchemaContractID)
	rr := httptest.NewRecorder()

	start := time.Now()
	TokenMetadata(fastQueryDB{}).ServeHTTP(rr, fastReq)
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("recovery request took %v, want a fast response — pool/handler path looks wedged after the prior timeout", elapsed)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200 on recovery request, got %d", rr.Code)
	}
}
