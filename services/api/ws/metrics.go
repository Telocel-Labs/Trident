package ws

import (
	"fmt"
	"io"
	"sync/atomic"
)

// Backpressure metrics for slow-consumer handling (issue #224), in the same
// dependency-free Prometheus text-exposition style as handlers/stats.go and
// grpc/metrics.go. Every dropped message and every slow-consumer disconnect
// (REST WS and GraphQL subscriptions both funnel through Hub.Broadcast) is
// counted here.
var (
	metricMessagesDropped         atomic.Int64
	metricSlowConsumerDisconnects atomic.Int64
)

// WriteMetrics writes the WS/GraphQL backpressure counters in Prometheus text
// format. Mounted into the API's /metrics endpoint by handlers.MetricsHandler.
func WriteMetrics(w io.Writer) {
	_, _ = fmt.Fprintf(w, "# HELP trident_ws_messages_dropped_total Messages dropped because a subscriber's send buffer was full.\n")
	_, _ = fmt.Fprintf(w, "# TYPE trident_ws_messages_dropped_total counter\n")
	_, _ = fmt.Fprintf(w, "trident_ws_messages_dropped_total %d\n", metricMessagesDropped.Load())

	_, _ = fmt.Fprintf(w, "# HELP trident_ws_slow_consumer_disconnects_total Subscribers disconnected for exceeding the consecutive-drop threshold.\n")
	_, _ = fmt.Fprintf(w, "# TYPE trident_ws_slow_consumer_disconnects_total counter\n")
	_, _ = fmt.Fprintf(w, "trident_ws_slow_consumer_disconnects_total %d\n", metricSlowConsumerDisconnects.Load())
}
