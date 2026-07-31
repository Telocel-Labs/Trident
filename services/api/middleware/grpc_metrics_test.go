package middleware_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Depo-dev/trident/services/api/middleware"
)

func TestRecordGRPCClientCall_RendersRequestsAndLatency(t *testing.T) {
	middleware.RecordGRPCClientCall("/trident.Events/GetEvent", "NotFound", 0.002)

	var buf bytes.Buffer
	middleware.WriteGRPCClientMetrics(&buf)
	body := buf.String()

	if !strings.Contains(body, `trident_api_grpc_client_requests_total{method="/trident.Events/GetEvent",code="NotFound"} 1`) {
		t.Errorf("expected a requests_total sample of 1, got:\n%s", body)
	}
	if !strings.Contains(body, "trident_api_grpc_client_request_duration_seconds_bucket") {
		t.Errorf("expected latency histogram buckets, got:\n%s", body)
	}
}
