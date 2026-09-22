package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xiaojohn-eng/JunGo/internal/control"
)

func enrollDevice(t *testing.T, store *control.Store, n byte) control.EnrollmentResult {
	t.Helper()
	p, err := store.CreatePairing(time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 32)
	b[0] = n
	r, err := store.Enroll(control.Enrollment{ServiceID: p.ServiceID, Code: p.Code, Name: "test", PublicKey: base64.StdEncoding.EncodeToString(b)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPacketCodecBounds(t *testing.T) {
	for _, size := range []int{1, 148, 65535} {
		payload := bytes.Repeat([]byte{0xff}, size)
		frame, err := EncodePacket("device_a", payload)
		if err != nil {
			t.Fatal(err)
		}
		id, p, err := DecodePacket(frame)
		if err != nil || id != "device_a" || !bytes.Equal(p, payload) {
			t.Fatalf("roundtrip %d: %v", size, err)
		}
	}
	for _, id := range []string{"", strings.Repeat("x", 129), "a\x00b", string([]byte{0xff})} {
		if _, err := EncodePacket(id, []byte{1}); err == nil {
			t.Fatalf("accepted ID %q", id)
		}
	}
	if _, err := EncodePacket("a", make([]byte, 65536)); err == nil {
		t.Fatal("oversize payload accepted")
	}
	for _, frame := range [][]byte{nil, {0, 0, 1}, {0, 3, 1, 2}, {0, 1, 'a'}, make([]byte, MaxFrame+1)} {
		if _, _, err := DecodePacket(frame); err == nil {
			t.Fatalf("accepted malformed frame: %d", len(frame))
		}
	}
	frame := []byte{0, 1, 0xff, 1}
	if _, _, err := DecodePacket(frame); err == nil {
		t.Fatal("invalid UTF8 ID accepted")
	}
}

func TestUnauthorizedOriginAndSessionLimits(t *testing.T) {
	store, _ := control.NewStore("")
	a := enrollDevice(t, store, 1)
	b := enrollDevice(t, store, 2)
	handler, _ := NewHandler(store, Config{MaxSessions: 1})
	defer handler.Close()
	server := httptest.NewServer(handler)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	for _, headers := range []http.Header{{}, {"Authorization": []string{"Bearer " + a.Token}, "Origin": []string{"https://untrusted.invalid"}}} {
		conn, response, err := websocket.DefaultDialer.Dial(wsURL, headers)
		if conn != nil {
			conn.Close()
		}
		if err == nil || response == nil || (response.StatusCode != 401 && response.StatusCode != 403) {
			t.Fatalf("unauthorized handshake: %v %+v", err, response)
		}
		if response != nil {
			response.Body.Close()
		}
	}
	client, err := Dial(context.Background(), wsURL, a.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if conn, err := Dial(context.Background(), wsURL, a.Token); err == nil {
		conn.Close()
		t.Fatal("duplicate session accepted")
	}
	if conn, err := Dial(context.Background(), wsURL, b.Token); err == nil {
		conn.Close()
		t.Fatal("session limit ignored")
	}
	if err = store.Revoke(a.Device.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if conn, err := Dial(context.Background(), wsURL, a.Token); err == nil {
		conn.Close()
		t.Fatal("revoked enrollment connected")
	}
}

func TestCiphertextForwardingAndImmediateRevocation(t *testing.T) {
	store, _ := control.NewStore("")
	a := enrollDevice(t, store, 1)
	b := enrollDevice(t, store, 2)
	handler, _ := NewHandler(store, Config{})
	defer handler.Close()
	server := httptest.NewServer(handler)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ac, err := Dial(context.Background(), wsURL, a.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer ac.Close()
	bc, err := Dial(context.Background(), wsURL, b.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Close()
	_ = bc.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_ = ac.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	packet := bytes.Repeat([]byte{0xde, 0xad, 0xbe, 0xef}, 1024)
	if err = ac.Send(b.Device.ID, packet); err != nil {
		t.Fatal(err)
	}
	id, received, err := bc.Receive()
	if err != nil || id != a.Device.ID || !bytes.Equal(received, packet) {
		t.Fatalf("forwarding source or bytes changed: %s %v", id, err)
	}
	if err = bc.Send(a.Device.ID, []byte("opaque reply")); err != nil {
		t.Fatal(err)
	}
	id, received, err = ac.Receive()
	if err != nil || id != b.Device.ID || string(received) != "opaque reply" {
		t.Fatalf("reply %s %v", id, err)
	}
	if err = store.Revoke(b.Device.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err = bc.Receive(); err == nil {
		t.Fatal("revoked live connection remained open")
	}
}

func TestUnknownDestinationAndTextFrameCloseSender(t *testing.T) {
	for _, test := range []string{"unknown", "text", "oversize"} {
		t.Run(test, func(t *testing.T) {
			store, _ := control.NewStore("")
			a := enrollDevice(t, store, 1)
			handler, _ := NewHandler(store, Config{})
			defer handler.Close()
			server := httptest.NewServer(handler)
			defer server.Close()
			c, err := Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), a.Token)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_ = c.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			switch test {
			case "unknown":
				err = c.Send("unknown", []byte{1})
			case "text":
				err = c.conn.WriteMessage(websocket.TextMessage, []byte("not ciphertext"))
			case "oversize":
				frame := make([]byte, MaxFrame+1)
				binary.BigEndian.PutUint16(frame, 1)
				frame[2] = 'a'
				err = c.conn.WriteMessage(websocket.BinaryMessage, frame)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = c.Receive(); err == nil {
				t.Fatal("invalid frame kept connection open")
			}
		})
	}
}

func TestClientRejectsCleartextRemoteAndCredentialURL(t *testing.T) {
	for _, rawURL := range []string{"ws://192.168.1.2:8080/v1/relay", "wss://user:secret@example.com/v1/relay", "wss://example.com/v1/relay?token=bad"} {
		if c, err := Dial(context.Background(), rawURL, strings.Repeat("t", 40)); err == nil {
			c.Close()
			t.Fatalf("accepted unsafe URL %s", rawURL)
		}
	}
}
