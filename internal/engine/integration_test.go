package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaojohn-eng/JunGo/internal/control"
	"github.com/xiaojohn-eng/JunGo/internal/relay"
	"github.com/xiaojohn-eng/JunGo/internal/secure"
)

func call(t *testing.T, e *Engine, method string, params any) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"method": method, "params": params})
	result, err := e.Request(string(b))
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return result
}
func fixture(t *testing.T, wrappers ...func(http.Handler) http.Handler) (*Engine, *Engine, *control.Store) {
	t.Helper()
	root := t.TempDir()
	store, err := control.NewStore(filepath.Join(root, "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := control.NewHandler(store, control.Config{AdminToken: secure.Random(32)})
	if err != nil {
		t.Fatal(err)
	}
	rel, err := relay.NewHandler(store, relay.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rel.Close() })
	mux := http.NewServeMux()
	mux.Handle("/v1/relay", rel)
	mux.Handle("/", h)
	var handler http.Handler = mux
	for _, wrap := range wrappers {
		handler = wrap(handler)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	server.StartTLS()
	t.Cleanup(server.Close)
	sum := sha256.Sum256(server.Certificate().Raw)
	fp := hex.EncodeToString(sum[:])
	a, err := New(filepath.Join(root, "a"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	b, err := New(filepath.Join(root, "b"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	for i, eng := range []*Engine{a, b} {
		pair, err := store.CreatePairing(time.Minute, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		call(t, eng, "pair", map[string]any{"server": server.URL, "fingerprint": fp, "serviceId": pair.ServiceID, "code": pair.Code, "name": []string{"phone", "mac"}[i]})
	}
	return a, b, store
}
func wait(t *testing.T, d time.Duration, condition func() bool) {
	t.Helper()
	until := time.Now().Add(d)
	for time.Now().Before(until) {
		if condition() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}
func TestIntegratedPairTransferResumeAndRevoke(t *testing.T) {
	a, b, store := fixture(t)
	defer func() {
		if t.Failed() {
			t.Logf("sender: %+v; tasks: %+v; receiver: %+v", a.State(), a.transferSnapshot(), b.State())
		}
	}()
	share := t.TempDir()
	call(t, b, "shareAdd", map[string]any{"name": "Files", "path": share})
	sid := b.State().Shares[0].ID
	call(t, a, "network", map[string]bool{"mesh": true})
	call(t, b, "network", map[string]bool{"mesh": true})
	wait(t, 15*time.Second, func() bool {
		return len(a.State().Peers) == 1 && a.State().Peers[0].FileTLSFingerprint != "" && len(b.State().Peers) == 1
	})
	payload := bytes.Repeat([]byte("真实WireGuard与HTTPS传输\n"), 10000)
	source := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(source, payload, 0600); err != nil {
		t.Fatal(err)
	}
	var upload Transfer
	if err := json.Unmarshal([]byte(call(t, a, "upload", map[string]any{"deviceId": b.State().Device.ID, "shareId": sid, "path": "nested/sample.bin", "source": source})), &upload); err != nil {
		t.Fatal(err)
	}
	call(t, a, "transferAction", map[string]string{"id": upload.ID, "action": "pause"})
	time.Sleep(100 * time.Millisecond)
	tasks := a.transferSnapshot()
	if tasks[0].Status != "paused" {
		t.Fatal("pause lost", tasks)
	}
	call(t, a, "transferAction", map[string]string{"id": upload.ID, "action": "resume"})
	wait(t, 2*time.Minute, func() bool {
		for _, task := range a.transferSnapshot() {
			if task.ID == upload.ID {
				if task.Status == "failed" {
					t.Fatal(task.Error)
				}
				return task.Status == "complete"
			}
		}
		return false
	})
	content, err := os.ReadFile(filepath.Join(share, "nested/sample.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, payload) {
		t.Fatal("uploaded bytes differ")
	}
	dest := filepath.Join(t.TempDir(), "download.bin")
	var download Transfer
	json.Unmarshal([]byte(call(t, a, "download", map[string]any{"deviceId": b.State().Device.ID, "shareId": sid, "path": "nested/sample.bin", "destination": dest})), &download)
	wait(t, 2*time.Minute, func() bool {
		for _, task := range a.transferSnapshot() {
			if task.ID == download.ID {
				if task.Status == "failed" {
					t.Fatal(task.Error)
				}
				return task.Status == "complete"
			}
		}
		return false
	})
	content, err = os.ReadFile(dest)
	if err != nil || !bytes.Equal(content, payload) {
		t.Fatal("download mismatch", err)
	}
	if err = store.Revoke(b.State().Device.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	wait(t, 5*time.Second, func() bool { return len(a.State().Peers) == 0 })
	if _, _, _, _, err = a.peerClient(b.State().Device.ID); err == nil {
		t.Fatal("revoked peer still accessible")
	}
}

func TestLargeFiveGiBTransfer(t *testing.T) {
	if os.Getenv("JUNGO_LARGE_TEST") != "1" {
		t.Skip("set JUNGO_LARGE_TEST=1 for 5GiB WireGuard upload/download acceptance")
	}
	a, b, _ := fixture(t)
	share := t.TempDir()
	call(t, b, "shareAdd", map[string]any{"name": "Large", "path": share})
	sid := b.State().Shares[0].ID
	call(t, a, "network", map[string]bool{"mesh": true})
	call(t, b, "network", map[string]bool{"mesh": true})
	wait(t, 15*time.Second, func() bool {
		return len(a.State().Peers) == 1 && a.State().Peers[0].FileTLSFingerprint != "" && len(b.State().Peers) == 1
	})
	source := filepath.Join(t.TempDir(), "five-gib.bin")
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	size := int64(5) << 30
	if err = f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	f.Close()
	var up Transfer
	json.Unmarshal([]byte(call(t, a, "upload", map[string]any{"deviceId": b.State().Device.ID, "shareId": sid, "path": "five-gib.bin", "source": source})), &up)
	wait(t, 25*time.Minute, func() bool {
		for _, task := range a.transferSnapshot() {
			if task.ID == up.ID {
				if task.Status == "failed" {
					t.Fatal(task.Error)
				}
				return task.Status == "complete"
			}
		}
		return false
	})
	destination := filepath.Join(t.TempDir(), "download.bin")
	var down Transfer
	json.Unmarshal([]byte(call(t, a, "download", map[string]any{"deviceId": b.State().Device.ID, "shareId": sid, "path": "five-gib.bin", "destination": destination})), &down)
	wait(t, 25*time.Minute, func() bool {
		for _, task := range a.transferSnapshot() {
			if task.ID == down.ID {
				if task.Status == "failed" {
					t.Fatal(task.Error)
				}
				return task.Status == "complete"
			}
		}
		return false
	})
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	h1, h2 := sha256.New(), sha256.New()
	io.Copy(h1, input)
	io.Copy(h2, output)
	if !bytes.Equal(h1.Sum(nil), h2.Sum(nil)) {
		t.Fatal("5GiB checksum mismatch")
	}
	t.Log("5GiB upload and download checksums match")
}

func TestNoUnprotectedFileRequest(t *testing.T) {
	a, _, _ := fixture(t)
	_, err := a.fileJSON(context.Background(), "unknown", "GET", "/v1/files/shares", nil, nil)
	if err == nil {
		t.Fatal("unknown peer permitted")
	}
}
