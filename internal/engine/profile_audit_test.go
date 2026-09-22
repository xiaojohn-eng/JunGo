package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaojohn-eng/JunGo/internal/proxycore"
	"golang.org/x/net/proxy"
)

const auditProfile = "proxies:\n  - {name: alpha, type: direct}\n  - {name: beta, type: direct}\n"

func auditEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func denyConfigWrite(t *testing.T, e *Engine) func() {
	t.Helper()
	path := filepath.Join(e.dir, "device.json")
	backup := path + ".test-backup"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(backup, path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProfileImportSelectionSurvivesVPNRestart(t *testing.T) {
	e := auditEngine(t)
	call(t, e, "importProfile", map[string]string{"YAML": "proxies: [{name: old, type: direct}]"})
	call(t, e, "selectProxy", map[string]string{"Name": "old"})
	call(t, e, "network", map[string]bool{"proxy": true})
	if err := e.StartVPN(-1); err != nil {
		t.Fatal(err)
	}
	call(t, e, "importProfile", map[string]string{"YAML": "proxies: [{name: replacement, type: direct}]"})
	if e.config().Selected != "replacement" {
		t.Fatal("live fallback selection was not saved")
	}
	e.StopVPN()
	if err := e.StartVPN(-1); err != nil {
		t.Fatalf("updated profile cannot restart: %v", err)
	}
	e.StopVPN()
	call(t, e, "importProfile", map[string]string{"YAML": "proxies: [{name: offline-update, type: direct}]"})
	if e.config().Selected != "offline-update" {
		t.Fatal("offline import retained a removed selection")
	}
	if _, err := e.Request(`{"method":"selectProxy","params":{"Name":"missing"}}`); err == nil {
		t.Fatal("offline invalid selection accepted")
	}
	if err := e.StartVPN(-1); err != nil {
		t.Fatalf("offline update cannot restart: %v", err)
	}
}

func TestProfileWriteFailurePreservesLiveProfile(t *testing.T) {
	e := auditEngine(t)
	call(t, e, "importProfile", map[string]string{"YAML": auditProfile})
	call(t, e, "selectProxy", map[string]string{"Name": "beta"})
	if err := e.StartVPN(-1); err != nil {
		t.Fatal(err)
	}
	restore := denyConfigWrite(t, e)
	defer restore()
	if _, err := e.Request(`{"method":"importProfile","params":{"YAML":"proxies: [{name: replacement, type: direct}]"}}`); err == nil {
		t.Fatal("write failure was hidden")
	}
	state := e.State()
	if e.config().Profile != auditProfile || len(state.Proxies) != 2 {
		t.Fatal("failed persistence changed the saved or live profile")
	}
	for _, p := range state.Proxies {
		if p.Selected != (p.Name == "beta") {
			t.Fatalf("failed import changed runtime selection: %+v", state.Proxies)
		}
	}
}

func TestPolicyWriteFailureRestoresRuntime(t *testing.T) {
	e := auditEngine(t)
	c := e.config()
	c.Profile, c.Mode, c.Selected, c.ProxyEnabled = auditProfile, "global", "REJECT", true
	if err := e.commit(c); err != nil {
		t.Fatal(err)
	}
	core, err := proxycore.Start(proxycore.Config{StateDir: filepath.Join(e.dir, "proxy"), TUNFD: -1, Profile: []byte(c.Profile), Mode: c.Mode, Selected: c.Selected, ProxyEnabled: true, MixedAddress: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.core = core
	e.mu.Unlock()
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	dialer, err := proxy.SOCKS5("tcp", core.MixedAddress(), nil, &net.Dialer{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	restore := denyConfigWrite(t, e)
	defer restore()
	for _, request := range []string{
		`{"method":"mode","params":{"Mode":"direct"}}`,
		`{"method":"selectProxy","params":{"Name":"alpha"}}`,
	} {
		if _, err := e.Request(request); err == nil {
			t.Fatal("configuration write unexpectedly succeeded")
		}
		conn, err := dialer.Dial("tcp", target.Addr().String())
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			_, _ = conn.Write([]byte("probe"))
			_, readErr := io.ReadFull(conn, make([]byte, 5))
			conn.Close()
			if readErr == nil {
				t.Fatal("failed policy save changed live traffic from reject to direct")
			}
		}
	}
}

func TestFailedSelectionRestoresLegacyImplicitDefault(t *testing.T) {
	e := auditEngine(t)
	c := e.config()
	c.Profile, c.Selected = auditProfile, ""
	if err := e.commit(c); err != nil {
		t.Fatal(err)
	}
	if err := e.StartVPN(-1); err != nil {
		t.Fatal(err)
	}
	if e.core.Selected() != "alpha" {
		t.Fatal("unexpected implicit default")
	}
	restore := denyConfigWrite(t, e)
	defer restore()
	if _, err := e.Request(`{"method":"selectProxy","params":{"Name":"beta"}}`); err == nil {
		t.Fatal("write failure was hidden")
	}
	if e.core.Selected() != "alpha" {
		t.Fatal("rollback re-selected the failed new default instead of the original")
	}
}

func TestOfflineBypassRejectsRootUID(t *testing.T) {
	e := auditEngine(t)
	if _, err := e.Request(`{"method":"network","params":{"bypassUIDs":[0]}}`); err == nil {
		t.Fatal("persisted a UID rejected by the VPN core on next startup")
	}
	if len(e.config().BypassUIDs) != 0 {
		t.Fatal("invalid bypass persisted")
	}
}

func TestSlowProxyTestDoesNotBlockModeChange(t *testing.T) {
	e := auditEngine(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	call(t, e, "importProfile", map[string]string{"YAML": fmt.Sprintf("proxies: [{name: slow, type: socks5, server: 127.0.0.1, port: %d}]", port)})
	if err := e.StartVPN(-1); err != nil {
		t.Fatal(err)
	}
	testDone := make(chan struct{})
	go func() {
		defer close(testDone)
		_, _ = e.Request(`{"method":"testProxy","params":{"Name":"slow"}}`)
	}()
	select {
	case conn := <-accepted:
		defer conn.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("latency test never started its slow connection")
	}
	changed := make(chan error, 1)
	go func() {
		_, err := e.Request(`{"method":"mode","params":{"Mode":"direct"}}`)
		changed <- err
	}()
	select {
	case err := <-changed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("latency test held the network configuration lock")
	}
	e.cancel()
	select {
	case <-testDone:
	case <-time.After(3 * time.Second):
		t.Fatal("latency test ignored shutdown cancellation")
	}
}

func TestSubscriptionErrorsNeverExposeSecretURL(t *testing.T) {
	secretURL := "https://example.invalid/secret-path?token=must-never-appear"
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errProfileRedirect, errors.New("dial failed")} {
		message := profileDownloadError(&url.Error{Op: http.MethodGet, URL: secretURL, Err: cause}).Error()
		if strings.Contains(message, "https") || strings.Contains(message, "secret-path") || strings.Contains(message, "must-never-appear") {
			t.Fatal("error contains subscription address or credential")
		}
	}
	e := auditEngine(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	_, err = e.downloadProfile("https://" + address + "/secret-path?token=must-never-appear")
	if err == nil || strings.Contains(err.Error(), "secret-path") || strings.Contains(err.Error(), "must-never-appear") {
		t.Fatalf("HTTP error exposed subscription credentials: %v", err)
	}
}
