// Package proxycore embeds a single process-wide mihomo core. Android supplies
// its one VpnService TUN; WireGuard mesh is an outbound userspace network.
package proxycore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/xiaojohn-eng/JunGo/internal/mesh"
	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/component/process"
	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/listener/mixed"
	"github.com/metacubex/mihomo/tunnel"
	"github.com/metacubex/mihomo/tunnel/statistic"
)

type PeerName struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
}
type NodeInfo struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Selected bool     `json:"selected"`
	Delay    int      `json:"delay"`
	Members  []string `json:"members,omitempty"`
}
type Config struct {
	StateDir      string
	Profile       []byte
	TUNFD         int // transferred ownership; -1 for no TUN
	Mesh          *mesh.Node
	Peers         []PeerName
	ProxyEnabled  bool
	Mode          string
	Selected      string
	BypassUIDs    []int
	ProtectSocket func(int) bool
	OwnerUID      func(protocol int, localIP string, localPort int, remoteIP string, remotePort int) int
	// MixedAddress is optional and restricted to numeric loopback. Empty disables
	// local SOCKS/HTTP; tests may use 127.0.0.1:0. Imported profiles cannot set it.
	MixedAddress string
}
type policy struct {
	enabled        bool
	mode, selected string
	bypass         map[uint32]bool
}
type meshState struct {
	node  *mesh.Node
	names map[string]netip.Addr
	ips   map[netip.Addr]bool
}
type Core struct {
	mu      sync.Mutex
	cfg     Config
	parsed  *config.Config
	current atomic.Pointer[config.Config]
	policy  atomic.Pointer[policy]
	mesh    atomic.Pointer[meshState]
	closed  atomic.Bool
	tun     io.Closer
	mixed   *mixed.Listener
	entry   *entryTunnel
}

var globalMu sync.Mutex
var active *Core
var hookCore atomic.Pointer[Core]
var hookOnce sync.Once
var privateRange = netip.MustParsePrefix("100.96.0.0/16")

func Start(cfg Config) (result *Core, err error) {
	fd := cfg.TUNFD
	defer func() {
		if fd >= 0 {
			syscall.Close(fd)
		}
	}()
	globalMu.Lock()
	defer globalMu.Unlock()
	if active != nil {
		return nil, errors.New("one mihomo core is already running")
	}
	if fd == 0 {
		return nil, errors.New("TUN FD 0 is not supported; use a detached descriptor or -1")
	}
	if fd >= 0 && cfg.ProtectSocket == nil {
		return nil, errors.New("a VPN TUN requires the VpnService.protect socket callback")
	}
	if cfg.StateDir == "" {
		return nil, errors.New("StateDir is required")
	}
	cfg.StateDir, err = filepath.Abs(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Join(cfg.StateDir, "providers"), 0700); err != nil {
		return nil, err
	}
	data, err := sanitizeProfile(cfg.Profile, cfg.StateDir)
	if err != nil {
		return nil, err
	}
	c := &Core{cfg: cfg}
	if err = c.setMesh(cfg.Mesh, cfg.Peers); err != nil {
		return nil, err
	}
	C.SetHomeDir(cfg.StateDir)
	installHooks()
	hookCore.Store(c)
	defer func() {
		if err != nil {
			hookCore.Store(nil)
		}
	}()
	c.parsed, err = config.Parse(data)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			closeProviders(c.parsed)
		}
	}()
	c.parsed.Proxies[meshName] = adapter.NewProxy(newMeshAdapter(c))
	c.parsed.Proxies[selectedName] = adapter.NewProxy(newSelectedAdapter(c))
	c.current.Store(c.parsed)
	// Route all public modes through immutable rules; changing policy only
	// swaps an atomic snapshot and never recreates the Android TUN.
	if err = c.setPolicy(cfg.ProxyEnabled, cfg.Mode, cfg.Selected, cfg.BypassUIDs); err != nil {
		return nil, err
	}
	rules := []C.Rule{&privateRule{}, &bypassRule{core: c}, &publicPolicyRule{core: c}}
	rules = append(rules, c.parsed.Rules...)
	rules = append(rules, &fallbackRule{core: c})
	c.parsed.Rules = rules
	for name, p := range c.parsed.Proxies {
		if _, ok := p.Adapter().(interface{ GetProxies(bool) []C.Proxy }); ok && name != "GLOBAL" {
			c.parsed.Proxies[name] = &selectedGroup{Proxy: p, core: c}
		}
	}
	c.parsed.General.Mode = tunnel.Rule
	c.parsed.General.Tun.Enable = false
	executor.ApplyConfig(c.parsed, true)
	baseService := resolver.DefaultService
	resolver.DefaultService = &meshDNSService{core: c, fallback: baseService}
	baseResolver := resolver.DefaultResolver
	resolver.DefaultResolver = &meshResolver{Resolver: baseResolver, core: c}
	c.entry = &entryTunnel{core: c}
	defer func() {
		if err != nil {
			c.stopRuntime()
		}
	}()
	if cfg.MixedAddress != "" {
		ap, e := netip.ParseAddrPort(cfg.MixedAddress)
		if e != nil || !ap.Addr().IsLoopback() {
			return nil, errors.New("MixedAddress must be numeric loopback address:port")
		}
		c.mixed, err = mixed.New(cfg.MixedAddress, c.entry)
		if err != nil {
			return nil, err
		}
	}
	if fd >= 0 {
		// startTUN always consumes fd, including errors.
		owned := fd
		fd = -1
		c.tun, err = startTUN(owned, c.entry)
		if err != nil {
			return nil, err
		}
	}
	active = c
	return c, nil
}

func installHooks() {
	hookOnce.Do(func() {
		dialer.DefaultSocketHook = func(network, address string, raw syscall.RawConn) error {
			c := hookCore.Load()
			if c == nil || c.closed.Load() {
				return net.ErrClosed
			}
			if c.cfg.ProtectSocket == nil {
				return nil
			}
			ok := false
			if err := raw.Control(func(fd uintptr) { ok = c.cfg.ProtectSocket(int(fd)) }); err != nil {
				return err
			}
			if !ok {
				return errors.New("VpnService.protect rejected outbound socket")
			}
			return nil
		}
		process.DefaultPackageNameResolver = func(metadata *C.Metadata) (string, error) {
			c := hookCore.Load()
			if c == nil || c.closed.Load() || c.cfg.OwnerUID == nil {
				return "", process.ErrNotFound
			}
			protocol := 6
			if metadata.NetWork == C.UDP {
				protocol = 17
			}
			uid := c.cfg.OwnerUID(protocol, metadata.SrcIP.String(), int(metadata.SrcPort), metadata.DstIP.String(), int(metadata.DstPort))
			if uid < 0 {
				return "", process.ErrNotFound
			}
			metadata.Uid = uint32(uid)
			return strconv.Itoa(uid), nil
		}
	})
}

func (c *Core) SetPolicy(enabled bool, mode, selected string, bypass []int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() {
		return net.ErrClosed
	}
	return c.setPolicy(enabled, mode, selected, bypass)
}

func (c *Core) Selected() string {
	if p := c.policy.Load(); p != nil {
		return p.selected
	}
	return ""
}
func (c *Core) setPolicy(enabled bool, mode, selected string, bypass []int) error {
	if len(bypass) > 0 && c.cfg.OwnerUID == nil {
		return errors.New("application bypass requires the connection owner UID callback")
	}
	mode = strings.ToLower(mode)
	if mode == "" {
		mode = "rule"
	}
	if mode != "rule" && mode != "global" && mode != "direct" {
		return errors.New("mode must be rule, global, or direct")
	}
	if selected == "" {
		selected = c.defaultSelected()
	}
	if selected == meshName || selected == selectedName || selected == "GLOBAL" {
		return errors.New("reserved proxy cannot be selected")
	}
	if _, ok := c.allProxies()[selected]; !ok {
		return fmt.Errorf("proxy %q does not exist", selected)
	}
	p := &policy{enabled: enabled, mode: mode, selected: selected, bypass: make(map[uint32]bool)}
	for _, uid := range bypass {
		if uid <= 0 {
			return errors.New("bypass UIDs must be positive")
		}
		p.bypass[uint32(uid)] = true
	}
	c.policy.Store(p)
	return nil
}
func (c *Core) defaultSelected() string {
	if p := c.policy.Load(); p != nil {
		return p.selected
	}
	var groups, nodes []string
	for name, p := range c.parsed.Proxies {
		if isReserved(name) {
			continue
		}
		if _, ok := p.Adapter().(interface{ GetProxies(bool) []C.Proxy }); ok {
			groups = append(groups, name)
		} else {
			nodes = append(nodes, name)
		}
	}
	sort.Strings(groups)
	sort.Strings(nodes)
	if len(groups) > 0 {
		return groups[0]
	}
	if len(nodes) > 0 {
		return nodes[0]
	}
	return "DIRECT"
}
func isReserved(name string) bool {
	switch name {
	case meshName, selectedName, "GLOBAL", "DIRECT", "REJECT", "REJECT-DROP", "PASS", "PASS-RULE", "COMPATIBLE":
		return true
	}
	return false
}

func (c *Core) SetMesh(node *mesh.Node, peers []PeerName) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() {
		return net.ErrClosed
	}
	old := c.mesh.Load()
	if err := c.setMesh(node, peers); err != nil {
		return err
	}
	next := c.mesh.Load()
	statistic.DefaultManager.Range(func(t statistic.Tracker) bool {
		info := t.Info()
		m := info.Metadata
		if m == nil {
			return true
		}
		isMesh := privateHost(m.Host) || privateRange.Contains(m.DstIP)
		if isMesh && (old == nil || old.node != next.node || next.node == nil || !next.ips[m.DstIP]) {
			t.Close()
		}
		return true
	})
	return nil
}
func (c *Core) setMesh(node *mesh.Node, peers []PeerName) error {
	state := &meshState{node: node, names: make(map[string]netip.Addr), ips: make(map[netip.Addr]bool)}
	for _, peer := range peers {
		ip, err := netip.ParseAddr(peer.IP)
		if err != nil || !privateRange.Contains(ip) {
			return errors.New("peer IP must be inside 100.96.0.0/16")
		}
		name := strings.ToLower(strings.TrimSuffix(peer.Hostname, "."))
		if name != "" {
			if !strings.HasSuffix(name, ".jungo.internal") {
				name += ".jungo.internal"
			}
			if strings.ContainsAny(name, " /\\:\t\r\n") || name == ".jungo.internal" {
				return errors.New("invalid peer hostname")
			}
			if old, ok := state.names[name]; ok && old != ip {
				return errors.New("duplicate peer hostname")
			}
			state.names[name] = ip
		}
		state.ips[ip] = true
	}
	c.mesh.Store(state)
	return nil
}
func (c *Core) allProxies() map[string]C.Proxy {
	cfg := c.current.Load()
	if cfg == nil {
		cfg = c.parsed
	}
	return configProxies(cfg)
}
func configProxies(cfg *config.Config) map[string]C.Proxy {
	out := make(map[string]C.Proxy)
	for name, p := range cfg.Proxies {
		out[name] = p
	}
	for _, provider := range cfg.Providers {
		for _, p := range provider.Proxies() {
			if _, ok := out[p.Name()]; !ok {
				out[p.Name()] = p
			}
		}
	}
	return out
}
func (c *Core) Nodes() []NodeInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() {
		return nil
	}
	selected := c.policy.Load().selected
	var out []NodeInfo
	for name, p := range c.allProxies() {
		if isReserved(name) {
			continue
		}
		delay := -1
		history := p.DelayHistory()
		if len(history) > 0 {
			delay = int(history[len(history)-1].Delay)
		}
		info := NodeInfo{Name: name, Type: p.Type().String(), Selected: name == selected, Delay: delay}
		if group, ok := p.Adapter().(interface{ GetProxies(bool) []C.Proxy }); ok {
			for _, member := range group.GetProxies(false) {
				if !isReserved(member.Name()) {
					info.Members = append(info.Members, member.Name())
				}
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (c *Core) TestNode(ctx context.Context, name string) (int, error) {
	c.mu.Lock()
	if c.closed.Load() {
		c.mu.Unlock()
		return 0, net.ErrClosed
	}
	p, ok := c.allProxies()[name]
	c.mu.Unlock()
	if !ok || isReserved(name) {
		return 0, errors.New("unknown proxy node")
	}
	delay, err := p.URLTest(ctx, "https://www.gstatic.com/generate_204", nil)
	return int(delay), err
}
func (c *Core) MixedAddress() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mixed == nil {
		return ""
	}
	return c.mixed.Address()
}

func (c *Core) Close() error {
	globalMu.Lock()
	defer globalMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Swap(true) {
		return nil
	}
	c.stopRuntime()
	closeProviders(c.parsed)
	if active == c {
		active = nil
	}
	if hookCore.Load() == c {
		hookCore.Store(nil)
	}
	return nil
}
func (c *Core) stopRuntime() {
	if c.mixed != nil {
		c.mixed.Close()
		c.mixed = nil
	}
	if c.tun != nil {
		c.tun.Close()
		c.tun = nil
	}
	statistic.DefaultManager.Range(func(t statistic.Tracker) bool { t.Close(); return true })
	executor.Shutdown()
}
func closeProviders(cfg *config.Config) {
	if cfg == nil {
		return
	}
	for _, p := range cfg.Providers {
		if closer, ok := p.(io.Closer); ok {
			closer.Close()
		}
	}
	for _, p := range cfg.RuleProviders {
		if closer, ok := p.(io.Closer); ok {
			closer.Close()
		}
	}
}

// LoadProfile replaces outbounds, providers and rules without closing the TUN,
// loopback listener, mesh node or existing mesh streams. DNS transport settings
// are applied on the next Start; private device DNS updates through SetMesh.
func (c *Core) LoadProfile(profile []byte) (err error) {
	return c.LoadProfileWithCommit(profile, nil)
}

// LoadProfileWithCommit calls commit with the resolved selection after parsing
// and provider initialization succeed, but before replacing the live policy.
// A failed durable write therefore leaves the old running profile untouched.
// The callback must not call back into Core while its configuration lock is held.
func (c *Core) LoadProfileWithCommit(profile []byte, commit func(selected string) error) (err error) {
	globalMu.Lock()
	defer globalMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() {
		return net.ErrClosed
	}
	data, err := sanitizeProfile(profile, c.cfg.StateDir)
	if err != nil {
		return err
	}
	next, err := config.Parse(data)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			closeProviders(next)
		}
	}()
	next.Proxies[meshName] = adapter.NewProxy(newMeshAdapter(c))
	next.Proxies[selectedName] = adapter.NewProxy(newSelectedAdapter(c))
	for _, provider := range next.Providers {
		if err = provider.Initial(); err != nil {
			return fmt.Errorf("initialize proxy provider %s: %w", provider.Name(), err)
		}
	}
	for _, provider := range next.RuleProviders {
		if err = provider.Initial(); err != nil {
			return fmt.Errorf("initialize rule provider %s: %w", provider.Name(), err)
		}
	}
	for name, p := range next.Proxies {
		if _, ok := p.Adapter().(interface{ GetProxies(bool) []C.Proxy }); ok && name != "GLOBAL" {
			next.Proxies[name] = &selectedGroup{Proxy: p, core: c}
		}
	}
	rules := []C.Rule{&privateRule{}, &bypassRule{core: c}, &publicPolicyRule{core: c}}
	rules = append(rules, next.Rules...)
	rules = append(rules, &fallbackRule{core: c})
	next.Rules = rules
	p := c.policy.Load()
	nextPolicy := *p
	proxies := configProxies(next)
	if _, ok := proxies[p.selected]; !ok {
		nextPolicy.selected = "DIRECT"
		var choices []string
		for name := range proxies {
			if !isReserved(name) {
				choices = append(choices, name)
			}
		}
		sort.Strings(choices)
		if len(choices) > 0 {
			nextPolicy.selected = choices[0]
		}
	}
	if commit != nil {
		if err = commit(nextPolicy.selected); err != nil {
			return err
		}
	}
	old := c.parsed
	c.parsed = next
	c.current.Store(next)
	c.policy.Store(&nextPolicy)
	tunnel.UpdateProxies(next.Proxies, next.Providers)
	tunnel.UpdateRules(next.Rules, next.SubRules, next.RuleProviders)
	c.cfg.Profile = append([]byte(nil), profile...)
	committed = true
	closeProviders(old)
	return nil
}
