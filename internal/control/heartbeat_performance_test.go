package control

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStableHeartbeatDoesNotWriteOrBroadcast(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	peer := enroll(t, s, "Mac", 1)
	events, unsubscribe := s.SubscribeEvents()
	defer unsubscribe()
	hb := Heartbeat{Endpoints: []string{"192.168.1.2:51820", "192.168.1.3:51820"}, FileURL: "https://100.96.0.2:8443"}
	now := time.Now()
	if _, err = s.Heartbeat(peer.Token, hb, now); err != nil {
		t.Fatal(err)
	}
	select {
	case <-events:
	default:
		t.Fatal("topology change notification missing")
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Reordering the same endpoints must not generate false topology changes.
	hb.Endpoints[0], hb.Endpoints[1] = hb.Endpoints[1], hb.Endpoints[0]
	next := now.Add(10 * time.Second)
	if _, err = s.Heartbeat(peer.Token, hb, next); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(disk, current) {
		t.Fatal("liveness-only heartbeat rewrote durable state")
	}
	got, err := s.Authenticate(peer.Token)
	if err != nil || !got.LastSeen.Equal(next) {
		t.Fatal("liveness was not updated immediately")
	}
	select {
	case event := <-events:
		t.Fatalf("redundant invalidation: %+v", event)
	default:
	}
	// A device returning from offline still prompts an immediate peer refresh.
	if _, err = s.Heartbeat(peer.Token, hb, next.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-events:
	default:
		t.Fatal("returning device did not invalidate peers")
	}
	// Topology updates stay durable, and a reload must rebuild token lookup.
	hb.FileURL = "https://100.96.0.2:9443"
	if _, err = s.Heartbeat(peer.Token, hb, next.Add(time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err = reopened.Authenticate(peer.Token)
	if err != nil || got.FileURL != hb.FileURL {
		t.Fatal("topology or token index lost on restart")
	}
	if err = s.Revoke(peer.Device.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Heartbeat(peer.Token, hb, time.Now()); err == nil {
		t.Fatal("revoked heartbeat accepted")
	}
}

func TestHeartbeatTopologyFailureDoesNotChangeMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	peer := enroll(t, s, "Mac", 1)
	s.path = filepath.Join(path, "unwritable.json")
	if _, err = s.Heartbeat(peer.Token, Heartbeat{Endpoints: []string{"192.168.1.2:51820"}}, time.Now()); err == nil {
		t.Fatal("expected write failure")
	}
	got, err := s.Authenticate(peer.Token)
	if err != nil || len(got.Endpoints) != 0 || !got.LastSeen.Equal(peer.Device.LastSeen) {
		t.Fatal("failed topology write changed registry")
	}
}
