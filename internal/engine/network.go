package engine

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xiaojohn-eng/JunGo/internal/control"
	"github.com/xiaojohn-eng/JunGo/internal/files"
	"github.com/xiaojohn-eng/JunGo/internal/mesh"
	"github.com/xiaojohn-eng/JunGo/internal/relay"
	"github.com/xiaojohn-eng/JunGo/internal/secure"
)

type relayPacket struct {
	id string
	b  []byte
}
type reconnectRelay struct {
	mu       sync.RWMutex
	client   *relay.Client
	ctx      context.Context
	cancel   context.CancelFunc
	incoming chan relayPacket
}

func newRelay(ctx context.Context, raw, token string, cfg relay.DialConfig) *reconnectRelay {
	ctx, cancel := context.WithCancel(ctx)
	r := &reconnectRelay{ctx: ctx, cancel: cancel, incoming: make(chan relayPacket, 256)}
	go func() {
		delay := time.Second
		for ctx.Err() == nil {
			client, err := relay.DialWithConfig(ctx, raw, token, cfg)
			if err == nil {
				r.mu.Lock()
				r.client = client
				r.mu.Unlock()
				delay = time.Second
				for ctx.Err() == nil {
					id, b, readErr := client.Receive()
					if readErr != nil {
						break
					}
					select {
					case r.incoming <- relayPacket{id, b}:
					case <-ctx.Done():
					default:
					}
				}
				r.mu.Lock()
				if r.client == client {
					r.client = nil
				}
				r.mu.Unlock()
				_ = client.Close()
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			if delay < 15*time.Second {
				delay *= 2
			}
		}
	}()
	return r
}
func (r *reconnectRelay) Send(id string, b []byte) error {
	r.mu.RLock()
	c := r.client
	r.mu.RUnlock()
	if c == nil {
		return errors.New("中继暂未连接")
	}
	err := c.Send(id, b)
	if err != nil {
		// WebSocket write errors are terminal even if its reader remains alive.
		// Closing wakes the receive loop so it reconnects instead of repeatedly
		// sending through the permanently failed writer. Never clear a newer
		// client installed concurrently after this Send took its snapshot.
		r.mu.Lock()
		if r.client == c {
			r.client = nil
		}
		r.mu.Unlock()
		_ = c.Close()
	}
	return err
}

// Receive intentionally stays open across reconnects. Expose the current
// transport state separately so mesh status does not report a dead socket as
// a usable relay while the reconnect worker is backing off.
func (r *reconnectRelay) Connected() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ctx.Err() == nil && r.client != nil
}

func (r *reconnectRelay) Receive() (string, []byte, error) {
	select {
	case <-r.ctx.Done():
		return "", nil, net.ErrClosed
	case p := <-r.incoming:
		return p.id, p.b, nil
	}
}
func (r *reconnectRelay) Close() error {
	r.cancel()
	r.mu.Lock()
	c := r.client
	r.client = nil
	r.mu.Unlock()
	if c != nil {
		return c.Close()
	}
	return nil
}

func (e *Engine) startMesh() error {
	c := e.config()
	if c.Token == "" {
		return errors.New("请先配对控制服务")
	}
	e.mu.RLock()
	existing := e.node
	e.mu.RUnlock()
	if existing != nil {
		return nil
	}
	client, err := e.controlClient(c)
	if err != nil {
		return err
	}
	handedOff := false
	defer func() {
		if !handedOff {
			client.CloseIdleConnections()
		}
	}()
	var self control.Device
	if err = apiDo(e.ctx, client, c.Server, c.Token, "GET", "/v1/device", nil, &self); err != nil {
		return err
	}
	if c, err = e.syncDeviceName(c, self); err != nil {
		return err
	}
	tlsConfig, err := secure.PinnedTLS(c.Fingerprint)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(e.ctx)
	r := newRelay(ctx, "wss"+strings.TrimPrefix(c.Server, "https")+"/v1/relay", c.Token, relay.DialConfig{TLSConfig: tlsConfig, NetDialContext: e.dialer().DialContext})
	cfg := mesh.Config{ID: c.Device.ID, PrivateKey: c.PrivateKey, Address: c.Device.IP, Relay: r}
	if e.platform != nil {
		cfg.ProtectSocket = func(fd int) error {
			if !e.platform.Protect(fd) {
				return errors.New("无法保护WireGuard UDP socket")
			}
			return nil
		}
	}
	node, err := mesh.New(cfg)
	if err != nil {
		cancel()
		r.Close()
		return err
	}
	e.mu.Lock()
	e.node = node
	e.sessionCancel = cancel
	e.sessionDone = make(chan struct{})
	done := e.sessionDone
	e.mu.Unlock()
	{
		if err = e.startFiles(node, c); err != nil {
			close(done)
			e.stopMesh()
			return err
		}
	}
	for _, mapping := range c.Services {
		if err = e.forwardService(ctx, node, mapping); err != nil {
			e.setError(err)
		}
	}
	e.mu.RLock()
	core := e.core
	e.mu.RUnlock()
	if core != nil {
		_ = core.SetMesh(node, e.peerNames())
	}
	handedOff = true
	go e.sessionLoop(ctx, c, node, client, done)
	e.wakeTransfers()
	return nil
}

// syncDeviceName runs under e.op. The controller owns the display name, while
// the local state retains its existing identity, keys, shares and preferences.
func (e *Engine) syncDeviceName(c Config, remote control.Device) (Config, error) {
	if remote.ID == "" || remote.ID != c.Device.ID || remote.Name == "" {
		return c, errors.New("控制服务返回的设备身份或名称无效")
	}
	if c.Device.Name == remote.Name {
		return c, nil
	}
	c.Device.Name = remote.Name
	if err := e.commit(c); err != nil {
		return c, err
	}
	return c, nil
}

func (e *Engine) stopMesh() {
	e.mu.Lock()
	cancel, done, node, server, service := e.sessionCancel, e.sessionDone, e.node, e.fileServer, e.fileService
	listeners := e.forwards
	e.node = nil
	e.sessionCancel = nil
	e.sessionDone = nil
	e.fileServer = nil
	e.fileService = nil
	e.forwards = nil
	e.peers = map[string]control.Device{}
	core := e.core
	e.mu.Unlock()
	e.prunePeerClients()
	if cancel != nil {
		cancel()
	}
	if core != nil {
		_ = core.SetMesh(nil, nil)
	}
	if server != nil {
		_ = server.Close()
	}
	for _, l := range listeners {
		_ = l.Close()
	}
	if node != nil {
		_ = node.Close()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	if service != nil {
		_ = service.Close()
	}
}

// retireSession runs teardown outside the session goroutine: stopMesh waits for
// sessionDone and callers may already hold op while waiting for that goroutine.
// The node guard prevents a delayed retirement from stopping a newer session.
func (e *Engine) retireSession(node *mesh.Node) <-chan struct{} {
	done := make(chan struct{})
	_ = node.Close()
	go func() {
		defer close(done)
		e.op.Lock()
		defer e.op.Unlock()
		e.mu.RLock()
		current := e.node == node
		e.mu.RUnlock()
		if current {
			e.stopMesh()
		}
	}()
	return done
}

func (e *Engine) sessionLoop(ctx context.Context, c Config, node *mesh.Node, client *http.Client, done chan struct{}) {
	defer close(done)
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	refresh := make(chan struct{}, 1)
	signal := func() {
		select {
		case refresh <- struct{}{}:
		default:
		}
	}
	var sessionError uint64
	heartbeatHealthy, peersHealthy, selfHealthy := false, false, true
	reportFailure := func(err error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.node == node && ctx.Err() == nil {
			sessionError = e.setErrorLocked(err)
		}
	}
	recovered := func() {
		if heartbeatHealthy && peersHealthy && selfHealthy {
			e.clearErrorIfCurrent(sessionError)
		}
	}
	go e.eventLoop(ctx, c, signal)
	endpoints := node.LocalEndpoints()
	candidates := make(chan []string, 1)
	go e.refreshCandidates(ctx, c.Server, node, candidates, 10*time.Second)
	heartbeat := func() error {
		fileURL := "https://" + net.JoinHostPort(c.Device.IP, "8443")
		fp := e.certFingerprint
		err := apiDo(ctx, client, c.Server, c.Token, "POST", "/v1/heartbeat", control.Heartbeat{Endpoints: endpoints, FileURL: fileURL, FileTLSFingerprint: fp}, nil)
		heartbeatHealthy = err == nil
		if err != nil {
			reportFailure(err)
		} else {
			recovered()
		}
		return err
	}
	_ = heartbeat()
	signal()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	heart := time.NewTicker(10 * time.Second)
	defer heart.Stop()
	lastSync := time.Now()
	// startMesh already fetched this device from the controller.
	lastSelfSync := lastSync
	expire := func() {
		e.mu.Lock()
		if e.node != node {
			e.mu.Unlock()
			return
		}
		old := e.peers
		e.peers = map[string]control.Device{}
		core := e.core
		e.mu.Unlock()
		e.prunePeerClients()
		for id := range old {
			_ = node.RemovePeer(id)
		}
		if core != nil {
			_ = core.SetMesh(node, nil)
		}
	}
	revoked := func(err error) bool {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized {
			expire()
			e.retireSession(node)
			return true
		}
		return false
	}
	for {
		select {
		case <-ctx.Done():
			return
		case next := <-candidates:
			if !reflect.DeepEqual(endpoints, next) {
				endpoints = next
				if err := heartbeat(); err != nil {
					if revoked(err) {
						return
					}
				}
			}
		case <-heart.C:
			if err := heartbeat(); err != nil {
				if revoked(err) {
					return
				}
			}
		case <-ticker.C:
			signal()
		case <-refresh:
			var response struct {
				Peers []control.Device `json:"peers"`
			}
			err := apiDo(ctx, client, c.Server, c.Token, "GET", "/v1/peers", nil, &response)
			if err != nil {
				peersHealthy = false
				reportFailure(err)
				if revoked(err) {
					return
				}
				if time.Since(lastSync) > 30*time.Second {
					expire()
				}
				continue
			}
			lastSync = time.Now()
			peersHealthy = true
			if time.Since(lastSelfSync) >= 30*time.Second {
				var self control.Device
				selfErr := apiDo(ctx, client, c.Server, c.Token, "GET", "/v1/device", nil, &self)
				synced := false
				if selfErr == nil && e.op.TryLock() {
					e.mu.RLock()
					currentNode := e.node == node
					e.mu.RUnlock()
					if currentNode {
						_, selfErr = e.syncDeviceName(e.config(), self)
						synced = true
						if selfErr == nil {
							lastSelfSync = time.Now()
						}
					}
					e.op.Unlock()
				}
				if selfErr != nil {
					selfHealthy = false
					reportFailure(selfErr)
				} else if synced {
					selfHealthy = true
				}
			}
			next := make(map[string]control.Device, len(response.Peers))
			e.mu.RLock()
			old := e.peers
			e.mu.RUnlock()
			for _, p := range response.Peers {
				if p.RevokedAt != nil {
					continue
				}
				prev, exists := old[p.ID]
				if exists && (prev.PublicKey != p.PublicKey || prev.IP != p.IP) {
					_ = node.RemovePeer(p.ID)
					exists = false
				}
				if !exists || !reflect.DeepEqual(prev.Endpoints, p.Endpoints) {
					if err = node.SetPeer(mesh.Peer{ID: p.ID, PublicKey: p.PublicKey, Address: p.IP, Endpoints: p.Endpoints}); err != nil {
						peersHealthy = false
						reportFailure(err)
						continue
					}
				}
				next[p.ID] = p
			}
			for id := range old {
				if _, ok := next[id]; !ok {
					_ = node.RemovePeer(id)
				}
			}
			e.mu.Lock()
			if e.node != node {
				e.mu.Unlock()
				return
			}
			e.peers = next
			core := e.core
			e.mu.Unlock()
			e.prunePeerClients()
			if core != nil {
				_ = core.SetMesh(node, e.peerNames())
			}
			recovered()
			e.wakeTransfers()
		}
	}
}

type candidateSource interface {
	LocalEndpoints() []string
	DiscoverSTUN(context.Context, string) (string, error)
}

// Discovery runs separately from authorization refreshes so an unavailable STUN
// server cannot delay revocation. Re-read both interfaces and NAT mapping after
// network changes, instead of advertising the endpoints from session startup.
func (e *Engine) refreshCandidates(ctx context.Context, server string, node candidateSource, updates chan<- []string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		endpoints := e.discoverCandidates(ctx, server, node)
		select {
		case updates <- endpoints:
		case <-ctx.Done():
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

func (e *Engine) discoverCandidates(ctx context.Context, server string, node candidateSource) []string {
	endpoints := append([]string(nil), node.LocalEndpoints()...)
	defer func() { slices.Sort(endpoints) }()
	u, err := url.Parse(server)
	if err != nil || u.Hostname() == "" {
		return endpoints
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addresses, err := e.stunAddresses(ctx, u.Hostname())
	if err != nil {
		return endpoints
	}
	// A control-service name may have multiple A records. Probe a bounded set
	// concurrently so one unreachable address does not hide a working address.
	if len(addresses) > 4 {
		addresses = addresses[:4]
	}
	type result struct{ endpoint string }
	results := make(chan result, len(addresses))
	for _, ip := range addresses {
		go func(ip netip.Addr) {
			endpoint, _ := node.DiscoverSTUN(ctx, net.JoinHostPort(ip.String(), "3478"))
			results <- result{endpoint}
		}(ip)
	}
	for range addresses {
		select {
		case r := <-results:
			if r.endpoint != "" && !slices.Contains(endpoints, r.endpoint) {
				endpoints = append(endpoints, r.endpoint)
			}
		case <-ctx.Done():
			return endpoints
		}
	}
	return endpoints
}

type networkDial func(context.Context, string, string) (net.Conn, error)

// The resolver dials a numeric DNS server with the same protection hook as
// control/relay sockets. Android's system DNS can be the VPN's 172.19.0.2;
// explicitly overriding it avoids trying to reach that virtual IP outside TUN.
func protectedResolver(dial networkDial, server string) *net.Resolver {
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dial(ctx, network, server)
	}}
}

func (e *Engine) stunAddresses(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Is4() {
			return []netip.Addr{ip}, nil
		}
		return nil, errors.New("STUN requires an IPv4 endpoint")
	}
	if e.platform == nil {
		return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		ips []netip.Addr
		err error
	}
	results := make(chan result, 2)
	for _, server := range []string{"223.5.5.5:53", "1.1.1.1:53"} {
		go func(server string) {
			ips, err := protectedResolver(e.dialer().DialContext, server).LookupNetIP(ctx, "ip4", host)
			results <- result{ips, err}
		}(server)
	}
	var failures []error
	for range 2 {
		select {
		case r := <-results:
			if r.err == nil && len(r.ips) > 0 {
				return r.ips, nil
			}
			failures = append(failures, r.err)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, errors.Join(failures...)
}

// SSE heartbeat keeps the stream alive but carries no peer invalidation.
// Keep the independent five-second snapshot refresh for missed events/revokes.
func peerEventNeedsRefresh(line string) bool {
	if !strings.HasPrefix(line, "event:") {
		return false
	}
	switch strings.TrimSpace(strings.TrimPrefix(line, "event:")) {
	case "ready", "peers-changed", "revoked":
		return true
	default:
		return false
	}
}

func (e *Engine) eventLoop(ctx context.Context, c Config, signal func()) {
	client, err := e.controlClient(c)
	if err != nil {
		return
	}
	client.Timeout = 0
	defer client.CloseIdleConnections()
	for ctx.Err() == nil {
		req, err := http.NewRequestWithContext(ctx, "GET", c.Server+"/v1/events", nil)
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		response, err := client.Do(req)
		if err == nil {
			if response.StatusCode == http.StatusUnauthorized {
				response.Body.Close()
				signal()
				return
			}
			if response.StatusCode == 200 {
				scan := bufio.NewScanner(response.Body)
				scan.Buffer(make([]byte, 4096), 65536)
				for scan.Scan() {
					if peerEventNeedsRefresh(scan.Text()) {
						signal()
					}
				}
			}
			response.Body.Close()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}
func (e *Engine) startFiles(node *mesh.Node, c Config) error {
	authorizer := &secure.Authorizer{PrivateKey: c.PrivateKey, Lookup: func(id string) (string, bool) {
		e.mu.RLock()
		defer e.mu.RUnlock()
		p, ok := e.peers[id]
		return p.PublicKey, ok && e.node == node && p.RevokedAt == nil
	}}
	shares := make([]files.Share, 0, len(c.Shares))
	for _, s := range c.Shares {
		shares = append(shares, files.Share{ID: s.ID, Name: s.Name, Path: s.Path, ReadOnly: s.ReadOnly})
	}
	shares = append(shares, files.Share{ID: chatInboxShare, Name: "聊天收件箱", Path: e.dir + "/chat-inbox"})
	service, err := files.New(files.Config{StateDir: e.dir + "/files", Shares: shares, Authorize: authorizer.Authorize})
	if err != nil {
		return err
	}
	listener, err := node.ListenTCP(8443)
	if err != nil {
		service.Close()
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/messages", e.chatHandler(authorizer))
	mux.Handle("/", service.Handler())
	server := &http.Server{Handler: authorizer.Middleware(mux), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{e.cert}}}
	e.mu.Lock()
	e.fileServer = server
	e.fileService = service
	e.mu.Unlock()
	go func() {
		err := server.ServeTLS(listener, "", "")
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			e.setError(err)
		}
	}()
	return nil
}
func (e *Engine) forwardService(ctx context.Context, node *mesh.Node, m PortMapping) error {
	if m.Port == 0 || m.Port == 8443 {
		return errors.New("无效的服务端口")
	}
	host, _, err := net.SplitHostPort(m.Target)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		return errors.New("服务只能转发到明确的本机loopback地址")
	}
	if m.Network != "tcp" {
		return errors.New("本机端口映射当前支持TCP")
	}
	l, err := node.ListenTCP(m.Port)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.forwards = append(e.forwards, l)
	e.mu.Unlock()
	go func() {
		for {
			in, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer in.Close()
				out, err := e.dialer().DialContext(ctx, "tcp", m.Target)
				if err != nil {
					return
				}
				defer out.Close()
				done := make(chan struct{})
				go func() { _, _ = io.Copy(out, in); close(done) }()
				_, _ = io.Copy(in, out)
				out.Close()
				in.Close()
				<-done
			}()
		}
	}()
	return nil
}

// A pool is scoped to one live mesh and both device identities. Reusing TLS
// connections avoids a new WireGuard TCP stream and handshake for each 4 MiB
// chunk. Every HTTP request still gets a fresh signed nonce and authorization.
type peerHTTPClient struct {
	client                                       *http.Client
	node                                         *mesh.Node
	ip, publicKey, fingerprint, privateKey, self string
	signatureKey                                 []byte
}

func (p *peerHTTPClient) matches(node *mesh.Node, peer control.Device, c Config) bool {
	return node != nil && peer.RevokedAt == nil && p.node == node && p.ip == peer.IP && p.publicKey == peer.PublicKey && p.fingerprint == peer.FileTLSFingerprint && p.privateKey == c.PrivateKey && p.self == c.Device.ID
}

func (e *Engine) prunePeerClients() {
	e.mu.RLock()
	e.peerClientsMu.Lock()
	var retired []*http.Client
	for id, cached := range e.peerClients {
		peer, ok := e.peers[id]
		if !ok || !cached.matches(e.node, peer, e.cfg) {
			retired = append(retired, cached.client)
			delete(e.peerClients, id)
		}
	}
	e.peerClientsMu.Unlock()
	e.mu.RUnlock()
	for _, client := range retired {
		client.CloseIdleConnections()
	}
}

func (e *Engine) peerClient(id string) (*http.Client, string, []byte, string, error) {
	// Keep registry and pool lock ordering identical to prunePeerClients. Do not
	// establish network connections while either lock is held.
	e.mu.RLock()
	e.peerClientsMu.Lock()
	var retired *http.Client
	defer func() {
		e.peerClientsMu.Unlock()
		e.mu.RUnlock()
		if retired != nil {
			retired.CloseIdleConnections()
		}
	}()
	p, ok := e.peers[id]
	node, c := e.node, e.cfg
	if !ok || node == nil || p.RevokedAt != nil {
		return nil, "", nil, "", errors.New("设备不在线或组网未开启")
	}
	if p.FileTLSFingerprint == "" {
		return nil, "", nil, "", errors.New("设备未开启文件服务")
	}
	if cached := e.peerClients[id]; cached != nil {
		if cached.matches(node, p, c) {
			return cached.client, "https://" + net.JoinHostPort(p.IP, "8443"), slices.Clone(cached.signatureKey), c.Device.ID, nil
		}
		retired = cached.client
		delete(e.peerClients, id)
	}
	cfg, err := secure.PinnedTLS(p.FileTLSFingerprint)
	if err != nil {
		return nil, "", nil, "", err
	}
	key, err := secure.PairKey(c.PrivateKey, p.PublicKey)
	if err != nil {
		return nil, "", nil, "", err
	}
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		conn, err := node.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &idleFileConn{Conn: conn}, nil
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DialContext: dial, ResponseHeaderTimeout: 10 * time.Minute, TLSHandshakeTimeout: 15 * time.Second, MaxIdleConns: 4, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("文件服务不允许重定向") }}
	if e.peerClients == nil {
		e.peerClients = make(map[string]*peerHTTPClient)
	}
	e.peerClients[id] = &peerHTTPClient{client: client, node: node, ip: p.IP, publicKey: p.PublicKey, fingerprint: p.FileTLSFingerprint, privateKey: c.PrivateKey, self: c.Device.ID, signatureKey: slices.Clone(key)}
	return client, "https://" + net.JoinHostPort(p.IP, "8443"), key, c.Device.ID, nil
}

func (e *Engine) String() string { return fmt.Sprintf("JunGo(%s)", e.dir) }

// A lost TCP stream must return to the persisted queue instead of retaining a
// worker forever. This is an inactivity limit, not a whole-file time limit.
type idleFileConn struct{ net.Conn }

func (c *idleFileConn) Read(b []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
	return c.Conn.Read(b)
}
func (c *idleFileConn) Write(b []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(2 * time.Minute))
	return c.Conn.Write(b)
}
