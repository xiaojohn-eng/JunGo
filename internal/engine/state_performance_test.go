package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/xiaojohn-eng/JunGo/internal/control"
)

func stateBenchmarkProfile() string {
	var p strings.Builder
	p.WriteString("proxies:\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&p, "  - {name: node-%03d, type: socks5, server: example.invalid, port: 1080, password: private}\n", i)
	}
	p.WriteString("rules:\n")
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&p, "  - DOMAIN,host-%d.example.invalid,node-000\n", i)
	}
	return p.String()
}

func BenchmarkStateOfflineProfile(b *testing.B) {
	e := &Engine{cfg: Config{Profile: stateBenchmarkProfile(), Selected: "node-001"}, peers: make(map[string]control.Device), tasks: make(map[string]*Transfer)}
	_ = e.State()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if len(e.State().Proxies) != 200 {
			b.Fatal("missing nodes")
		}
	}
}

func TestOfflineProfileMetadataRefreshAndIsolation(t *testing.T) {
	e := &Engine{peers: make(map[string]control.Device), tasks: make(map[string]*Transfer)}
	e.cfg.Profile = "proxies: [{name: alpha, type: direct}, {name: beta, type: direct}]\nproxy-groups: [{name: group, type: select, proxies: [alpha, beta]}]\n"
	e.cfg.Selected = "alpha"
	first := e.State()
	if len(first.Proxies) != 3 || !first.Proxies[0].Selected {
		t.Fatal("initial selection lost")
	}
	first.Proxies[0].Name = "mutated"
	first.Proxies[2].Members[0] = "mutated"
	e.cfg.Selected = "beta"
	second := e.State()
	if second.Proxies[0].Name != "alpha" || second.Proxies[0].Selected || !second.Proxies[1].Selected || second.Proxies[2].Members[0] != "alpha" {
		t.Fatal("cached metadata aliases caller data or selection became stale")
	}
	e.cfg.Profile = "proxies: [{name: replacement, type: direct}]\n"
	third := e.State()
	if len(third.Proxies) != 1 || third.Proxies[0].Name != "replacement" {
		t.Fatal("subscription replacement did not invalidate cache")
	}
	e.cfg.Profile = ""
	if len(e.State().Proxies) != 0 {
		t.Fatal("cleared profile retained old metadata")
	}
}

func TestPeerEventRefreshIgnoresKeepAlive(t *testing.T) {
	for _, line := range []string{"event: ready", "event: peers-changed", "event: revoked", "event:peers-changed"} {
		if !peerEventNeedsRefresh(line) {
			t.Fatalf("ignored invalidation %q", line)
		}
	}
	for _, line := range []string{"event: heartbeat", "data: {}", "", ": keepalive"} {
		if peerEventNeedsRefresh(line) {
			t.Fatalf("unnecessary refresh for %q", line)
		}
	}
}
