package engine

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xiaojohn-eng/JunGo/internal/control"
	"github.com/xiaojohn-eng/JunGo/internal/files"
	"github.com/xiaojohn-eng/JunGo/internal/mesh"
	"github.com/xiaojohn-eng/JunGo/internal/relay"
	"github.com/miekg/dns"
)

type failedRelayWriter struct {
	net.Conn
	fail atomic.Bool
}

func (c *failedRelayWriter) Write(b []byte) (int, error) {
	if c.fail.Load() {
		return 0, errors.New("injected permanent relay writer failure")
	}
	return c.Conn.Write(b)
}

func TestRelayWriteFailureReconnectsWithoutReadFailure(t *testing.T) {
	connected := make(chan struct{}, 4)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connected <- struct{}{}
		for {
			kind, packet, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(kind, packet); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var first atomic.Pointer[failedRelayWriter]
	var attempts atomic.Int32
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		if attempts.Add(1) == 1 {
			wrapped := &failedRelayWriter{Conn: conn}
			first.Store(wrapped)
			return wrapped, nil
		}
		return conn, nil
	}
	r := newRelay(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), strings.Repeat("a", 32), relay.DialConfig{NetDialContext: dial})
	defer r.Close()
	wait(t, 5*time.Second, func() bool { r.mu.RLock(); defer r.mu.RUnlock(); return r.client != nil })
	if !r.Connected() {
		t.Fatal("live relay reported disconnected")
	}
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("initial relay connection missing")
	}
	// Only writes fail. Both TCP read sides remain healthy and blocked, with no
	// server ping/read deadline that could accidentally trigger reconnection.
	first.Load().fail.Store(true)
	if err := r.Send("peer", []byte("opaque packet")); err == nil {
		t.Fatal("injected failure was not returned")
	}
	if r.Connected() {
		t.Fatal("failed relay writer still reported connected")
	}
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("permanently failed writer retained a live reader; reconnect never started")
	}
	wait(t, 5*time.Second, func() bool { r.mu.RLock(); defer r.mu.RUnlock(); return r.client != nil })
	if err := r.Send("peer", []byte("after reconnect")); err != nil {
		t.Fatal(err)
	}
	id, packet, err := r.Receive()
	if err != nil || id != "peer" || string(packet) != "after reconnect" {
		t.Fatalf("replacement relay cannot carry packets: id=%q packet=%q error=%v", id, packet, err)
	}
	if !r.Connected() {
		t.Fatal("replacement relay did not restore connected status")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if r.Connected() {
		t.Fatal("closed relay still reported connected")
	}
}

func TestRevocationReleasesWholeSession(t *testing.T) {
	a, _, store := fixture(t)
	call(t, a, "network", map[string]bool{"mesh": true})
	wait(t, 5*time.Second, func() bool { return len(a.State().Peers) == 1 })
	a.mu.RLock()
	done := a.sessionDone
	a.mu.RUnlock()
	if err := store.Revoke(a.State().Device.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	wait(t, 5*time.Second, func() bool {
		a.mu.RLock()
		defer a.mu.RUnlock()
		return a.node == nil && a.sessionCancel == nil && a.sessionDone == nil && a.fileServer == nil && a.fileService == nil && len(a.forwards) == 0
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("revoked session has not exited")
	}
	// The file service's state lock must actually be released, not merely have
	// its pointer cleared on Engine. Serialize with the tail of stopMesh.
	a.op.Lock()
	service, err := files.New(files.Config{StateDir: filepath.Join(a.dir, "files"), Authorize: func(context.Context, *http.Request) (string, error) { return "", errors.New("test") }})
	a.op.Unlock()
	if err != nil {
		t.Fatalf("revoked file service retained state lock: %v", err)
	}
	service.Close()
}

func lifecycleNode(t *testing.T, id, ip string) *mesh.Node {
	t.Helper()
	key, _, err := mesh.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	node, err := mesh.New(mesh.Config{ID: id, Address: ip, PrivateKey: key})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { node.Close() })
	return node
}

func TestDelayedRetirementKeepsReplacementSession(t *testing.T) {
	old := lifecycleNode(t, "old", "100.96.0.10")
	next := lifecycleNode(t, "next", "100.96.0.11")
	e := &Engine{node: old, peers: map[string]control.Device{}}
	e.op.Lock()
	done := e.retireSession(old)
	e.mu.Lock()
	e.node = next
	e.mu.Unlock()
	e.op.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retirement did not complete")
	}
	e.mu.RLock()
	retained := e.node == next
	e.mu.RUnlock()
	if !retained {
		t.Fatal("stale retirement cleared replacement session")
	}
	l, err := next.ListenTCP(9000)
	if err != nil {
		t.Fatalf("replacement node closed: %v", err)
	}
	l.Close()
}

type protectedTestPlatform struct{ calls atomic.Int32 }

func (p *protectedTestPlatform) Protect(int) bool                         { p.calls.Add(1); return true }
func (*protectedTestPlatform) OwnerUID(int, string, int, string, int) int { return -1 }
func (*protectedTestPlatform) OpenURI(string, string) int                 { return -1 }
func (*protectedTestPlatform) URIInfo(string) string                      { return "" }

func TestProtectedResolverUsesProtectedExplicitDNS(t *testing.T) {
	packet, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{PacketConn: packet, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, query *dns.Msg) {
		response := new(dns.Msg)
		response.SetReply(query)
		for _, q := range query.Question {
			if q.Qtype == dns.TypeA {
				response.Answer = append(response.Answer, &dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30}, A: net.ParseIP("192.0.2.42")})
			}
		}
		_ = w.WriteMsg(response)
	})}
	go server.ActivateAndServe()
	t.Cleanup(func() { server.Shutdown(); packet.Close() })
	p := &protectedTestPlatform{}
	e := &Engine{platform: p}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := protectedResolver(e.dialer().DialContext, packet.LocalAddr().String()).LookupNetIP(ctx, "ip4", "control.example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 1 || ips[0] != netip.MustParseAddr("192.0.2.42") || p.calls.Load() == 0 {
		t.Fatalf("DNS did not use protected explicit upstream: %v, protection calls %d", ips, p.calls.Load())
	}
}

type changingCandidates struct {
	network atomic.Int32
	stun    chan string
}

func (c *changingCandidates) LocalEndpoints() []string {
	if c.network.Load() == 0 {
		return []string{"192.168.1.2:2000"}
	}
	return []string{"192.168.2.2:3000"}
}
func (c *changingCandidates) DiscoverSTUN(ctx context.Context, server string) (string, error) {
	select {
	case c.stun <- server:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if c.network.Load() == 0 {
		return "198.51.100.1:4000", nil
	}
	return "198.51.100.2:5000", nil
}

func TestCandidatesResolveDomainAndRefreshAfterNetworkChange(t *testing.T) {
	e := &Engine{}
	node := &changingCandidates{stun: make(chan string, 4)}
	updates := make(chan []string, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.refreshCandidates(ctx, "https://localhost:9443", node, updates, 20*time.Millisecond)
	}()
	select {
	case first := <-updates:
		if !slices.Contains(first, "192.168.1.2:2000") || !slices.Contains(first, "198.51.100.1:4000") {
			t.Fatal(first)
		}
	case <-ctx.Done():
		t.Fatal("initial candidate discovery timed out")
	}
	select {
	case server := <-node.stun:
		if server != "127.0.0.1:3478" {
			t.Fatalf("STUN did not receive numeric IPv4: %s", server)
		}
	case <-ctx.Done():
		t.Fatal("STUN was not called")
	}
	node.network.Store(1)
	for {
		select {
		case next := <-updates:
			if slices.Contains(next, "192.168.2.2:3000") && slices.Contains(next, "198.51.100.2:5000") {
				if slices.Contains(next, "198.51.100.1:4000") {
					t.Fatal("stale NAT mapping retained", next)
				}
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("candidate worker ignored cancellation")
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("changed network endpoints were not discovered")
		}
	}
}
