package proxycore

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A real HTTP CONNECT upstream verifies that public node selection actually
// traverses mihomo's proxy protocol, rather than silently dialing DIRECT.
func TestPublicProxyProtocolAndLivePolicy(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveEcho(t, echo)
	var connects atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", 405)
			return
		}
		destination, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer destination.Close()
		c, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		connects.Add(1)
		fmt.Fprint(rw, "HTTP/1.1 200 Connection Established\r\n\r\n")
		rw.Flush()
		done := make(chan struct{})
		go func() { io.Copy(destination, rw); destination.(*net.TCPConn).CloseWrite(); close(done) }()
		io.Copy(c, destination)
		c.Close()
		<-done
	}))
	defer upstream.Close()
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	profile := []byte(fmt.Sprintf("proxies:\n  - {name: actual-http, type: http, server: %s, port: %s}\nrules:\n  - MATCH,actual-http\n", host, port))
	core, err := Start(Config{StateDir: t.TempDir(), TUNFD: -1, Profile: profile, ProxyEnabled: true, Mode: "rule", Selected: "actual-http", MixedAddress: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	for _, mode := range []string{"rule", "global"} {
		if err = core.SetPolicy(true, mode, "actual-http", nil); err != nil {
			t.Fatal(err)
		}
		c := socksConn(t, core, echo.Addr().String())
		assertEcho(t, c)
		c.Close()
	}
	if connects.Load() != 2 {
		t.Fatalf("expected two real upstream proxy connections; got %d", connects.Load())
	}
	if err = core.SetPolicy(true, "direct", "actual-http", nil); err != nil {
		t.Fatal(err)
	}
	c := socksConn(t, core, echo.Addr().String())
	assertEcho(t, c)
	c.Close()
	if connects.Load() != 2 {
		t.Fatal("direct mode still used the public proxy")
	}
	if err = core.LoadProfile([]byte("proxies:\n  - {name: new-direct, type: direct}\nrules:\n  - MATCH,new-direct\n")); err != nil {
		t.Fatal(err)
	}
	if err = core.SetPolicy(true, "rule", "new-direct", nil); err != nil {
		t.Fatal(err)
	}
	c = socksConn(t, core, echo.Addr().String())
	assertEcho(t, c)
	c.Close()
	if connects.Load() != 2 {
		t.Fatal("removed upstream remained active for new connections")
	}
}
