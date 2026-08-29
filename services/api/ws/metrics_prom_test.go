package ws

import (
	"testing"

	"github.com/Depo-dev/trident/services/api/internal/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestHub_RegisterUnregisterUpdatesPrometheusMetrics verifies register/
// unregister move the active-connections gauge and connect/disconnect
// counters exposed on the internal metrics port (issue #58).
func TestHub_RegisterUnregisterUpdatesPrometheusMetrics(t *testing.T) {
	h := NewHub()
	c := &client{contractID: "contract-abc", send: make(chan []byte, 8)}

	activeBefore := testutil.ToFloat64(metrics.WSActiveConnections)
	connectsBefore := testutil.ToFloat64(metrics.WSConnectsTotal)

	h.register(c)

	if got := testutil.ToFloat64(metrics.WSActiveConnections); got != activeBefore+1 {
		t.Errorf("active connections after register: want %v, got %v", activeBefore+1, got)
	}
	if got := testutil.ToFloat64(metrics.WSConnectsTotal); got != connectsBefore+1 {
		t.Errorf("connects total after register: want %v, got %v", connectsBefore+1, got)
	}

	disconnectsBefore := testutil.ToFloat64(metrics.WSDisconnectsTotal)
	h.unregister(c)

	if got := testutil.ToFloat64(metrics.WSActiveConnections); got != activeBefore {
		t.Errorf("active connections after unregister: want %v, got %v", activeBefore, got)
	}
	if got := testutil.ToFloat64(metrics.WSDisconnectsTotal); got != disconnectsBefore+1 {
		t.Errorf("disconnects total after unregister: want %v, got %v", disconnectsBefore+1, got)
	}

	// A second unregister of the same (already-removed) client must not double-count.
	h.unregister(c)
	if got := testutil.ToFloat64(metrics.WSDisconnectsTotal); got != disconnectsBefore+1 {
		t.Errorf("disconnects total after redundant unregister: want %v, got %v", disconnectsBefore+1, got)
	}
}

// TestHub_BroadcastUpdatesMessageCounters verifies sent/dropped outcomes are
// recorded on trident_ws_messages_total.
func TestHub_BroadcastUpdatesMessageCounters(t *testing.T) {
	h := NewHub()
	c := &client{contractID: "contract-msg", send: make(chan []byte, 1)}
	h.register(c)
	defer h.unregister(c)

	sentBefore := testutil.ToFloat64(metrics.WSMessagesTotal.WithLabelValues("sent"))
	droppedBefore := testutil.ToFloat64(metrics.WSMessagesTotal.WithLabelValues("dropped"))

	h.Broadcast("contract-msg", []byte("first"))  // fills the buffer, delivered
	h.Broadcast("contract-msg", []byte("second")) // buffer full, dropped

	if got := testutil.ToFloat64(metrics.WSMessagesTotal.WithLabelValues("sent")); got != sentBefore+1 {
		t.Errorf("sent counter: want %v, got %v", sentBefore+1, got)
	}
	if got := testutil.ToFloat64(metrics.WSMessagesTotal.WithLabelValues("dropped")); got != droppedBefore+1 {
		t.Errorf("dropped counter: want %v, got %v", droppedBefore+1, got)
	}
}
