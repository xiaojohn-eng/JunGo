// Package engine is the shared stateful runtime used by Android and desktop.
package engine

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/xiaojohn-eng/JunGo/internal/chat"
	"github.com/xiaojohn-eng/JunGo/internal/control"
	"github.com/xiaojohn-eng/JunGo/internal/files"
	"github.com/xiaojohn-eng/JunGo/internal/mesh"
	"github.com/xiaojohn-eng/JunGo/internal/proxycore"
	"github.com/xiaojohn-eng/JunGo/internal/secure"
)

type Platform interface {
	Protect(fd int) bool
	OwnerUID(protocol int, localIP string, localPort int, remoteIP string, remotePort int) int
	OpenURI(uri, mode string) int
	URIInfo(uri string) string
}
type Share struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"readOnly"`
}
type PortMapping struct {
	Port    uint16 `json:"port"`
	Target  string `json:"target"`
	Network string `json:"network"`
}
type Config struct {
	Version      int            `json:"version"`
	PrivateKey   string         `json:"privateKey"`
	Server       string         `json:"server"`
	Fingerprint  string         `json:"fingerprint"`
	ServiceID    string         `json:"serviceId"`
	Token        string         `json:"token"`
	Device       control.Device `json:"device"`
	MeshEnabled  bool           `json:"meshEnabled"`
	ProxyEnabled bool           `json:"proxyEnabled"`
	Mode         string         `json:"mode"`
	Selected     string         `json:"selected"`
	BypassUIDs   []int          `json:"bypassUIDs"`
	ProfileURL   string         `json:"profileURL"`
	Profile      string         `json:"profile"`
	Shares       []Share        `json:"shares"`
	Services     []PortMapping  `json:"services"`
}
type Peer struct {
	control.Device
	Path string `json:"path"`
}
type State struct {
	Paired         bool                 `json:"paired"`
	Device         control.Device       `json:"device"`
	Peers          []Peer               `json:"peers"`
	MeshEnabled    bool                 `json:"meshEnabled"`
	MeshRunning    bool                 `json:"meshRunning"`
	ProxyEnabled   bool                 `json:"proxyEnabled"`
	VPNRunning     bool                 `json:"vpnRunning"`
	Mode           string               `json:"mode"`
	BypassUIDs     []int                `json:"bypassUIDs"`
	Proxies        []proxycore.NodeInfo `json:"proxies"`
	ProfileURL     string               `json:"profileURL"`
	Transfers      []Transfer           `json:"transfers"`
	Shares         []Share              `json:"shares"`
	Services       []PortMapping        `json:"services"`
	Error          string               `json:"error"`
	ActiveIncoming int                  `json:"activeIncoming"`
}
type Engine struct {
	peerClientsMu         sync.Mutex
	peerClients           map[string]*peerHTTPClient
	profileMu             sync.Mutex
	profileMetadataSource string
	profileMetadata       []proxycore.NodeInfo
	op                    sync.Mutex
	mu                    sync.RWMutex
	dir                   string
	platform              Platform
	cfg                   Config
	peers                 map[string]control.Device
	node                  *mesh.Node
	core                  *proxycore.Core
	sessionCancel         context.CancelFunc
	sessionDone           chan struct{}
	fileServer            *http.Server
	fileService           *files.Service
	forwards              []net.Listener
	cert                  tls.Certificate
	certFingerprint       string
	lastError             string
	errorRevision         uint64
	closed                bool
	ctx                   context.Context
	cancel                context.CancelFunc
	tasksMu               sync.Mutex
	tasks                 map[string]*Transfer
	taskCancels           map[string]context.CancelFunc
	workerWake            chan struct{}
	workerDone            chan struct{}
	chatStore             *chat.Store
	chatWake              chan struct{}
	chatDone              chan struct{}
}

func New(dir string, platform Platform) (*Engine, error) {
	abs, e := filepath.Abs(dir)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(abs, 0700); e != nil {
		return nil, e
	}
	if e = os.Chmod(abs, 0700); e != nil {
		return nil, e
	}
	c := Config{Version: 1, Mode: "rule", Shares: []Share{}, BypassUIDs: []int{}}
	b, e := os.ReadFile(filepath.Join(abs, "device.json"))
	if e == nil {
		if e = json.Unmarshal(b, &c); e != nil {
			return nil, e
		}
		if c.Version != 1 {
			return nil, errors.New("unsupported device state")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if c.PrivateKey == "" {
		c.PrivateKey, _, e = mesh.GenerateKey()
		if e != nil {
			return nil, e
		}
	}
	cert, fp, e := secure.Certificate(filepath.Join(abs, "tls"))
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	eng := &Engine{dir: abs, platform: platform, cfg: c, peers: map[string]control.Device{}, cert: cert, certFingerprint: fp, ctx: ctx, cancel: cancel, tasks: map[string]*Transfer{}, taskCancels: map[string]context.CancelFunc{}, workerWake: make(chan struct{}, 1), workerDone: make(chan struct{})}
	if e = eng.saveConfig(c); e != nil {
		cancel()
		return nil, e
	}
	if e = eng.loadTransfers(); e != nil {
		cancel()
		return nil, e
	}
	eng.chatStore, e = chat.New(filepath.Join(abs, "chat", "messages.json"))
	if e != nil {
		cancel()
		return nil, e
	}
	if e = os.MkdirAll(filepath.Join(abs, "chat-inbox"), 0700); e != nil {
		cancel()
		return nil, e
	}
	eng.chatWake, eng.chatDone = make(chan struct{}, 1), make(chan struct{})
	go eng.transferLoop()
	go eng.chatLoop()
	if c.MeshEnabled && c.Token != "" {
		go eng.restoreMesh(10 * time.Second)
	}
	return eng, nil
}

// Restore a persisted enabled mesh even when the controller is restarting.
// This reuses the single initialization goroutine and exits after success or an
// explicit disable/Close. Never hold op while waiting between attempts.
func (e *Engine) restoreMesh(retryDelay time.Duration) {
	var previousError uint64
	for e.ctx.Err() == nil {
		e.op.Lock()
		c := e.config()
		if e.ctx.Err() != nil || !c.MeshEnabled || c.Token == "" {
			e.op.Unlock()
			return
		}
		err := e.startMesh()
		if err == nil {
			e.clearErrorIfCurrent(previousError)
			e.op.Unlock()
			return
		}
		previousError = e.setError(err)
		e.op.Unlock()
		// Invalid/revoked credentials need explicit repair; retrying cannot restore
		// this identity and must not turn revocation into an automatic reconnect loop.
		var apiError *APIError
		if errors.As(err, &apiError) && (apiError.Status == http.StatusUnauthorized || apiError.Status == http.StatusForbidden) {
			return
		}
		timer := time.NewTimer(retryDelay)
		select {
		case <-e.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (e *Engine) saveConfig(c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return secure.WriteFile(filepath.Join(e.dir, "device.json"), b)
}
func (e *Engine) config() Config { e.mu.RLock(); defer e.mu.RUnlock(); return e.cfg }
func (e *Engine) commit(c Config) error {
	if err := e.saveConfig(c); err != nil {
		return err
	}
	e.mu.Lock()
	e.cfg = c
	e.mu.Unlock()
	return nil
}
func (e *Engine) setError(err error) uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.setErrorLocked(err)
}
func (e *Engine) setErrorLocked(err error) uint64 {
	e.errorRevision++
	if err == nil {
		e.lastError = ""
	} else {
		e.lastError = err.Error()
	}
	return e.errorRevision
}

// A recovery may dismiss only the error it actually published. Matching text
// is insufficient: another operation may have reported the same error later.
func (e *Engine) clearErrorIfCurrent(revision uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if revision != 0 && revision == e.errorRevision {
		e.setErrorLocked(nil)
	}
}

// Cache only immutable display metadata. Live routes, authorization, delays and
// transfer progress still come from their current owners on every snapshot.
func (e *Engine) offlineProfileNodes(profile, selected string) []proxycore.NodeInfo {
	e.profileMu.Lock()
	defer e.profileMu.Unlock()
	if profile != e.profileMetadataSource {
		e.profileMetadata = proxycore.ProfileNodes([]byte(profile), "")
		e.profileMetadataSource = profile
	}
	nodes := make([]proxycore.NodeInfo, len(e.profileMetadata))
	for i, node := range e.profileMetadata {
		nodes[i] = node
		nodes[i].Selected = node.Name == selected
		nodes[i].Members = slices.Clone(node.Members)
	}
	return nodes
}

func (e *Engine) State() State {
	e.mu.RLock()
	c := e.cfg
	node, core := e.node, e.core
	s := State{Paired: c.Token != "", Device: c.Device, MeshEnabled: c.MeshEnabled, MeshRunning: node != nil, ProxyEnabled: c.ProxyEnabled, VPNRunning: core != nil, Mode: c.Mode, BypassUIDs: append([]int{}, c.BypassUIDs...), ProfileURL: c.ProfileURL, Error: e.lastError, Shares: append([]Share{}, c.Shares...), Services: append([]PortMapping{}, c.Services...), Peers: []Peer{}, Proxies: []proxycore.NodeInfo{}}
	for _, p := range e.peers {
		s.Peers = append(s.Peers, Peer{Device: p, Path: "unavailable"})
	}
	e.mu.RUnlock()
	if node != nil {
		for _, p := range node.Status() {
			for i := range s.Peers {
				if s.Peers[i].ID == p.ID && time.Since(s.Peers[i].LastSeen) < 45*time.Second {
					s.Peers[i].Path = p.Path
				}
			}
		}
	}
	if core != nil {
		s.Proxies = core.Nodes()
	} else {
		s.Proxies = e.offlineProfileNodes(c.Profile, c.Selected)
	}
	s.Transfers = e.transferSnapshot()
	if e.chatStore != nil {
		s.ActiveIncoming = e.chatStore.ActiveIncoming(time.Now())
	}
	return s
}
func (e *Engine) Close() error {
	e.op.Lock()
	defer e.op.Unlock()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	e.mu.Unlock()
	e.cancel()
	e.stopVPN()
	e.stopMesh()
	e.tasksMu.Lock()
	for _, cancel := range e.taskCancels {
		cancel()
	}
	e.tasksMu.Unlock()
	<-e.workerDone
	<-e.chatDone
	if e.chatStore != nil {
		return e.chatStore.Close()
	}
	return nil
}
func (e *Engine) StartVPN(fd int) error {
	e.op.Lock()
	defer e.op.Unlock()
	e.mu.RLock()
	closed := e.closed
	e.mu.RUnlock()
	if closed {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		return errors.New("客户端已关闭")
	}
	e.stopVPN()
	c := e.config()
	e.mu.RLock()
	node := e.node
	e.mu.RUnlock()
	opts := proxycore.Config{StateDir: filepath.Join(e.dir, "proxy"), Profile: []byte(c.Profile), TUNFD: fd, Mesh: node, Peers: e.peerNames(), ProxyEnabled: c.ProxyEnabled, Mode: c.Mode, Selected: c.Selected, BypassUIDs: c.BypassUIDs}
	if e.platform != nil {
		opts.ProtectSocket = e.platform.Protect
		opts.OwnerUID = e.platform.OwnerUID
	}
	core, err := proxycore.Start(opts)
	if err != nil {
		e.setError(err)
		return err
	}
	e.mu.Lock()
	e.core = core
	e.setErrorLocked(nil)
	e.mu.Unlock()
	return nil
}
func (e *Engine) stopVPN() {
	e.mu.Lock()
	core := e.core
	e.core = nil
	e.mu.Unlock()
	if core != nil {
		_ = core.Close()
	}
}
func (e *Engine) StopVPN() { e.op.Lock(); defer e.op.Unlock(); e.stopVPN() }
func (e *Engine) peerNames() []proxycore.PeerName {
	e.mu.RLock()
	defer e.mu.RUnlock()
	names := []proxycore.PeerName{}
	for _, p := range e.peers {
		names = append(names, proxycore.PeerName{IP: p.IP, Hostname: p.Hostname})
	}
	return names
}
