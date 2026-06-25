package ws

import (
	"testing"
)

// TestHub_RegisterAndBroadcast verifies that a registered client receives
// messages broadcast to its contractID (issue #15 AC: fan-out delivery).
func TestHub_RegisterAndBroadcast(t *testing.T) {
	h := NewHub()

	c := &client{
		contractID: "contract-abc",
		send:       make(chan []byte, 8),
	}
	h.register(c)

	msg := []byte(`{"event":"transfer"}`)
	h.Broadcast("contract-abc", msg)

	select {
	case got := <-c.send:
		if string(got) != string(msg) {
			t.Errorf("want %q, got %q", msg, got)
		}
	default:
		t.Fatal("expected message in send channel, got none")
	}
}

// TestHub_BroadcastDoesNotDeliverToOtherContracts verifies that messages are
// only delivered to subscribers of the matching contractID.
func TestHub_BroadcastDoesNotDeliverToOtherContracts(t *testing.T) {
	h := NewHub()

	c := &client{
		contractID: "contract-xyz",
		send:       make(chan []byte, 8),
	}
	h.register(c)

	h.Broadcast("contract-abc", []byte(`{"event":"irrelevant"}`))

	select {
	case got := <-c.send:
		t.Errorf("did not expect message for different contractID, got %q", got)
	default:
		// correct — nothing delivered
	}
}

// TestHub_UnregisterClosesChannel verifies that after unregister the client's
// send channel is closed so the write goroutine can exit cleanly (issue #15).
func TestHub_UnregisterClosesChannel(t *testing.T) {
	h := NewHub()

	c := &client{
		contractID: "contract-abc",
		send:       make(chan []byte, 8),
	}
	h.register(c)
	h.unregister(c)

	// Channel must be closed; a receive on a closed empty channel returns immediately.
	_, open := <-c.send
	if open {
		t.Error("expected send channel to be closed after unregister")
	}
}

// TestHub_UnregisterIsIdempotent verifies that calling unregister twice does
// not panic (double-close guard).
func TestHub_UnregisterIsIdempotent(t *testing.T) {
	h := NewHub()

	c := &client{
		contractID: "contract-abc",
		send:       make(chan []byte, 8),
	}
	h.register(c)
	h.unregister(c)

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("second unregister panicked: %v", r)
		}
	}()
	h.unregister(c)
}

// TestHub_MultipleClientsPerContract verifies that all subscribers for the
// same contractID receive the broadcast.
func TestHub_MultipleClientsPerContract(t *testing.T) {
	h := NewHub()

	const n = 3
	clients := make([]*client, n)
	for i := range clients {
		clients[i] = &client{contractID: "shared", send: make(chan []byte, 8)}
		h.register(clients[i])
	}

	h.Broadcast("shared", []byte(`{"event":"mint"}`))

	for i, c := range clients {
		select {
		case got := <-c.send:
			if string(got) != `{"event":"mint"}` {
				t.Errorf("client %d: unexpected message %q", i, got)
			}
		default:
			t.Errorf("client %d: expected message, got none", i)
		}
	}
}

// TestHub_SlowClientDropsMessage verifies that a client with a full send
// buffer does not block the broadcaster (drop-on-full semantics).
func TestHub_SlowClientDropsMessage(t *testing.T) {
	h := NewHub()

	// Buffer size 1 — fill it first so the next broadcast must drop.
	c := &client{contractID: "contract-slow", send: make(chan []byte, 1)}
	h.register(c)
	c.send <- []byte("pre-fill")

	// This must not block.
	done := make(chan struct{})
	go func() {
		h.Broadcast("contract-slow", []byte("dropped"))
		close(done)
	}()

	select {
	case <-done:
		// correct — broadcast returned without blocking
	}

	// Only the pre-filled message should be in the channel.
	if len(c.send) != 1 {
		t.Errorf("want 1 message in channel (pre-fill), got %d", len(c.send))
	}
}
