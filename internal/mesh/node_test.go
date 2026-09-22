package mesh

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memoryHub struct {
	mu        sync.Mutex
	peers     map[string]*memoryRelay
	packets   atomic.Int64
	plaintext atomic.Bool
	types     [5]atomic.Int64
}
type memoryRelay struct {
	id       string
	hub      *memoryHub
	incoming chan wirePacket
	done     chan struct{}
	once     sync.Once
}

func (h *memoryHub) join(id string) *memoryRelay {
	r := &memoryRelay{id: id, hub: h, incoming: make(chan wirePacket, 1024), done: make(chan struct{})}
	h.mu.Lock()
	if h.peers == nil {
		h.peers = make(map[string]*memoryRelay)
	}
	h.peers[id] = r
	h.mu.Unlock()
	return r
}
func (r *memoryRelay) Send(id string, p []byte) error {
	r.hub.mu.Lock()
	target := r.hub.peers[id]
	r.hub.mu.Unlock()
	if target == nil {
		return errors.New("unknown relay destination")
	}
	if bytes.Contains(p, []byte("mesh-secret-application-payload")) {
		r.hub.plaintext.Store(true)
	}
	r.hub.packets.Add(1)
	if len(p) >= 4 {
		kind := binary.LittleEndian.Uint32(p[:4])
		if kind < uint32(len(r.hub.types)) {
			r.hub.types[kind].Add(1)
		}
	}
	select {
	case <-r.done:
		return net.ErrClosed
	default:
	}
	packet := wirePacket{peerID: r.id, payload: append([]byte(nil), p...)}
	select {
	case target.incoming <- packet:
		return nil
	case <-target.done:
		return net.ErrClosed
	case <-r.done:
		return net.ErrClosed
	}
}
func (r *memoryRelay) Receive() (string, []byte, error) {
	select {
	case p := <-r.incoming:
		return p.peerID, p.payload, nil
	case <-r.done:
		return "", nil, net.ErrClosed
	}
}
func (r *memoryRelay) Close() error { r.once.Do(func() { close(r.done) }); return nil }

func testNode(t *testing.T, id, ip string, relay Relay, force bool) (*Node, string) {
	t.Helper()
	private, public, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	n, err := New(Config{ID: id, PrivateKey: private, Address: ip, Relay: relay, ForceRelay: force})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			// Keep diagnostic output limited to counters: IpcGet also contains
			// private keys and must never be printed without filtering.
			state, _ := n.device.IpcGet()
			var counters []string
			for _, line := range strings.Split(state, "\n") {
				if strings.HasPrefix(line, "last_handshake_time_") || strings.HasPrefix(line, "rx_bytes=") || strings.HasPrefix(line, "tx_bytes=") {
					counters = append(counters, line)
				}
			}
			t.Logf("device %s WireGuard counters: %v", id, counters)
		}
		n.Close()
	})
	return n, public
}
func udpAddress(n *Node) string {
	n.bind.mu.Lock()
	defer n.bind.mu.Unlock()
	return netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(n.bind.session.udp.LocalAddr().(*net.UDPAddr).Port)).String()
}
func awaitPath(t *testing.T, n *Node, path string) {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		s := n.Status()
		if len(s) == 1 && s[0].Path == path {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("wanted path %s, got %+v", path, n.Status())
}

const exchangeTimeout = 30 * time.Second

func exchangeTCP(t *testing.T, a, b *Node) {
	t.Helper()
	started := time.Now()
	want := bytes.Repeat([]byte("mesh-secret-application-payload"), 64)
	listener, err := b.ListenTCP(0)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		c, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(exchangeTimeout))
		// A finite echo verifies the payload without also waiting for TCP FIN
		// retransmissions under the race detector and concurrent host builds.
		_, err = io.CopyN(c, c, int64(len(want)))
		done <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), exchangeTimeout)
	defer cancel()
	c, err := a.DialContext(ctx, "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("TCP connect after %s: %v; paths %v / %v", time.Since(started), err, a.Status(), b.Status())
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(exchangeTimeout))
	if _, err = c.Write(want); err != nil {
		t.Fatalf("TCP write after %s: %v", time.Since(started), err)
	}
	got := make([]byte, len(want))
	if _, err = io.ReadFull(c, got); err != nil {
		t.Fatalf("TCP echo read after %s: %v; paths %v / %v", time.Since(started), err, a.Status(), b.Status())
	}
	if !bytes.Equal(got, want) {
		t.Fatal("TCP payload mismatch")
	}
	c.Close()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(exchangeTimeout):
		t.Fatal("TCP echo goroutine did not finish after verified payload")
	}
}
func exchangeUDP(t *testing.T, a, b *Node) {
	t.Helper()
	listener, err := b.ListenUDP(0)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		listener.SetDeadline(time.Now().Add(exchangeTimeout))
		buf := make([]byte, 2048)
		n, addr, err := listener.ReadFrom(buf)
		if err == nil {
			_, err = listener.WriteTo(buf[:n], addr)
		}
		done <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), exchangeTimeout)
	defer cancel()
	c, err := a.DialContext(ctx, "udp", listener.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(exchangeTimeout))
	want := []byte("mesh-secret-application-payload-udp")
	if _, err = c.Write(want); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 2048)
	size, err := c.Read(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[:size], want) {
		t.Fatal("UDP payload mismatch")
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

// Both transports carry real TCP and UDP through independent WireGuard and
// gVisor stacks. No system TUN, root privileges, or external network is used.
func TestWireGuardDirectAndRelay(t *testing.T) {
	for _, force := range []bool{false, true} {
		name := "direct"
		if force {
			name = "UDP_blocked_relay"
		}
		t.Run(name, func(t *testing.T) {
			hub := &memoryHub{}
			defer func() {
				if t.Failed() {
					t.Logf("relay packets %d, handshake-init %d, handshake-response %d, cookie %d, transport %d", hub.packets.Load(), hub.types[1].Load(), hub.types[2].Load(), hub.types[3].Load(), hub.types[4].Load())
				}
			}()
			var ra, rb Relay
			if force {
				ra = hub.join("a")
				rb = hub.join("b")
			}
			a, apub := testNode(t, "a", "100.96.0.1", ra, force)
			b, bpub := testNode(t, "b", "100.96.0.2", rb, force)
			if err := a.SetPeer(Peer{ID: "b", Address: b.address.String(), PublicKey: bpub, Endpoints: []string{udpAddress(b)}}); err != nil {
				t.Fatal(err)
			}
			if err := b.SetPeer(Peer{ID: "a", Address: a.address.String(), PublicKey: apub, Endpoints: []string{udpAddress(a)}}); err != nil {
				t.Fatal(err)
			}
			path := "direct"
			if force {
				path = "relay"
			}
			awaitPath(t, a, path)
			awaitPath(t, b, path)
			exchangeTCP(t, a, b)
			exchangeUDP(t, a, b)
			if force && hub.packets.Load() == 0 {
				t.Fatal("relay was not used")
			}
			if hub.plaintext.Load() {
				t.Fatal("relay observed application plaintext")
			}
			if err := a.RemovePeer("b"); err != nil {
				t.Fatal(err)
			}
			if _, err := a.DialContext(context.Background(), "tcp", "100.96.0.2:8443"); err == nil {
				t.Fatal("revoked peer remains dialable")
			}
		})
	}
}

func TestDirectFallbackAndRecovery(t *testing.T) {
	hub := &memoryHub{}
	a, apub := testNode(t, "a", "100.96.0.11", hub.join("a"), false)
	b, bpub := testNode(t, "b", "100.96.0.12", hub.join("b"), false)
	bridge, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bridge.Close() })
	var blocked atomic.Bool
	bridgeDone := make(chan struct{})
	aUDP, bUDP := netip.MustParseAddrPort(udpAddress(a)), netip.MustParseAddrPort(udpAddress(b))
	go func() {
		defer close(bridgeDone)
		buf := make([]byte, 65535)
		for {
			size, from, err := bridge.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			if blocked.Load() {
				continue
			}
			if from == aUDP {
				bridge.WriteToUDPAddrPort(buf[:size], bUDP)
			} else if from == bUDP {
				bridge.WriteToUDPAddrPort(buf[:size], aUDP)
			}
		}
	}()
	t.Cleanup(func() { bridge.Close(); <-bridgeDone })
	pa := Peer{ID: "a", Address: a.address.String(), PublicKey: apub, Endpoints: []string{bridge.LocalAddr().String()}}
	pb := Peer{ID: "b", Address: b.address.String(), PublicKey: bpub, Endpoints: []string{bridge.LocalAddr().String()}}
	if err := a.SetPeer(pb); err != nil {
		t.Fatal(err)
	}
	if err := b.SetPeer(pa); err != nil {
		t.Fatal(err)
	}
	awaitPath(t, a, "direct")
	awaitPath(t, b, "direct")
	exchangeTCP(t, a, b)
	// Drop the actual UDP network path, including its reachability probes.
	// Peer identity, candidates, WireGuard keys and existing sessions stay intact.
	blocked.Store(true)
	awaitPath(t, a, "relay")
	awaitPath(t, b, "relay")
	before := hub.packets.Load()
	exchangeTCP(t, a, b)
	exchangeUDP(t, a, b)
	if hub.packets.Load() <= before {
		t.Fatal("fallback did not relay packets")
	}
	blocked.Store(false)
	awaitPath(t, a, "direct")
	awaitPath(t, b, "direct")
	exchangeTCP(t, a, b)
	exchangeUDP(t, a, b)
}

func TestRejectsUnenrolledAndInvalidPeers(t *testing.T) {
	n, _ := testNode(t, "self", "100.96.0.21", nil, false)
	_, public, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, peer := range []Peer{
		{ID: "bad\nline", Address: "100.96.0.22", PublicKey: public},
		{ID: "peer", Address: "8.8.8.8", PublicKey: public},
		{ID: "peer", Address: "100.96.0.22", PublicKey: strings.Repeat("0", 64)},
		{ID: "peer", Address: "100.96.0.22", PublicKey: public, Endpoints: []string{"example.com:9999"}},
	} {
		if err := n.SetPeer(peer); err == nil {
			t.Fatalf("accepted invalid peer %+v", peer)
		}
	}
	for _, target := range []string{"8.8.8.8:443", "100.96.0.22:443", "example.com:443"} {
		if _, err := n.DialContext(context.Background(), "tcp", target); err == nil {
			t.Fatalf("host fallback allowed for %s", target)
		}
	}
}

func TestSTUNUsesWireGuardSocket(t *testing.T) {
	n, _ := testNode(t, "a", "100.96.0.31", nil, false)
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.SetDeadline(time.Now().Add(4 * time.Second))
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 256)
		size, from, err := server.ReadFromUDPAddrPort(buf)
		if err != nil {
			done <- err
			return
		}
		if size != 20 {
			done <- errors.New("bad STUN request")
			return
		}
		packet := make([]byte, 32)
		binary.BigEndian.PutUint16(packet[:2], 0x0101)
		binary.BigEndian.PutUint16(packet[2:4], 12)
		binary.BigEndian.PutUint32(packet[4:8], stunMagic)
		copy(packet[8:20], buf[8:20])
		binary.BigEndian.PutUint16(packet[20:22], 0x20)
		binary.BigEndian.PutUint16(packet[22:24], 8)
		packet[25] = 1
		binary.BigEndian.PutUint16(packet[26:28], from.Port()^uint16(stunMagic>>16))
		ip := from.Addr().As4()
		binary.BigEndian.PutUint32(packet[28:32], binary.BigEndian.Uint32(ip[:])^stunMagic)
		_, err = server.WriteToUDPAddrPort(packet, from)
		done <- err
	}()
	mapped, err := n.DiscoverSTUN(context.Background(), server.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	if mapped != udpAddress(n) {
		t.Fatalf("STUN mapped %s instead of WireGuard socket %s", mapped, udpAddress(n))
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSocketProtectionAndClose(t *testing.T) {
	private, _, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	n, err := New(Config{ID: "a", PrivateKey: private, Address: "100.96.0.41", ProtectSocket: func(fd int) error {
		if fd < 0 {
			return errors.New("invalid descriptor")
		}
		calls.Add(1)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() == 0 {
		t.Fatal("VPN socket protection was not called")
	}
	if err = n.Close(); err != nil {
		t.Fatal(err)
	}
	if err = n.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = n.ListenTCP(1234); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed ListenTCP: %v", err)
	}
}

func TestSelfHostedSTUN(t *testing.T) {
	n, _ := testNode(t, "stun-client", "100.96.0.51", nil, false)
	server, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- ServeSTUN(server) }()
	t.Cleanup(func() {
		server.Close()
		if err := <-done; !errors.Is(err, net.ErrClosed) {
			t.Errorf("STUN shutdown: %v", err)
		}
	})
	result, err := n.DiscoverSTUN(context.Background(), server.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	if result != udpAddress(n) {
		t.Fatalf("self-hosted STUN mapped %q instead of %q", result, udpAddress(n))
	}
}
