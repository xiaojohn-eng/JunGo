package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client implements the mesh packet relay interface: concurrent Send is safe;
// exactly one goroutine may call Receive at a time. Call Close to unblock it.
type Client struct {
	conn        *websocket.Conn
	writeMu     sync.Mutex
	writeBuffer []byte
	readTimeout time.Duration
}

type DialConfig struct {
	TLSConfig        *tls.Config
	HandshakeTimeout time.Duration
	// ReadTimeout bounds a silent or blackholed connection. Binary frames and
	// server pings refresh it; zero uses 75 seconds (server pings every 20s).
	ReadTimeout    time.Duration
	NetDialContext func(context.Context, string, string) (net.Conn, error)
}

func Dial(ctx context.Context, rawURL, token string) (*Client, error) {
	return DialWithConfig(ctx, rawURL, token, DialConfig{})
}

func DialWithConfig(ctx context.Context, rawURL, token string, cfg DialConfig) (*Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid relay URL")
	}
	if u.Scheme != "wss" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "ws" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return nil, errors.New("relay requires wss except on loopback")
		}
	}
	if len(token) < 32 || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("invalid relay token")
	}
	if cfg.TLSConfig != nil && cfg.TLSConfig.InsecureSkipVerify && cfg.TLSConfig.VerifyConnection == nil && cfg.TLSConfig.VerifyPeerCertificate == nil {
		return nil, errors.New("relay TLS certificate verification is required")
	}
	if cfg.HandshakeTimeout == 0 {
		cfg.HandshakeTimeout = 10 * time.Second
	}
	if cfg.ReadTimeout < 0 {
		return nil, errors.New("relay read timeout must be positive")
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = 75 * time.Second
	}
	dialer := websocket.Dialer{NetDialContext: cfg.NetDialContext, HandshakeTimeout: cfg.HandshakeTimeout, TLSClientConfig: cfg.TLSConfig, ReadBufferSize: 4096, WriteBufferSize: 4096}
	conn, response, err := dialer.DialContext(ctx, rawURL, http.Header{"Authorization": []string{"Bearer " + token}})
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, err
	}
	conn.SetReadLimit(MaxFrame)
	client := &Client{conn: conn, readTimeout: cfg.ReadTimeout}
	if err = client.refreshReadDeadline(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	conn.SetPingHandler(func(data string) error {
		if err := client.refreshReadDeadline(); err != nil {
			return err
		}
		// Replacing Gorilla's default ping handler must retain its Pong reply.
		// WriteControl is safe alongside the data writer and remains bounded.
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(10*time.Second))
	})
	return client, nil
}

func (c *Client) refreshReadDeadline() error {
	return c.conn.SetReadDeadline(time.Now().Add(c.readTimeout))
}

func (c *Client) Send(peerID string, packet []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	frame, err := encodePacket(c.writeBuffer, peerID, packet)
	if err != nil {
		return err
	}
	// WriteMessage consumes its input before returning. A single bounded buffer
	// therefore suffices across writes, including concurrent Send callers.
	c.writeBuffer = frame
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.conn.WriteMessage(websocket.BinaryMessage, frame)
}

func (c *Client) Receive() (string, []byte, error) {
	for {
		kind, frame, err := c.conn.ReadMessage()
		if err != nil {
			return "", nil, err
		}
		if kind != websocket.BinaryMessage {
			return "", nil, ErrPacket
		}
		if err := c.refreshReadDeadline(); err != nil {
			return "", nil, err
		}
		return DecodePacket(frame)
	}
}

func (c *Client) Close() error { return c.conn.Close() }
