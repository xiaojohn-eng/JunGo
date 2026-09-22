package mesh

import (
	"context"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.zx2c4.com/wireguard/conn"
)

const proofLifetime = 3 * time.Second
const probeInterval = time.Second
const probeSize = 4 + 1 + 32 + 16 + 32

type wirePacket struct {
	peerID  string
	source  netip.AddrPort
	payload []byte
}
type bindSession struct {
	udp     *net.UDPConn
	done    chan struct{}
	packets chan wirePacket
	workers sync.WaitGroup
}
type bindPeer struct {
	public     [32]byte
	secret     []byte
	candidates []netip.AddrPort
	proof      map[netip.AddrPort]time.Time
}
type pendingProbe struct {
	peerID string
	target netip.AddrPort
	sent   time.Time
}

type meshBind struct {
	mu           sync.Mutex
	id           string
	key          *ecdh.PrivateKey
	public       []byte
	protect      func(int) error
	control      func(string, string, syscall.RawConn) error
	relay        Relay
	forceRelay   bool
	peers        map[string]*bindPeer
	byPublic     map[[32]byte]string
	pending      map[[16]byte]pendingProbe
	stun         map[[12]byte]stunRequest
	session      *bindSession
	stopped      bool
	stop         chan struct{}
	relayOnce    sync.Once
	relayWorkers sync.WaitGroup
	relayErr     error
}

func newMeshBind(cfg Config, key *ecdh.PrivateKey) *meshBind {
	return &meshBind{id: cfg.ID, key: key, public: key.PublicKey().Bytes(), protect: cfg.ProtectSocket, control: cfg.Control,
		relay: cfg.Relay, forceRelay: cfg.ForceRelay, peers: make(map[string]*bindPeer), byPublic: make(map[[32]byte]string),
		pending: make(map[[16]byte]pendingProbe), stun: make(map[[12]byte]stunRequest), stop: make(chan struct{})}
}

func (b *meshBind) setPeer(p Peer) error {
	publicBytes, _ := hex.DecodeString(p.PublicKey)
	publicKey, err := ecdh.X25519().NewPublicKey(publicBytes)
	if err != nil {
		return err
	}
	shared, err := b.key.ECDH(publicKey)
	if err != nil {
		return fmt.Errorf("invalid peer public key: %w", err)
	}
	// A separate domain-separated authentication key proves UDP path reachability;
	// WireGuard alone encrypts/authenticates all application traffic.
	mac := hmac.New(sha256.New, shared)
	mac.Write([]byte("junge-mesh-udp-path-proof-v1"))
	secret := mac.Sum(nil)
	if len(p.Endpoints) > 32 {
		return errors.New("at most 32 UDP endpoints per peer")
	}
	var candidates []netip.AddrPort
	seen := make(map[netip.AddrPort]bool)
	for _, value := range p.Endpoints {
		ap, err := netip.ParseAddrPort(value)
		if err != nil || !ap.Addr().Is4() || ap.Port() == 0 || ap.Addr().IsUnspecified() || ap.Addr().IsMulticast() {
			return fmt.Errorf("invalid IPv4 UDP endpoint %q", value)
		}
		if !seen[ap] {
			candidates = append(candidates, ap)
			seen[ap] = true
		}
	}
	var pub [32]byte
	copy(pub[:], publicBytes)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return net.ErrClosed
	}
	proof := make(map[netip.AddrPort]time.Time)
	if old := b.peers[p.ID]; old != nil {
		for ap, when := range old.proof {
			if seen[ap] {
				proof[ap] = when
			}
		}
		delete(b.byPublic, old.public)
	}
	for nonce, pending := range b.pending {
		if pending.peerID == p.ID {
			delete(b.pending, nonce)
		}
	}
	b.peers[p.ID] = &bindPeer{public: pub, secret: secret, candidates: candidates, proof: proof}
	b.byPublic[pub] = p.ID
	return nil
}

func (b *meshBind) removePeer(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if p := b.peers[id]; p != nil {
		delete(b.byPublic, p.public)
	}
	delete(b.peers, id)
	for nonce, p := range b.pending {
		if p.peerID == id {
			delete(b.pending, nonce)
		}
	}
}

func (b *meshBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return nil, 0, net.ErrClosed
	}
	if b.session != nil {
		return nil, 0, conn.ErrBindAlreadyOpen
	}
	lc := net.ListenConfig{Control: func(network, address string, raw syscall.RawConn) error {
		if b.control != nil {
			if err := b.control(network, address, raw); err != nil {
				return err
			}
		}
		if b.protect == nil {
			return nil
		}
		var protectErr error
		if err := raw.Control(func(fd uintptr) { protectErr = b.protect(int(fd)) }); err != nil {
			return err
		}
		return protectErr
	}}
	pc, err := lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return nil, 0, err
	}
	udp := pc.(*net.UDPConn)
	s := &bindSession{udp: udp, done: make(chan struct{}), packets: make(chan wirePacket, 256)}
	b.session = s
	s.workers.Add(2)
	go func() { defer s.workers.Done(); b.readUDP(s) }()
	go func() { defer s.workers.Done(); b.probeLoop(s) }()
	if b.relay != nil {
		b.relayOnce.Do(func() { b.relayWorkers.Add(1); go func() { defer b.relayWorkers.Done(); b.readRelay() }() })
	}
	recv := func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		select {
		case <-s.done:
			return 0, net.ErrClosed
		default:
		}
		select {
		case <-s.done:
			return 0, net.ErrClosed
		case p := <-s.packets:
			if len(packets) == 0 || len(sizes) == 0 || len(eps) == 0 {
				return 0, errors.New("empty receive buffers")
			}
			if len(p.payload) > len(packets[0]) {
				sizes[0] = 0
				return 1, nil
			}
			sizes[0] = copy(packets[0], p.payload)
			eps[0] = &meshEndpoint{peerID: p.peerID, source: p.source}
			return 1, nil
		}
	}
	return []conn.ReceiveFunc{recv}, uint16(udp.LocalAddr().(*net.UDPAddr).Port), nil
}

func (b *meshBind) Close() error {
	b.mu.Lock()
	s := b.session
	if s == nil {
		b.mu.Unlock()
		return nil
	}
	b.session = nil
	close(s.done)
	err := s.udp.Close()
	b.mu.Unlock()
	s.workers.Wait()
	return err
}

func (b *meshBind) shutdown() {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}
	b.stopped = true
	close(b.stop)
	b.mu.Unlock()
	b.Close()
	if b.relay != nil {
		b.relay.Close()
	}
	b.relayWorkers.Wait()
}

func (b *meshBind) BatchSize() int { return 1 }
func (b *meshBind) SetMark(mark uint32) error {
	if mark != 0 {
		return errors.New("socket mark unsupported by userspace mesh bind")
	}
	return nil
}
func (b *meshBind) ParseEndpoint(value string) (conn.Endpoint, error) {
	if !strings.HasPrefix(value, "peer:") || !validID(strings.TrimPrefix(value, "peer:")) {
		return nil, errors.New("mesh endpoint must be peer:<device ID>")
	}
	id := strings.TrimPrefix(value, "peer:")
	b.mu.Lock()
	_, ok := b.peers[id]
	b.mu.Unlock()
	if !ok {
		return nil, errors.New("unknown mesh peer")
	}
	return &meshEndpoint{peerID: id}, nil
}

func (b *meshBind) Send(packets [][]byte, endpoint conn.Endpoint) error {
	ep, ok := endpoint.(*meshEndpoint)
	if !ok {
		return conn.ErrWrongEndpointType
	}
	b.mu.Lock()
	s, p := b.session, b.peers[ep.peerID]
	if b.stopped || s == nil {
		b.mu.Unlock()
		return net.ErrClosed
	}
	if p == nil {
		b.mu.Unlock()
		return errors.New("peer revoked")
	}
	best, _ := bestDirect(p, time.Now())
	if b.forceRelay {
		best = netip.AddrPort{}
	}
	relay, relayErr := b.relay, b.relayErr
	b.mu.Unlock()
	for _, packet := range packets {
		if best.IsValid() {
			if _, err := s.udp.WriteToUDPAddrPort(packet, best); err == nil {
				continue
			}
		}
		if relay != nil && relayErr == nil {
			if err := relay.Send(ep.peerID, packet); err == nil {
				continue
			}
		}
		if b.forceRelay {
			return errors.New("relay unavailable")
		}
		// Without relay, initial handshakes may race the path probes. Sending
		// ciphertext to candidate addresses cannot expose application content.
		// The normal direct/relay paths do not need a candidate snapshot per
		// encrypted packet. Only the unproven-path fallback takes an owned copy.
		b.mu.Lock()
		current := b.peers[ep.peerID]
		if current == nil {
			b.mu.Unlock()
			return errors.New("peer revoked")
		}
		candidates := append([]netip.AddrPort(nil), current.candidates...)
		b.mu.Unlock()
		var sent bool
		for _, ap := range candidates {
			if _, err := s.udp.WriteToUDPAddrPort(packet, ap); err == nil {
				sent = true
			}
		}
		if !sent {
			return errors.New("no reachable mesh transport")
		}
	}
	return nil
}

func bestDirect(p *bindPeer, now time.Time) (netip.AddrPort, time.Time) {
	var best netip.AddrPort
	var proof time.Time
	for ap, t := range p.proof {
		if now.Sub(t) > proofLifetime {
			continue
		}
		if !best.IsValid() || endpointPriority(ap) < endpointPriority(best) || endpointPriority(ap) == endpointPriority(best) && ap.String() < best.String() {
			best, proof = ap, t
		}
	}
	return best, proof
}
func endpointPriority(ap netip.AddrPort) int {
	if ap.Addr().IsLoopback() {
		return 0
	}
	if ap.Addr().IsPrivate() {
		return 1
	}
	return 2
}

func (b *meshBind) readUDP(s *bindSession) {
	packet := make([]byte, 65535)
	for {
		n, source, err := s.udp.ReadFromUDPAddrPort(packet)
		if err != nil {
			return
		}
		body := packet[:n]
		if b.handleSTUN(body, source) {
			continue
		}
		if len(body) >= 4 && string(body[:4]) == "JMP1" {
			b.handleProbe(s, body, source)
			continue
		}
		if b.forceRelay {
			continue
		}
		b.mu.Lock()
		var id string
		for peerID, p := range b.peers {
			if t, ok := p.proof[source]; ok && time.Since(t) <= proofLifetime {
				id = peerID
				break
			}
			for _, ap := range p.candidates {
				if ap == source {
					id = peerID
					break
				}
			}
			if id != "" {
				break
			}
		}
		b.mu.Unlock()
		if id == "" {
			continue
		}
		p := wirePacket{peerID: id, source: source, payload: append([]byte(nil), body...)}
		select {
		case s.packets <- p:
		case <-s.done:
			return
		default:
		}
	}
}

func (b *meshBind) readRelay() {
	for {
		id, packet, err := b.relay.Receive()
		if err != nil {
			b.mu.Lock()
			b.relayErr = err
			b.mu.Unlock()
			return
		}
		b.mu.Lock()
		s := b.session
		_, ok := b.peers[id]
		b.mu.Unlock()
		if !ok || s == nil || len(packet) == 0 || len(packet) > 65535 {
			continue
		}
		p := wirePacket{peerID: id, payload: append([]byte(nil), packet...)}
		select {
		case s.packets <- p:
		case <-s.done:
		case <-b.stop:
			return
		default:
		}
	}
}

func probePacket(kind byte, public, secret []byte, nonce [16]byte) []byte {
	packet := make([]byte, probeSize)
	copy(packet, "JMP1")
	packet[4] = kind
	copy(packet[5:37], public)
	copy(packet[37:53], nonce[:])
	mac := hmac.New(sha256.New, secret)
	mac.Write(packet[:53])
	copy(packet[53:], mac.Sum(nil))
	return packet
}

func (b *meshBind) handleProbe(s *bindSession, packet []byte, source netip.AddrPort) {
	if b.forceRelay || len(packet) != probeSize || (packet[4] != 1 && packet[4] != 2) {
		return
	}
	var public [32]byte
	copy(public[:], packet[5:37])
	var nonce [16]byte
	copy(nonce[:], packet[37:53])
	b.mu.Lock()
	id, ok := b.byPublic[public]
	p := b.peers[id]
	if !ok || p == nil {
		b.mu.Unlock()
		return
	}
	mac := hmac.New(sha256.New, p.secret)
	mac.Write(packet[:53])
	if !hmac.Equal(mac.Sum(nil), packet[53:]) {
		b.mu.Unlock()
		return
	}
	if packet[4] == 2 {
		pending, ok := b.pending[nonce]
		if ok && pending.peerID == id && pending.target == source && time.Since(pending.sent) <= proofLifetime {
			p.proof[source] = time.Now()
			delete(b.pending, nonce)
		}
		b.mu.Unlock()
		return
	}
	// A valid request only nominates a candidate. It does not prove a round
	// trip; our own unpredictable nonce must come back before selecting it.
	found := false
	for _, ap := range p.candidates {
		if ap == source {
			found = true
			break
		}
	}
	if !found && len(p.candidates) < 64 {
		p.candidates = append(p.candidates, source)
	}
	response := probePacket(2, b.public, p.secret, nonce)
	b.mu.Unlock()
	s.udp.WriteToUDPAddrPort(response, source)
}

func (b *meshBind) probeLoop(s *bindSession) {
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			b.sendProbes(s)
		}
	}
}
func (b *meshBind) sendProbes(s *bindSession) {
	if b.forceRelay {
		return
	}
	type send struct {
		packet []byte
		target netip.AddrPort
	}
	var sends []send
	now := time.Now()
	b.mu.Lock()
	for nonce, p := range b.pending {
		if now.Sub(p.sent) > proofLifetime {
			delete(b.pending, nonce)
		}
	}
	for id, p := range b.peers {
		for _, ap := range p.candidates {
			var nonce [16]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				continue
			}
			b.pending[nonce] = pendingProbe{peerID: id, target: ap, sent: now}
			sends = append(sends, send{probePacket(1, b.public, p.secret, nonce), ap})
		}
	}
	b.mu.Unlock()
	for _, out := range sends {
		s.udp.WriteToUDPAddrPort(out.packet, out.target)
	}
}

func (b *meshBind) localEndpoints() []string {
	b.mu.Lock()
	s := b.session
	b.mu.Unlock()
	if s == nil {
		return nil
	}
	port := uint16(s.udp.LocalAddr().(*net.UDPAddr).Port)
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var endpoints []string
	for _, addr := range addrs {
		prefix, err := netip.ParsePrefix(addr.String())
		if err != nil || !prefix.Addr().Is4() || prefix.Addr().IsUnspecified() || prefix.Addr().IsLoopback() || prefix.Addr().IsLinkLocalUnicast() {
			continue
		}
		endpoints = append(endpoints, netip.AddrPortFrom(prefix.Addr(), port).String())
	}
	sort.Strings(endpoints)
	return endpoints
}

func (b *meshBind) status() []PeerStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []PeerStatus
	for id, p := range b.peers {
		status := PeerStatus{ID: id, Path: "unavailable"}
		ap, proof := bestDirect(p, time.Now())
		if ap.IsValid() && !b.forceRelay && !b.stopped && b.session != nil {
			status.Path = "direct"
			status.Endpoint = ap.String()
			status.LastDirectProof = proof
		} else if b.relayAvailable() && !b.stopped && b.session != nil {
			status.Path = "relay"
		}
		out = append(out, status)
	}
	return out
}

// Call while holding b.mu. Simple relays are live until Receive fails;
// reconnecting implementations may report their temporary disconnection.
func (b *meshBind) relayAvailable() bool {
	if b.relay == nil || b.relayErr != nil {
		return false
	}
	if state, ok := b.relay.(interface{ Connected() bool }); ok {
		return state.Connected()
	}
	return true
}

type meshEndpoint struct {
	peerID string
	source netip.AddrPort
}

func (e *meshEndpoint) ClearSrc()           {}
func (e *meshEndpoint) SrcToString() string { return "" }
func (e *meshEndpoint) DstToString() string { return "peer:" + e.peerID }
func (e *meshEndpoint) DstToBytes() []byte {
	if e.source.IsValid() {
		return []byte(e.source.String())
	}
	return []byte("relay:" + e.peerID)
}
func (e *meshEndpoint) DstIP() netip.Addr { return e.source.Addr() }
func (e *meshEndpoint) SrcIP() netip.Addr { return netip.Addr{} }

var _ conn.Bind = (*meshBind)(nil)
