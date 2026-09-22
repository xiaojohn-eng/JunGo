// Package relay forwards opaque WireGuard packets between authenticated devices.
// It neither creates WireGuard keys nor terminates their encryption.
package relay

import (
	"encoding/binary"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/xiaojohn-eng/JunGo/internal/control"
)

const MaxPayload = 65535
const MaxDeviceID = 128
const MaxFrame = 2 + MaxDeviceID + MaxPayload

var ErrPacket = errors.New("invalid relay packet")
var ErrClosed = errors.New("relay closed")

// EncodePacket encodes target ID for client-to-server traffic, or authenticated
// sender ID for server-to-client traffic. Data is WireGuard ciphertext only.
func EncodePacket(deviceID string, payload []byte) ([]byte, error) {
	return encodePacket(nil, deviceID, payload)
}

// The caller owns dst. Payload may alias it when replacing a received routing
// header; copy payload first so changing the ID length cannot corrupt ciphertext.
func encodePacket(dst []byte, deviceID string, payload []byte) ([]byte, error) {
	if len(deviceID) < 1 || len(deviceID) > MaxDeviceID || !utf8.ValidString(deviceID) || strings.ContainsAny(deviceID, "\x00\r\n") || len(payload) < 1 || len(payload) > MaxPayload {
		return nil, ErrPacket
	}
	size := 2 + len(deviceID) + len(payload)
	frame := dst
	if cap(frame) < size {
		frame = make([]byte, size)
	} else {
		frame = frame[:size]
	}
	copy(frame[2+len(deviceID):], payload)
	binary.BigEndian.PutUint16(frame, uint16(len(deviceID)))
	copy(frame[2:], deviceID)
	return frame, nil
}

// DecodePacket returns a payload slice backed by frame; copy it before reuse.
func DecodePacket(frame []byte) (string, []byte, error) {
	if len(frame) < 4 || len(frame) > MaxFrame {
		return "", nil, ErrPacket
	}
	n := int(binary.BigEndian.Uint16(frame[:2]))
	if n < 1 || n > MaxDeviceID || len(frame) <= 2+n || len(frame)-(2+n) > MaxPayload {
		return "", nil, ErrPacket
	}
	id := string(frame[2 : 2+n])
	if !utf8.ValidString(id) || strings.ContainsAny(id, "\x00\r\n") {
		return "", nil, ErrPacket
	}
	return id, frame[2+n:], nil
}

type Authorizer interface {
	Authenticate(token string) (control.Device, error)
	CanCommunicate(fromID, toID string) bool
	SubscribeRevocations(func(deviceID string)) func()
}

type Config struct {
	MaxSessions  int
	QueueDepth   int
	WriteTimeout time.Duration
	PongTimeout  time.Duration
	PingInterval time.Duration
}

type session struct {
	device control.Device
	token  string
	conn   *websocket.Conn
	queue  chan []byte
	done   chan struct{}
	once   sync.Once
}

func (s *session) close() { s.once.Do(func() { close(s.done); _ = s.conn.Close() }) }

type Handler struct {
	auth        Authorizer
	cfg         Config
	mu          sync.Mutex
	sessions    map[string]*session
	reserved    map[string]bool
	closed      bool
	unsubscribe func()
}

func NewHandler(auth Authorizer, cfg Config) (*Handler, error) {
	if auth == nil {
		return nil, errors.New("relay authorizer is required")
	}
	if cfg.MaxSessions == 0 {
		cfg.MaxSessions = 128
	}
	if cfg.QueueDepth == 0 {
		cfg.QueueDepth = 64
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 10 * time.Second
	}
	if cfg.PongTimeout == 0 {
		cfg.PongTimeout = 60 * time.Second
	}
	if cfg.PingInterval == 0 {
		cfg.PingInterval = 20 * time.Second
	}
	if cfg.MaxSessions < 1 || cfg.MaxSessions > 4096 || cfg.QueueDepth < 1 || cfg.QueueDepth > 1024 || cfg.WriteTimeout <= 0 || cfg.PingInterval <= 0 || cfg.PongTimeout <= cfg.PingInterval {
		return nil, errors.New("invalid relay limits")
	}
	h := &Handler{auth: auth, cfg: cfg, sessions: make(map[string]*session), reserved: make(map[string]bool)}
	h.unsubscribe = auth.SubscribeRevocations(func(id string) {
		h.mu.Lock()
		s := h.sessions[id]
		h.mu.Unlock()
		if s != nil {
			s.close()
		}
	})
	return h, nil
}

// ServeHTTP accepts authenticated native clients only. Browser-origin requests
// and URL query tokens are deliberately unsupported. Mount at /v1/relay.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.RawQuery != "" || r.Header.Get("Origin") != "" {
		http.Error(w, "native bearer authentication required", http.StatusForbidden)
		return
	}
	token := control.BearerToken(r)
	device, err := h.auth.Authenticate(token)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		http.Error(w, "relay closed", http.StatusServiceUnavailable)
		return
	}
	if h.reserved[device.ID] || h.sessions[device.ID] != nil {
		h.mu.Unlock()
		http.Error(w, "device already connected", http.StatusConflict)
		return
	}
	if len(h.sessions)+len(h.reserved) >= h.cfg.MaxSessions {
		h.mu.Unlock()
		http.Error(w, "relay capacity reached", http.StatusServiceUnavailable)
		return
	}
	h.reserved[device.ID] = true
	h.mu.Unlock()
	upgrader := websocket.Upgrader{HandshakeTimeout: 10 * time.Second, ReadBufferSize: 4096, WriteBufferSize: 4096, CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.mu.Lock()
		delete(h.reserved, device.ID)
		h.mu.Unlock()
		return
	}
	s := &session{device: device, token: token, conn: conn, queue: make(chan []byte, h.cfg.QueueDepth), done: make(chan struct{})}
	h.mu.Lock()
	delete(h.reserved, device.ID)
	if h.closed {
		h.mu.Unlock()
		s.close()
		return
	}
	h.sessions[device.ID] = s
	h.mu.Unlock()
	// Recheck after registration so a revoke racing the handshake cannot leave a
	// live session that missed its notification.
	if _, err = h.auth.Authenticate(token); err != nil {
		h.remove(s)
		return
	}
	defer h.remove(s)
	conn.SetReadLimit(MaxFrame)
	_ = conn.SetReadDeadline(time.Now().Add(h.cfg.PongTimeout))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(h.cfg.PongTimeout)) })
	go h.writeLoop(s)
	for {
		kind, frame, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if kind != websocket.BinaryMessage {
			h.closePolicy(s, "binary ciphertext required")
			return
		}
		targetID, payload, err := DecodePacket(frame)
		if err != nil {
			h.closePolicy(s, "invalid relay packet")
			return
		}
		if _, err = h.auth.Authenticate(s.token); err != nil {
			return
		}
		if !h.auth.CanCommunicate(s.device.ID, targetID) {
			h.closePolicy(s, "peer unavailable or unauthorized")
			return
		}
		h.mu.Lock()
		target := h.sessions[targetID]
		h.mu.Unlock()
		// Offline peers and congested sessions drop packets, as UDP does. Keeping
		// per-device queues bounded prevents one slow receiver exhausting memory.
		if target == nil {
			continue
		}
		// ReadMessage transfers ownership of this frame to us. Reuse it for the
		// authenticated sender header, then transfer ownership to the bounded queue.
		out, _ := encodePacket(frame, s.device.ID, payload)
		select {
		case <-target.done:
		case target.queue <- out:
		default:
		}
	}
}

func (h *Handler) closePolicy(s *session, reason string) {
	_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason), time.Now().Add(h.cfg.WriteTimeout))
}

func (h *Handler) writeLoop(s *session) {
	ticker := time.NewTicker(h.cfg.PingInterval)
	defer ticker.Stop()
	defer s.close()
	for {
		select {
		case <-s.done:
			return
		case frame := <-s.queue:
			if _, err := h.auth.Authenticate(s.token); err != nil {
				return
			}
			source, _, err := DecodePacket(frame)
			if err != nil || !h.auth.CanCommunicate(source, s.device.ID) {
				continue
			}
			_ = s.conn.SetWriteDeadline(time.Now().Add(h.cfg.WriteTimeout))
			if err = s.conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				return
			}
		case <-ticker.C:
			if _, err := h.auth.Authenticate(s.token); err != nil {
				return
			}
			if err := s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(h.cfg.WriteTimeout)); err != nil {
				return
			}
		}
	}
}

func (h *Handler) remove(s *session) {
	s.close()
	h.mu.Lock()
	if h.sessions[s.device.ID] == s {
		delete(h.sessions, s.device.ID)
	}
	h.mu.Unlock()
}

func (h *Handler) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	sessions := make([]*session, 0, len(h.sessions))
	for _, s := range h.sessions {
		sessions = append(sessions, s)
	}
	h.mu.Unlock()
	if h.unsubscribe != nil {
		h.unsubscribe()
	}
	for _, s := range sessions {
		s.close()
	}
	return nil
}
