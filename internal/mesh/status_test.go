package mesh

import (
	"sync/atomic"
	"testing"
)

type reconnectingTestRelay struct {
	*memoryRelay
	connected atomic.Bool
}

func (r *reconnectingTestRelay) Connected() bool { return r.connected.Load() }

func TestStatusReflectsRelayDisconnectAndRecovery(t *testing.T) {
	hub := &memoryHub{}
	r := &reconnectingTestRelay{memoryRelay: hub.join("phone")}
	node, _ := testNode(t, "phone", "100.96.0.60", r, true)
	_, peerKey, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := node.SetPeer(Peer{ID: "mac", Address: "100.96.0.61", PublicKey: peerKey}); err != nil {
		t.Fatal(err)
	}
	assertPath := func(want string) {
		t.Helper()
		status := node.Status()
		if len(status) != 1 || status[0].Path != want {
			t.Fatalf("wanted %s, got %+v", want, status)
		}
	}
	assertPath("unavailable")
	r.connected.Store(true)
	assertPath("relay")
	r.connected.Store(false)
	assertPath("unavailable")
	r.connected.Store(true)
	assertPath("relay")
	if err := node.bind.Close(); err != nil {
		t.Fatal(err)
	}
	assertPath("unavailable")
}
