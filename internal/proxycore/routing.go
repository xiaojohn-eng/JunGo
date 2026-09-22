package proxycore

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/common/utils"
	"github.com/metacubex/mihomo/component/process"
	"github.com/metacubex/mihomo/component/resolver"
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
	RC "github.com/metacubex/mihomo/rules/common"
	"github.com/metacubex/mihomo/tunnel"
)

const selectedName = "JUNGO-PUBLIC"

func privateHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "jungo.internal" || strings.HasSuffix(host, ".jungo.internal")
}

type privateRule struct{ RC.Base }

func (*privateRule) RuleType() C.RuleType { return C.IPCIDR }
func (*privateRule) Adapter() string      { return meshName }
func (*privateRule) Payload() string      { return "100.96.0.0/16 + jungo.internal" }
func (*privateRule) Match(m *C.Metadata, h C.RuleMatchHelper) (bool, string) {
	if privateHost(m.Host) || privateRange.Contains(m.DstIP) {
		return true, meshName
	}
	if m.Host != "" && !m.DstIP.IsValid() && h.ResolveIP != nil {
		h.ResolveIP()
	}
	return privateRange.Contains(m.DstIP), meshName
}

type bypassRule struct {
	RC.Base
	core *Core
}

func (*bypassRule) RuleType() C.RuleType { return C.Uid }
func (*bypassRule) Adapter() string      { return "DIRECT" }
func (*bypassRule) Payload() string      { return "app-selected UIDs" }
func (r *bypassRule) Match(m *C.Metadata, _ C.RuleMatchHelper) (bool, string) {
	return r.core.policy.Load().bypass[m.Uid], "DIRECT"
}

type publicPolicyRule struct {
	RC.Base
	core *Core
}

func (*publicPolicyRule) RuleType() C.RuleType { return C.MATCH }
func (*publicPolicyRule) Adapter() string      { return selectedName }
func (*publicPolicyRule) Payload() string      { return "public traffic policy" }
func (r *publicPolicyRule) Match(_ *C.Metadata, _ C.RuleMatchHelper) (bool, string) {
	p := r.core.policy.Load()
	if !p.enabled || p.mode == "direct" {
		return true, "DIRECT"
	}
	if p.mode == "global" {
		return true, selectedName
	}
	return false, ""
}

type fallbackRule struct {
	RC.Base
	core *Core
}

func (*fallbackRule) RuleType() C.RuleType { return C.MATCH }
func (*fallbackRule) Adapter() string      { return selectedName }
func (*fallbackRule) Payload() string      { return "public fallback" }
func (*fallbackRule) Match(_ *C.Metadata, _ C.RuleMatchHelper) (bool, string) {
	return true, selectedName
}

type entryTunnel struct{ core *Core }

func (e *entryTunnel) prepare(m *C.Metadata) {
	if e.core.cfg.OwnerUID != nil {
		_, _ = process.FindPackageName(m)
	}
	if privateHost(m.Host) || privateRange.Contains(m.DstIP) {
		m.SpecialProxy = meshName
		if ip, ok := e.core.mesh.Load().names[strings.ToLower(strings.TrimSuffix(m.Host, "."))]; ok {
			m.DstIP = ip
		}
	}
}
func (e *entryTunnel) HandleTCPConn(conn net.Conn, m *C.Metadata) {
	if e.core.closed.Load() {
		conn.Close()
		return
	}
	e.prepare(m)
	tunnel.Tunnel.HandleTCPConn(conn, m)
}
func (e *entryTunnel) HandleUDPPacket(packet C.UDPPacket, m *C.Metadata) {
	if e.core.closed.Load() {
		packet.Drop()
		return
	}
	e.prepare(m)
	tunnel.Tunnel.HandleUDPPacket(packet, m)
}
func (*entryTunnel) NatTable() C.NatTable                     { return tunnel.NatTable() }
func (*entryTunnel) Providers() map[string]P.ProxyProvider    { return tunnel.Providers() }
func (*entryTunnel) RuleProviders() map[string]P.RuleProvider { return tunnel.RuleProviders() }
func (*entryTunnel) RuleUpdateCallback() *utils.Callback[P.RuleProvider] {
	return tunnel.Tunnel.RuleUpdateCallback()
}

type meshAdapter struct {
	*outbound.Base
	core *Core
}

func newMeshAdapter(c *Core) *meshAdapter {
	return &meshAdapter{Base: outbound.NewBase(outbound.BaseOption{Name: meshName, Type: C.WireGuard, UDP: true}), core: c}
}
func (a *meshAdapter) resolve(m *C.Metadata) (*meshState, error) {
	s := a.core.mesh.Load()
	if a.core.closed.Load() || s == nil || s.node == nil {
		return nil, fmt.Errorf("mesh is disabled: %w", resolver.ErrIPNotFound)
	}
	if privateHost(m.Host) {
		ip, ok := s.names[strings.ToLower(strings.TrimSuffix(m.Host, "."))]
		if !ok {
			return nil, fmt.Errorf("unknown private device: %w", resolver.ErrIPNotFound)
		}
		m.DstIP = ip
	}
	if !s.ips[m.DstIP] {
		return nil, fmt.Errorf("destination is not an enrolled mesh device: %w", resolver.ErrIPNotFound)
	}
	return s, nil
}
func (a *meshAdapter) DialContext(ctx context.Context, m *C.Metadata) (C.Conn, error) {
	s, err := a.resolve(m)
	if err != nil {
		return nil, err
	}
	c, err := s.node.DialContext(ctx, "tcp", netip.AddrPortFrom(m.DstIP, m.DstPort).String())
	if err != nil {
		return nil, err
	}
	return outbound.NewConn(c, a), nil
}
func (a *meshAdapter) ListenPacketContext(ctx context.Context, m *C.Metadata) (C.PacketConn, error) {
	s, err := a.resolve(m)
	if err != nil {
		return nil, err
	}
	pc, err := s.node.ListenUDP(0)
	if err != nil {
		return nil, err
	}
	return outbound.NewPacketConn(&guardedPacketConn{PacketConn: pc, adapter: a, state: s}, a), nil
}
func (a *meshAdapter) ResolveUDP(ctx context.Context, m *C.Metadata) error {
	_, err := a.resolve(m)
	return err
}
func (*meshAdapter) IsL3Protocol(*C.Metadata) bool { return true }

type guardedPacketConn struct {
	net.PacketConn
	adapter *meshAdapter
	state   *meshState
}

func (p *guardedPacketConn) WriteTo(data []byte, address net.Addr) (int, error) {
	ap, err := netip.ParseAddrPort(address.String())
	if err != nil {
		return 0, err
	}
	s := p.adapter.core.mesh.Load()
	if p.adapter.core.closed.Load() || s.node != p.state.node || !s.ips[ap.Addr()] {
		return 0, fmt.Errorf("mesh peer unavailable: %w", resolver.ErrIPNotFound)
	}
	return p.PacketConn.WriteTo(data, net.UDPAddrFromAddrPort(ap))
}

type selectedAdapter struct {
	*outbound.Base
	core *Core
}

func newSelectedAdapter(c *Core) *selectedAdapter {
	return &selectedAdapter{Base: outbound.NewBase(outbound.BaseOption{Name: selectedName, Type: C.Selector, UDP: true}), core: c}
}
func (a *selectedAdapter) selected() (C.Proxy, error) {
	p, ok := a.core.allProxies()[a.core.policy.Load().selected]
	if !ok {
		return nil, fmt.Errorf("selected proxy disappeared: %w", resolver.ErrIPNotFound)
	}
	return p, nil
}
func (a *selectedAdapter) DialContext(ctx context.Context, m *C.Metadata) (C.Conn, error) {
	p, err := a.selected()
	if err != nil {
		return nil, err
	}
	return p.DialContext(ctx, m)
}
func (a *selectedAdapter) ListenPacketContext(ctx context.Context, m *C.Metadata) (C.PacketConn, error) {
	p, err := a.selected()
	if err != nil {
		return nil, err
	}
	return p.ListenPacketContext(ctx, m)
}
func (a *selectedAdapter) Unwrap(m *C.Metadata, touch bool) C.Proxy { p, _ := a.selected(); return p }

// Selected groups keep mihomo's original automatic behavior unless the user's
// chosen node is a member. No unsynchronized Selector.Set mutation is needed.
type selectedGroup struct {
	C.Proxy
	core *Core
}

func (g *selectedGroup) override() C.Proxy {
	name := g.core.policy.Load().selected
	group, ok := g.Proxy.Adapter().(interface{ GetProxies(bool) []C.Proxy })
	if !ok {
		return nil
	}
	for _, p := range group.GetProxies(false) {
		if p.Name() == name && p.Name() != g.Name() {
			return p
		}
	}
	return nil
}
func (g *selectedGroup) DialContext(ctx context.Context, m *C.Metadata) (C.Conn, error) {
	if p := g.override(); p != nil {
		return p.DialContext(ctx, m)
	}
	return g.Proxy.DialContext(ctx, m)
}
func (g *selectedGroup) ListenPacketContext(ctx context.Context, m *C.Metadata) (C.PacketConn, error) {
	if p := g.override(); p != nil {
		return p.ListenPacketContext(ctx, m)
	}
	return g.Proxy.ListenPacketContext(ctx, m)
}
func (g *selectedGroup) Unwrap(m *C.Metadata, touch bool) C.Proxy {
	if p := g.override(); p != nil {
		return p
	}
	return g.Proxy.Unwrap(m, touch)
}
