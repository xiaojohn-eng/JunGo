// Package control implements the self-hosted device registry. Device private keys
// never enter this package: enrollment accepts only a WireGuard public key.
package control

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrUnauthorized = errors.New("unauthorized or revoked device")
	ErrPairing      = errors.New("invalid, expired, or already used pairing code")
	ErrInvalid      = errors.New("invalid request")
	ErrNotFound     = errors.New("device not found")
)

type Device struct {
	ID                 string     `json:"id"`
	MeshID             string     `json:"mesh_id"`
	Name               string     `json:"name"`
	Hostname           string     `json:"hostname"`
	IP                 string     `json:"ip"`
	Address            string     `json:"address"`
	PublicKey          string     `json:"public_key"`
	Endpoints          []string   `json:"endpoints,omitempty"`
	FileURL            string     `json:"file_url,omitempty"`
	FileTLSFingerprint string     `json:"file_tls_fingerprint,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	LastSeen           time.Time  `json:"last_seen"`
	RevokedAt          *time.Time `json:"revoked_at,omitempty"`
}

type PairingCode struct {
	ServiceID string    `json:"service_id"`
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Enrollment struct {
	ServiceID          string `json:"service_id"`
	Code               string `json:"code"`
	Name               string `json:"name"`
	PublicKey          string `json:"public_key"`
	FileTLSFingerprint string `json:"file_tls_fingerprint,omitempty"`
}

type EnrollmentResult struct {
	ServiceID string `json:"service_id"`
	Token     string `json:"token"`
	Device    Device `json:"device"`
}

type Heartbeat struct {
	Endpoints          []string `json:"endpoints"`
	FileURL            string   `json:"file_url"`
	FileTLSFingerprint string   `json:"file_tls_fingerprint,omitempty"`
}

// Event is an invalidation hint: consumers must fetch a fresh peer snapshot.
// Notifications can be coalesced when a subscriber is slow.
type Event struct {
	Type     string `json:"type"`
	DeviceID string `json:"device_id"`
}

type persistedDevice struct {
	Device    Device `json:"device"`
	TokenHash string `json:"token_hash"`
}

type diskState struct {
	Version     int                        `json:"version"`
	ServiceID   string                     `json:"service_id"`
	NextAddress uint32                     `json:"next_address"`
	Devices     map[string]persistedDevice `json:"devices"`
	Pairings    map[string]time.Time       `json:"pairings"`
}

// Store serializes durable identity and topology changes with mode 0600. LastSeen
// is volatile liveness state; unchanged heartbeats do not require a disk write. Exactly
// one process may own a store file. An empty path creates an in-memory store.
type Store struct {
	mu           sync.RWMutex
	path         string
	state        diskState
	tokenDevices map[[sha256.Size]byte]string
	listeners    map[uint64]func(string)
	events       map[uint64]chan Event
	nextListener uint64
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path, listeners: make(map[uint64]func(string)), events: make(map[uint64]chan Event)}
	if path != "" {
		b, err := os.ReadFile(path)
		if err == nil {
			if err = json.Unmarshal(b, &s.state); err != nil {
				return nil, fmt.Errorf("read control state: %w", err)
			}
			if s.state.Version != 1 || s.state.ServiceID == "" || s.state.Devices == nil || s.state.Pairings == nil || s.state.NextAddress < 2 || s.state.NextAddress > 65535 {
				return nil, errors.New("unsupported or invalid control state")
			}
			if err = os.Chmod(path, 0600); err != nil {
				return nil, err
			}
			s.reindexTokens()
			return s, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	id, err := randomString(16)
	if err != nil {
		return nil, err
	}
	s.state = diskState{Version: 1, ServiceID: id, NextAddress: 2, Devices: make(map[string]persistedDevice), Pairings: make(map[string]time.Time)}
	if err = s.persist(s.state); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) ServiceID() string { s.mu.RLock(); defer s.mu.RUnlock(); return s.state.ServiceID }

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *Store) snapshot() diskState {
	n := s.state
	n.Devices = make(map[string]persistedDevice, len(s.state.Devices))
	for id, d := range s.state.Devices {
		d.Device.Endpoints = append([]string(nil), d.Device.Endpoints...)
		n.Devices[id] = d
	}
	n.Pairings = make(map[string]time.Time, len(s.state.Pairings))
	for k, v := range s.state.Pairings {
		n.Pairings[k] = v
	}
	return n
}

func (s *Store) persist(state diskState) error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".control-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), s.path); err != nil {
		return err
	}
	// Rename is the commit point. Sync the containing directory when supported.
	if dir, e := os.Open(filepath.Dir(s.path)); e == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func (s *Store) commit(n diskState) error {
	if err := s.persist(n); err != nil {
		return err
	}
	s.state = n
	s.reindexTokens()
	return nil
}

func (s *Store) CreatePairing(ttl time.Duration, now time.Time) (PairingCode, error) {
	if ttl <= 0 || ttl > 24*time.Hour {
		return PairingCode{}, ErrInvalid
	}
	buf := make([]byte, 15)
	if _, err := rand.Read(buf); err != nil {
		return PairingCode{}, err
	}
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.snapshot()
	for k, expires := range n.Pairings {
		if !now.Before(expires) {
			delete(n.Pairings, k)
		}
	}
	if len(n.Pairings) >= 1024 {
		return PairingCode{}, fmt.Errorf("%w: too many active pairing codes", ErrInvalid)
	}
	expires := now.UTC().Add(ttl)
	n.Pairings[hash(code)] = expires
	if err := s.commit(n); err != nil {
		return PairingCode{}, err
	}
	return PairingCode{ServiceID: n.ServiceID, Code: code, ExpiresAt: expires}, nil
}

var hostnameInvalid = regexp.MustCompile(`[^a-z0-9]+`)

func validKey(key string) bool {
	b, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(b) != 32 {
		return false
	}
	var any byte
	for _, v := range b {
		any |= v
	}
	return any != 0
}

func (s *Store) Enroll(req Enrollment, now time.Time) (EnrollmentResult, error) {
	if !validFingerprint(req.FileTLSFingerprint) {
		return EnrollmentResult{}, fmt.Errorf("%w: file TLS fingerprint must be a SHA-256 hex digest", ErrInvalid)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 128 || !validKey(req.PublicKey) {
		return EnrollmentResult{}, ErrInvalid
	}
	// Go's base64 decoder accepts line breaks and non-zero padding bits. The
	// public key is an identity, so compare and persist its canonical encoding
	// rather than allowing multiple spellings to bypass the uniqueness check.
	publicBytes, _ := base64.StdEncoding.DecodeString(req.PublicKey)
	req.PublicKey = base64.StdEncoding.EncodeToString(publicBytes)
	id, err := randomString(16)
	if err != nil {
		return EnrollmentResult{}, err
	}
	token, err := randomString(32)
	if err != nil {
		return EnrollmentResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := hash(strings.ToUpper(strings.TrimSpace(req.Code)))
	expires, ok := s.state.Pairings[key]
	if req.ServiceID != s.state.ServiceID || !ok || !now.Before(expires) {
		return EnrollmentResult{}, ErrPairing
	}
	for _, d := range s.state.Devices {
		existing, err := base64.StdEncoding.DecodeString(d.Device.PublicKey)
		if err == nil && base64.StdEncoding.EncodeToString(existing) == req.PublicKey {
			return EnrollmentResult{}, fmt.Errorf("%w: public key already registered", ErrInvalid)
		}
	}
	if s.state.NextAddress >= 65535 {
		return EnrollmentResult{}, errors.New("device address pool exhausted")
	}
	host := strings.Trim(hostnameInvalid.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(host) > 40 {
		host = strings.TrimRight(host[:40], "-")
	}
	if host == "" {
		host = "device"
	}
	// A suffix makes the name stable without conflicting after rename/revocation.
	host = host + "-" + hash(id)[:16] + ".jungo.internal"
	addr := s.state.NextAddress
	ip := fmt.Sprintf("100.96.%d.%d", addr>>8, addr&255)
	d := Device{ID: id, MeshID: "default", Name: name, Hostname: host, IP: ip, Address: ip + "/32", PublicKey: req.PublicKey, CreatedAt: now.UTC(), LastSeen: now.UTC()}
	d.FileTLSFingerprint = strings.ToLower(req.FileTLSFingerprint)
	n := s.snapshot()
	n.NextAddress++
	delete(n.Pairings, key)
	n.Devices[id] = persistedDevice{Device: d, TokenHash: hash(token)}
	if err = s.commit(n); err != nil {
		return EnrollmentResult{}, err
	}
	s.notifyLocked(Event{Type: "peers-changed", DeviceID: id})
	return EnrollmentResult{ServiceID: n.ServiceID, Token: token, Device: d}, nil
}

func copyDevice(d Device) Device {
	d.Endpoints = append([]string(nil), d.Endpoints...)
	if d.RevokedAt != nil {
		t := *d.RevokedAt
		d.RevokedAt = &t
	}
	return d
}

// Authentication runs for every relay packet. Index a full SHA-256 digest of
// the random token, never plaintext, and still check the current revocation bit.
func (s *Store) reindexTokens() {
	s.tokenDevices = make(map[[sha256.Size]byte]string, len(s.state.Devices))
	for id, d := range s.state.Devices {
		digest, err := hex.DecodeString(d.TokenHash)
		if err == nil && len(digest) == sha256.Size && d.Device.RevokedAt == nil {
			s.tokenDevices[[sha256.Size]byte(digest)] = id
		}
	}
}

func (s *Store) authenticateLocked(token string) (Device, error) {
	if len(token) < 32 || len(token) > 256 {
		return Device{}, ErrUnauthorized
	}
	id, ok := s.tokenDevices[sha256.Sum256([]byte(token))]
	if !ok {
		return Device{}, ErrUnauthorized
	}
	d, ok := s.state.Devices[id]
	if !ok || d.Device.RevokedAt != nil {
		return Device{}, ErrUnauthorized
	}
	return copyDevice(d.Device), nil
}

func (s *Store) Authenticate(token string) (Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.authenticateLocked(token)
}

// RenameDevice changes only the authenticated device's display name. Its stable
// hostname, address and cryptographic identity are never derived again.
func (s *Store) RenameDevice(token, name string) (Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.authenticateLocked(token)
	if err != nil {
		return Device{}, err
	}
	if !validRenameName(name) {
		return Device{}, fmt.Errorf("%w: name must be 1-128 UTF-8 bytes without surrounding or control whitespace", ErrInvalid)
	}
	if d.Name == name {
		return d, nil
	}
	d.Name = name
	n := s.snapshot()
	saved := n.Devices[d.ID]
	saved.Device = d
	n.Devices[d.ID] = saved
	if err := s.commit(n); err != nil {
		return Device{}, err
	}
	s.notifyLocked(Event{Type: "peers-changed", DeviceID: d.ID})
	return copyDevice(d), nil
}

func validRenameName(name string) bool {
	if name == "" || len(name) > 128 || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if unicode.Is(unicode.C, r) || (unicode.IsSpace(r) && r != ' ') {
			return false
		}
	}
	return true
}

func (s *Store) Peers(token string) ([]Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	self, err := s.authenticateLocked(token)
	if err != nil {
		return nil, err
	}
	peers := make([]Device, 0)
	for id, d := range s.state.Devices {
		if id != self.ID && d.Device.RevokedAt == nil && d.Device.MeshID == self.MeshID {
			peers = append(peers, copyDevice(d.Device))
		}
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].ID < peers[j].ID })
	return peers, nil
}

func (s *Store) ListDevices() []Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	devices := make([]Device, 0, len(s.state.Devices))
	for _, d := range s.state.Devices {
		devices = append(devices, copyDevice(d.Device))
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].ID < devices[j].ID })
	return devices
}

func (s *Store) Heartbeat(token string, req Heartbeat, now time.Time) (Device, error) {
	if !validFingerprint(req.FileTLSFingerprint) {
		return Device{}, fmt.Errorf("%w: file TLS fingerprint must be a SHA-256 hex digest", ErrInvalid)
	}
	if len(req.Endpoints) > 16 {
		return Device{}, ErrInvalid
	}
	for _, endpoint := range req.Endpoints {
		ap, err := netip.ParseAddrPort(endpoint)
		if err != nil || ap.Port() == 0 || ap.Addr().IsUnspecified() || ap.Addr().IsMulticast() {
			return Device{}, fmt.Errorf("%w: endpoint must be a unicast IP:port", ErrInvalid)
		}
	}
	if req.FileURL != "" {
		u, err := url.Parse(req.FileURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(req.FileURL) > 2048 {
			return Device{}, fmt.Errorf("%w: file_url must be HTTPS", ErrInvalid)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.authenticateLocked(token)
	if err != nil {
		return Device{}, err
	}
	// Endpoint order is immaterial; normalize it to avoid redundant durable
	// writes when interfaces are enumerated in a different order.
	endpoints := slices.Clone(req.Endpoints)
	slices.Sort(endpoints)
	endpoints = slices.Compact(endpoints)
	fingerprint := strings.ToLower(req.FileTLSFingerprint)
	topologyChanged := !slices.Equal(d.Endpoints, endpoints) || d.FileURL != req.FileURL || d.FileTLSFingerprint != fingerprint
	becameOnline := now.Sub(d.LastSeen) >= 45*time.Second
	d.LastSeen = now.UTC()
	d.Endpoints = endpoints
	d.FileURL = req.FileURL
	d.FileTLSFingerprint = fingerprint
	if topologyChanged {
		n := s.snapshot()
		saved := n.Devices[d.ID]
		saved.Device = d
		n.Devices[d.ID] = saved
		if err = s.commit(n); err != nil {
			return Device{}, err
		}
	} else {
		saved := s.state.Devices[d.ID]
		saved.Device = d
		s.state.Devices[d.ID] = saved
	}
	if topologyChanged || becameOnline {
		s.notifyLocked(Event{Type: "peers-changed", DeviceID: d.ID})
	}
	return copyDevice(d), nil
}

func validFingerprint(value string) bool {
	if value == "" {
		return true
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func (s *Store) CanCommunicate(fromID, toID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, aok := s.state.Devices[fromID]
	b, bok := s.state.Devices[toID]
	return aok && bok && fromID != toID && a.Device.RevokedAt == nil && b.Device.RevokedAt == nil && a.Device.MeshID == b.Device.MeshID
}

func (s *Store) Revoke(id string, now time.Time) error {
	s.mu.Lock()
	d, ok := s.state.Devices[id]
	if !ok {
		s.mu.Unlock()
		return ErrNotFound
	}
	if d.Device.RevokedAt != nil {
		s.mu.Unlock()
		return nil
	}
	n := s.snapshot()
	t := now.UTC()
	d.Device.RevokedAt = &t
	n.Devices[id] = d
	if err := s.commit(n); err != nil {
		s.mu.Unlock()
		return err
	}
	s.notifyLocked(Event{Type: "revoked", DeviceID: id})
	listeners := make([]func(string), 0, len(s.listeners))
	for _, fn := range s.listeners {
		listeners = append(listeners, fn)
	}
	s.mu.Unlock()
	for _, fn := range listeners {
		fn(id)
	}
	return nil
}

// SubscribeRevocations delivers synchronous notifications after a committed
// revocation and outside the store lock. Call the returned function on shutdown.
func (s *Store) SubscribeRevocations(fn func(string)) func() {
	s.mu.Lock()
	id := s.nextListener
	s.nextListener++
	s.listeners[id] = fn
	s.mu.Unlock()
	return func() { s.mu.Lock(); delete(s.listeners, id); s.mu.Unlock() }
}

func (s *Store) notifyLocked(event Event) {
	for _, ch := range s.events {
		select {
		case ch <- event:
		default:
		}
	}
}

// SubscribeEvents returns coalesced state-change hints. The channel is not
// closed by unsubscribe; use request context cancellation to stop consumers.
func (s *Store) SubscribeEvents() (<-chan Event, func()) {
	s.mu.Lock()
	id := s.nextListener
	s.nextListener++
	ch := make(chan Event, 1)
	s.events[id] = ch
	s.mu.Unlock()
	return ch, func() { s.mu.Lock(); delete(s.events, id); s.mu.Unlock() }
}
