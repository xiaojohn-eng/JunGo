package relay

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestClientSilentConnectionTimesOut(t *testing.T) {
	release := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		<-release // No close, read error, ping or data can trigger recovery.
	}))
	defer server.Close()
	defer close(release)
	client, err := DialWithConfig(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), strings.Repeat("a", 32), DialConfig{ReadTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, _, err = client.Receive()
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("silent relay did not produce a timeout: %v", err)
	}
}

func TestClientTrafficAndPingRefreshReadDeadline(t *testing.T) {
	for _, ping := range []bool{false, true} {
		name := "binary-data"
		if ping {
			name = "ping-and-pong"
		}
		t.Run(name, func(t *testing.T) {
			var pongs atomic.Int32
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				conn.SetPongHandler(func(string) error { pongs.Add(1); return nil })
				go func() {
					for {
						if _, _, err := conn.ReadMessage(); err != nil {
							return
						}
					}
				}()
				// The total interval exceeds ReadTimeout. Each packet/ping must
				// refresh the deadline for the final binary packet to arrive.
				for i := 0; i < 6; i++ {
					time.Sleep(150 * time.Millisecond)
					if ping {
						err = conn.WriteControl(websocket.PingMessage, []byte("alive"), time.Now().Add(time.Second))
					} else {
						frame, _ := EncodePacket("peer", []byte("alive"))
						err = conn.WriteMessage(websocket.BinaryMessage, frame)
					}
					if err != nil {
						return
					}
				}
				frame, _ := EncodePacket("peer", []byte("finished"))
				_ = conn.WriteMessage(websocket.BinaryMessage, frame)
			}))
			defer server.Close()
			client, err := DialWithConfig(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), strings.Repeat("a", 32), DialConfig{ReadTimeout: 750 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			for {
				id, packet, err := client.Receive()
				if err != nil {
					t.Fatalf("active relay timed out: %v", err)
				}
				if id != "peer" {
					t.Fatal(id)
				}
				if string(packet) == "finished" {
					break
				}
			}
			if ping && pongs.Load() == 0 {
				t.Fatal("custom ping handler stopped sending Pong replies")
			}
		})
	}
}
