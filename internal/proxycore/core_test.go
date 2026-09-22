package proxycore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaojohn-eng/JunGo/internal/mesh"
	"github.com/metacubex/mihomo/common/yaml"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel"
	D "github.com/miekg/dns"
	"golang.org/x/net/proxy"
	"golang.org/x/sys/unix"
)

func makeMeshPair(t *testing.T) (*mesh.Node, *mesh.Node) {
	t.Helper()
	ak, ap, err := mesh.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	bk, bp, err := mesh.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	a, err := mesh.New(mesh.Config{ID: "android", Address: "100.96.0.1", PrivateKey: ak})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	b, err := mesh.New(mesh.Config{ID: "mac", Address: "100.96.0.2", PrivateKey: bk})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	endpoint := func(n *mesh.Node) string {
		eps := n.LocalEndpoints()
		if len(eps) == 0 {
			t.Fatal("no local IPv4 interface for mesh test")
		}
		ap := netip.MustParseAddrPort(eps[0])
		return netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), ap.Port()).String()
	}
	if err = a.SetPeer(mesh.Peer{ID: "mac", Address: "100.96.0.2", PublicKey: bp, Endpoints: []string{endpoint(b)}}); err != nil {
		t.Fatal(err)
	}
	if err = b.SetPeer(mesh.Peer{ID: "android", Address: "100.96.0.1", PublicKey: ap, Endpoints: []string{endpoint(a)}}); err != nil {
		t.Fatal(err)
	}
	awaitMeshFixture(t, a, b)
	return a, b
}

// Authenticate both UDP paths before exercising a core built on this fixture.
func awaitMeshFixture(t *testing.T, a, b *mesh.Node) {
	t.Helper()
	ready := func(n *mesh.Node) bool { states := n.Status(); return len(states) == 1 && states[0].Path == "direct" }
	deadline := time.Now().Add(10 * time.Second)
	for !ready(a) || !ready(b) {
		if time.Now().After(deadline) {
			t.Fatalf("mesh fixture path not ready: %v / %v", a.Status(), b.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// SetPeer starts WireGuard keepalive immediately, so its first initiation can
// reach the other node before its reciprocal peer is installed. For actual
// routing tests, establish one encrypted exchange before testing mihomo policy;
// the routing assertions retain their original short deadlines. DNS-only tests
// do not depend on an unrelated application TCP exchange.
func awaitMeshTCPFixture(t *testing.T, a, b *mesh.Node) {
	t.Helper()
	listener, err := b.ListenTCP(0)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	payload := []byte("wireguard-fixture-ready")
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
		_, err = io.CopyN(conn, conn, int64(len(payload)))
		done <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := a.DialContext(ctx, "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("mesh fixture handshake: %v; paths %v / %v", err, a.Status(), b.Status())
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err = conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	echoed := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, echoed); err != nil {
		t.Fatalf("mesh fixture echo: %v", err)
	}
	if !bytes.Equal(payload, echoed) {
		t.Fatal("mesh fixture payload corrupted")
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("mesh fixture echo did not finish")
	}
}
func serveEcho(t *testing.T, listener net.Listener) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); c.SetDeadline(time.Now().Add(30 * time.Second)); io.Copy(c, c) }()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
}
func socksConn(t *testing.T, core *Core, target string) net.Conn {
	t.Helper()
	dialer, err := proxy.SOCKS5("tcp", core.MixedAddress(), nil, &net.Dialer{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	c, err := dialer.Dial("tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(8 * time.Second))
	return c
}
func assertEcho(t *testing.T, c net.Conn) {
	t.Helper()
	payload := []byte("real-application-payload-over-mihomo-and-wireguard")
	if _, err := c.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("payload mismatch")
	}
}
func TestPrivateTrafficWinsEveryPublicModeAndBypass(t *testing.T) {
	a, b := makeMeshPair(t)
	awaitMeshTCPFixture(t, a, b)
	listener, err := b.ListenTCP(0)
	if err != nil {
		t.Fatal(err)
	}
	serveEcho(t, listener)
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	core, err := Start(Config{StateDir: t.TempDir(), TUNFD: -1, Mesh: a, Peers: []PeerName{{IP: "100.96.0.2", Hostname: "mac.jungo.internal"}}, ProxyEnabled: true, Mode: "global", Selected: "REJECT", Profile: []byte("rules:\n  - MATCH,REJECT\n"), BypassUIDs: []int{10042}, OwnerUID: func(int, string, int, string, int) int { return 10042 }, MixedAddress: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	for _, mode := range []string{"rule", "global", "direct"} {
		for _, enabled := range []bool{true, false} {
			for _, bypass := range [][]int{nil, {10042}} {
				if err = core.SetPolicy(enabled, mode, "REJECT", bypass); err != nil {
					t.Fatal(err)
				}
				if tunnel.Mode() != tunnel.Rule {
					t.Fatal("public mode overrode core Rule mode")
				}
				for _, host := range []string{"100.96.0.2", "mac.jungo.internal"} {
					c := socksConn(t, core, net.JoinHostPort(host, port))
					assertEcho(t, c)
					c.Close()
				}
			}
		}
	}
	// UDP travels through the same mesh outbound used by the single TUN.
	udp, err := b.ListenUDP(0)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	udp.SetDeadline(time.Now().Add(5 * time.Second))
	go func() {
		p := make([]byte, 2048)
		n, from, e := udp.ReadFrom(p)
		if e == nil {
			udp.WriteTo(p[:n], from)
		}
	}()
	target := netip.MustParseAddrPort(udp.LocalAddr().String())
	meta := &C.Metadata{NetWork: C.UDP, DstIP: target.Addr(), DstPort: target.Port()}
	pc, err := core.parsed.Proxies[meshName].ListenPacketContext(context.Background(), meta)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	pc.SetDeadline(time.Now().Add(5 * time.Second))
	want := []byte("udp-through-real-wireguard")
	if _, err = pc.WriteTo(want, net.UDPAddrFromAddrPort(target)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:n], want) {
		t.Fatal("UDP payload mismatch")
	}
	// A live imported profile never replaces the application listener or mesh.
	before := core.mixed
	persistent := socksConn(t, core, net.JoinHostPort("mac.jungo.internal", port))
	defer persistent.Close()
	assertEcho(t, persistent)
	if err = core.LoadProfile([]byte("rules:\n  - NO-SUCH-RULE,x,DIRECT\n")); err == nil {
		t.Fatal("invalid profile was accepted")
	}
	assertEcho(t, persistent)
	if err = core.LoadProfile([]byte("proxies:\n  - {name: local-direct, type: direct}\nrules:\n  - MATCH,local-direct\n")); err != nil {
		t.Fatal(err)
	}
	if core.mixed != before || core.mesh.Load().node != a {
		t.Fatal("profile update restarted network transport")
	}
	assertEcho(t, persistent)
	nodes := core.Nodes()
	if len(nodes) != 1 || nodes[0].Name != "local-direct" || nodes[0].Delay != -1 {
		t.Fatalf("bad nodes: %+v", nodes)
	}
	if err = core.SetMesh(nil, nil); err != nil {
		t.Fatal(err)
	}
	persistent.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = persistent.Read(make([]byte, 1)); err == nil {
		t.Fatal("existing mesh stream survived disabling mesh")
	}
	_, err = core.parsed.Proxies[meshName].DialContext(context.Background(), &C.Metadata{DstIP: netip.MustParseAddr("100.96.0.2"), DstPort: target.Port()})
	if err == nil {
		t.Fatal("disabled mesh fell back to public transport")
	}
}

func TestUIDBypassOnlyChangesPublicTraffic(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveEcho(t, listener)
	var protected atomic.Int32
	core, err := Start(Config{StateDir: t.TempDir(), TUNFD: -1, Mode: "global", ProxyEnabled: true, Selected: "REJECT", BypassUIDs: []int{10042}, OwnerUID: func(int, string, int, string, int) int { return 10042 }, ProtectSocket: func(int) bool { protected.Add(1); return true }, MixedAddress: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	c := socksConn(t, core, listener.Addr().String())
	assertEcho(t, c)
	c.Close()
	if protected.Load() == 0 {
		t.Fatal("outbound sockets did not invoke VPN protection")
	}
	meta := &C.Metadata{Uid: 10042, DstIP: netip.MustParseAddr("100.96.255.254")}
	matched := false
	for _, r := range core.parsed.Rules {
		if ok, target := r.Match(meta, C.RuleMatchHelper{}); ok {
			matched = true
			if target != meshName {
				t.Fatalf("private traffic went to %s", target)
			}
			break
		}
	}
	if !matched {
		t.Fatal("private address was not captured")
	}
}

type noUpstream struct{ calls atomic.Int32 }

func (s *noUpstream) ServeMsg(context.Context, *D.Msg) (*D.Msg, error) {
	s.calls.Add(1)
	return nil, errors.New("unexpected DNS leak")
}
func TestPrivateDNSNeverLeaksAndUpdates(t *testing.T) {
	a, _ := makeMeshPair(t)
	c := &Core{}
	if err := c.setMesh(a, []PeerName{{IP: "100.96.0.2", Hostname: "mac.jungo.internal"}}); err != nil {
		t.Fatal(err)
	}
	upstream := &noUpstream{}
	service := &meshDNSService{core: c, fallback: upstream}
	for _, host := range []string{"mac.jungo.internal.", "missing.jungo.internal."} {
		q := new(D.Msg)
		q.SetQuestion(host, D.TypeA)
		answer, err := service.ServeMsg(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(host, "missing") {
			if answer.Rcode != D.RcodeNameError {
				t.Fatal("missing private name was not NXDOMAIN")
			}
		} else if len(answer.Answer) != 1 || answer.Answer[0].(*D.A).A.String() != "100.96.0.2" {
			t.Fatalf("bad DNS answer %v", answer)
		}
	}
	if upstream.calls.Load() != 0 {
		t.Fatal("private DNS leaked")
	}
	if err := c.SetMesh(nil, nil); err != nil {
		t.Fatal(err)
	}
	q := new(D.Msg)
	q.SetQuestion("mac.jungo.internal.", D.TypeA)
	answer, err := service.ServeMsg(context.Background(), q)
	if err != nil || answer.Rcode != D.RcodeNameError {
		t.Fatal("disabled mesh DNS retained stale address")
	}
}

func TestProfileSanitizationAndFDConsumption(t *testing.T) {
	root := t.TempDir()
	data, err := sanitizeProfile([]byte("mixed-port: 9999\nexternal-controller: 0.0.0.0:9090\ntun: {enable: true, file-descriptor: 55}\nlisteners: [{name: bad, type: socks, port: 8000}]\nrules: [MATCH,DIRECT]\n"), root)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err = yaml.Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"mixed-port", "external-controller", "tun", "listeners"} {
		if _, ok := output[key]; ok {
			t.Fatalf("profile enabled %s", key)
		}
	}
	outside := t.TempDir()
	if err = os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside.yaml", filepath.Join(outside, "secrets"), "escape/secrets"} {
		if _, err = containedPath(root, path); err == nil {
			t.Fatalf("allowed provider path %s", path)
		}
	}
	for _, bad := range []string{"proxies: wrong", "proxies: [{name: JUNGO-MESH, type: direct}]", "proxies: [{name: JUNGO-PUBLIC, type: direct}]"} {
		if _, err = sanitizeProfile([]byte(bad), root); err == nil {
			t.Fatalf("accepted malformed/reserved config %s", bad)
		}
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fd, err := unix.Dup(int(r.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Start(Config{StateDir: root, TUNFD: fd, Profile: []byte("bad: [")}); err == nil {
		t.Fatal("invalid profile started")
	}
	if _, err = unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
		t.Fatalf("Start failure did not consume TUN fd: %v", err)
	}
}
