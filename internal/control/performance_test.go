package control

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func benchmarkStore(b *testing.B, devices int, durable bool) (*Store, string) {
	b.Helper()
	path := ""
	if durable {
		path = filepath.Join(b.TempDir(), "control.json")
	}
	s, err := NewStore(path)
	if err != nil {
		b.Fatal(err)
	}
	n := s.snapshot()
	token := ""
	for i := 0; i < devices; i++ {
		token = fmt.Sprintf("%064d", i)
		id := fmt.Sprint(i)
		n.Devices[id] = persistedDevice{Device: Device{ID: id, MeshID: "default", LastSeen: time.Now(), Endpoints: []string{"192.168.1.2:51820"}}, TokenHash: hash(token)}
	}
	if err = s.commit(n); err != nil {
		b.Fatal(err)
	}
	return s, token
}

func BenchmarkAuthenticate128Devices(b *testing.B) {
	s, token := benchmarkStore(b, 128, false)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := s.Authenticate(token); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStableHeartbeatDurable(b *testing.B) {
	s, token := benchmarkStore(b, 8, true)
	hb := Heartbeat{Endpoints: []string{"192.168.1.2:51820"}}
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		now = now.Add(time.Second)
		if _, err := s.Heartbeat(token, hb, now); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRelayAuthorization128Devices(b *testing.B) {
	s, token := benchmarkStore(b, 128, false)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := s.Authenticate(token); err != nil || !s.CanCommunicate("127", "0") {
			b.Fatal("authorization failed")
		}
	}
}
