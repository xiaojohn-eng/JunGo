package engine

import (
	"context"
	"net/http/httptrace"
	"sync/atomic"
	"testing"
	"time"
)

func TestPeerHTTPPoolReusesConnectionAndRevokes(t *testing.T) {
	a, b, store := fixture(t)
	call(t, a, "network", map[string]bool{"mesh": true})
	call(t, b, "network", map[string]bool{"mesh": true})
	peerID := b.State().Device.ID
	wait(t, 15*time.Second, func() bool {
		return len(a.State().Peers) == 1 && a.State().Peers[0].FileTLSFingerprint != "" && len(b.State().Peers) == 1
	})
	var handshakes, reused atomic.Int32
	trace := &httptrace.ClientTrace{TLSHandshakeStart: func() { handshakes.Add(1) }, GotConn: func(info httptrace.GotConnInfo) {
		if info.Reused {
			reused.Add(1)
		}
	}}
	ctx, cancel := context.WithTimeout(httptrace.WithClientTrace(context.Background(), trace), 30*time.Second)
	defer cancel()
	for i := 0; i < 8; i++ {
		if _, err := a.fileJSON(ctx, peerID, "GET", "/v1/files/shares", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if handshakes.Load() != 1 || reused.Load() != 7 {
		t.Fatalf("8 sequential file calls: TLS handshakes=%d reused=%d", handshakes.Load(), reused.Load())
	}
	first, _, key, _, err := a.peerClient(peerID)
	if err != nil {
		t.Fatal(err)
	}
	original := key[0]
	key[0] ^= 0xff
	same, _, key, _, err := a.peerClient(peerID)
	if err != nil || same != first || key[0] != original {
		t.Fatal("pool exposes mutable authentication key")
	}
	// A transport cannot cross a peer certificate identity change.
	a.mu.Lock()
	peer := a.peers[peerID]
	previousFingerprint := peer.FileTLSFingerprint
	peer.FileTLSFingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	a.peers[peerID] = peer
	a.mu.Unlock()
	a.prunePeerClients()
	replacement, _, _, _, err := a.peerClient(peerID)
	if err != nil || replacement == first {
		t.Fatal("pool survived certificate identity change")
	}
	a.mu.Lock()
	peer.FileTLSFingerprint = previousFingerprint
	a.peers[peerID] = peer
	a.mu.Unlock()
	a.prunePeerClients()
	if _, _, _, _, err = a.peerClient(peerID); err != nil {
		t.Fatal(err)
	}
	if err = store.Revoke(peerID, time.Now()); err != nil {
		t.Fatal(err)
	}
	wait(t, 5*time.Second, func() bool {
		a.mu.RLock()
		_, ok := a.peers[peerID]
		a.mu.RUnlock()
		a.peerClientsMu.Lock()
		count := len(a.peerClients)
		a.peerClientsMu.Unlock()
		return !ok && count == 0
	})
	if _, _, _, _, err = a.peerClient(peerID); err == nil {
		t.Fatal("revoked device retained a usable pool")
	}
	t.Logf("8 requests: %d TLS handshake, %d reused connections", handshakes.Load(), reused.Load())
}

func TestPeerHTTPPoolCannotCrossMeshRestart(t *testing.T) {
	a, b, _ := fixture(t)
	peerID := b.State().Device.ID
	call(t, a, "network", map[string]bool{"mesh": true})
	wait(t, 5*time.Second, func() bool { return len(a.State().Peers) == 1 })
	old, _, _, _, err := a.peerClient(peerID)
	if err != nil {
		t.Fatal(err)
	}
	call(t, a, "network", map[string]bool{"mesh": false})
	a.peerClientsMu.Lock()
	count := len(a.peerClients)
	a.peerClientsMu.Unlock()
	if count != 0 {
		t.Fatal("mesh stop retained peer clients")
	}
	if _, _, _, _, err = a.peerClient(peerID); err == nil {
		t.Fatal("stopped mesh returned peer transport")
	}
	call(t, a, "network", map[string]bool{"mesh": true})
	wait(t, 5*time.Second, func() bool { return len(a.State().Peers) == 1 })
	current, _, _, _, err := a.peerClient(peerID)
	if err != nil || current == old {
		t.Fatal("new mesh reused previous transport")
	}
}
