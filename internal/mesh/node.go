// Package mesh provides an unprivileged WireGuard network. Only explicitly
// enrolled /32 peer addresses are routable; it never falls back to the host dialer.
package mesh

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
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

	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// Relay carries opaque WireGuard ciphertext. Receive must unblock on Close.
// The implementation must authenticate the sender identity and reconnect itself
// if desired. Send must be safe for concurrent use and copy retained packets.
type Relay interface {
	Send(peerID string, packet []byte) error
	Receive() (peerID string, packet []byte, err error)
	Close() error
}

type Config struct {
	ID         string
	PrivateKey string // 32-byte WireGuard private key in hex or standard base64
	Address    string // stable IPv4 virtual address
	ListenPort uint16
	Relay      Relay
	ForceRelay bool
	// ProtectSocket is called before binding UDP. Android must call
	// VpnService.protect(fd) here so encapsulated traffic bypasses its own VPN.
	ProtectSocket func(fd int) error
	Control       func(network, address string, conn syscall.RawConn) error
}

type Peer struct {
	ID        string   `json:"id"`
	Address   string   `json:"address"`
	PublicKey string   `json:"publicKey"`
	Endpoints []string `json:"endpoints"`
}

type PeerStatus struct {
	ID              string    `json:"id"`
	Path            string    `json:"path"` // direct, relay, or unavailable; selected transport, not presence
	Endpoint        string    `json:"endpoint,omitempty"`
	LastDirectProof time.Time `json:"lastDirectProof,omitempty"`
}

type Node struct {
	mu      sync.RWMutex
	device  *device.Device
	net     *netstack.Net
	bind    *meshBind
	address netip.Addr
	peers   map[string]Peer
	closed  bool
}

func GenerateKey() (privateHex, publicHex string, err error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return hex.EncodeToString(key.Bytes()), hex.EncodeToString(key.PublicKey().Bytes()), nil
}

func decodeKey(value string) ([]byte, error) {
	key, err := hex.DecodeString(value)
	if err != nil || len(key) != 32 {
		key, err = base64.StdEncoding.DecodeString(value)
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("WireGuard key must be 32 bytes encoded as hex or base64")
	}
	var nonzero byte
	for _, v := range key {
		nonzero |= v
	}
	if nonzero == 0 {
		return nil, errors.New("zero WireGuard key")
	}
	return key, nil
}

func New(cfg Config) (*Node, error) {
	if !validID(cfg.ID) {
		return nil, errors.New("invalid local device ID")
	}
	addr, err := parseVirtualAddress(cfg.Address)
	if err != nil || !validVirtualAddress(addr) {
		return nil, errors.New("virtual address must be a private IPv4 unicast address")
	}
	keyBytes, err := decodeKey(cfg.PrivateKey)
	if err != nil {
		return nil, err
	}
	key, err := ecdh.X25519().NewPrivateKey(keyBytes)
	if err != nil {
		return nil, err
	}
	if cfg.ForceRelay && cfg.Relay == nil {
		return nil, errors.New("ForceRelay requires a relay")
	}
	b := newMeshBind(cfg, key)
	tun, network, err := netstack.CreateNetTUN([]netip.Addr{addr}, nil, 1280)
	if err != nil {
		b.shutdown()
		return nil, err
	}
	d := device.NewDevice(tun, b, device.NewLogger(device.LogLevelSilent, "mesh: "))
	n := &Node{device: d, net: network, bind: b, address: addr, peers: make(map[string]Peer)}
	if err = d.IpcSet(fmt.Sprintf("private_key=%s\nlisten_port=%d\n", hex.EncodeToString(keyBytes), cfg.ListenPort)); err != nil {
		n.Close()
		return nil, err
	}
	if err = d.Up(); err != nil {
		n.Close()
		return nil, err
	}
	return n, nil
}

func validID(id string) bool {
	return len(id) > 0 && len(id) <= 128 && !strings.ContainsAny(id, "\x00\r\n:")
}
func validVirtualAddress(ip netip.Addr) bool {
	return ip.Is4() && (ip.IsPrivate() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip)) && !ip.IsUnspecified() && !ip.IsMulticast()
}

func parseVirtualAddress(value string) (netip.Addr, error) {
	if strings.Contains(value, "/") {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Bits() != 32 || !prefix.Addr().Is4() {
			return netip.Addr{}, errors.New("mesh requires an IPv4 /32")
		}
		return prefix.Addr(), nil
	}
	return netip.ParseAddr(value)
}

func (n *Node) SetPeer(peer Peer) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return net.ErrClosed
	}
	if !validID(peer.ID) || peer.ID == n.bind.id {
		return errors.New("invalid peer ID")
	}
	ip, err := parseVirtualAddress(peer.Address)
	if err != nil || !validVirtualAddress(ip) || ip == n.address {
		return errors.New("invalid peer virtual address")
	}
	peer.Address = ip.String()
	publicBytes, err := decodeKey(peer.PublicKey)
	if err != nil {
		return err
	}
	peer.PublicKey = hex.EncodeToString(publicBytes)
	if peer.PublicKey == hex.EncodeToString(n.bind.public) {
		return errors.New("peer public key cannot equal this device's public key")
	}
	for id, existing := range n.peers {
		if id != peer.ID && (existing.Address == peer.Address || existing.PublicKey == peer.PublicKey) {
			return errors.New("peer address and public key must be unique")
		}
	}
	if old, ok := n.peers[peer.ID]; ok && old.PublicKey != peer.PublicKey {
		return errors.New("remove a peer before replacing its public key")
	}
	if err = n.bind.setPeer(peer); err != nil {
		return err
	}
	config := fmt.Sprintf("public_key=%s\nreplace_allowed_ips=true\nallowed_ip=%s/32\nendpoint=peer:%s\npersistent_keepalive_interval=20\n", peer.PublicKey, ip, peer.ID)
	if err = n.device.IpcSet(config); err != nil {
		n.bind.removePeer(peer.ID)
		return err
	}
	peer.Endpoints = append([]string(nil), peer.Endpoints...)
	n.peers[peer.ID] = peer
	return nil
}

func (n *Node) RemovePeer(id string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return net.ErrClosed
	}
	peer, ok := n.peers[id]
	if !ok {
		return nil
	}
	if err := n.device.IpcSet("public_key=" + peer.PublicKey + "\nremove=true\n"); err != nil {
		return err
	}
	n.bind.removePeer(id)
	delete(n.peers, id)
	return nil
}

// DialContext accepts numeric enrolled IPv4 addresses only. Resolve stable
// device names through the authenticated control-plane device list first.
func (n *Node) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "udp" && network != "udp4" {
		return nil, net.UnknownNetworkError(network)
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.Is4() {
		return nil, errors.New("mesh dial requires a numeric IPv4 peer address")
	}
	n.mu.RLock()
	allowed, closed := ip == n.address, n.closed
	for _, p := range n.peers {
		if p.Address == host {
			allowed = true
			break
		}
	}
	n.mu.RUnlock()
	if closed {
		return nil, net.ErrClosed
	}
	if !allowed {
		return nil, errors.New("destination is not an enrolled mesh peer")
	}
	return n.net.DialContext(ctx, network, address)
}

func (n *Node) ListenTCP(port uint16) (net.Listener, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.closed {
		return nil, net.ErrClosed
	}
	return n.net.ListenTCPAddrPort(netip.AddrPortFrom(n.address, port))
}

func (n *Node) ListenUDP(port uint16) (net.PacketConn, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.closed {
		return nil, net.ErrClosed
	}
	return n.net.ListenUDPAddrPort(netip.AddrPortFrom(n.address, port))
}

func (n *Node) LocalEndpoints() []string { return n.bind.localEndpoints() }
func (n *Node) Status() []PeerStatus {
	statuses := n.bind.status()
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].ID < statuses[j].ID })
	return statuses
}

// DiscoverSTUN discovers the mapping of the actual WireGuard UDP socket, not
// an unrelated temporary socket. Caller decides whether to publish it.
func (n *Node) DiscoverSTUN(ctx context.Context, server string) (string, error) {
	return n.bind.discoverSTUN(ctx, server)
}

func (n *Node) Close() error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	n.mu.Unlock()
	n.bind.shutdown()
	n.device.Close()
	return nil
}
