package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testKey(n byte) string {
	b := make([]byte, 32)
	b[0] = n
	return base64.StdEncoding.EncodeToString(b)
}

func enroll(t *testing.T, s *Store, name string, n byte) EnrollmentResult {
	t.Helper()
	now := time.Now()
	code, err := s.CreatePairing(time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Enroll(Enrollment{ServiceID: code.ServiceID, Code: code.Code, Name: name, PublicKey: testKey(n)}, now)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPairingServiceBindingExpiryReuseAndAtomicConsumption(t *testing.T) {
	s, err := NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	p, err := s.CreatePairing(time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	req := Enrollment{ServiceID: "another-service", Code: p.Code, Name: "phone", PublicKey: testKey(1)}
	if _, err = s.Enroll(req, now); !errors.Is(err, ErrPairing) {
		t.Fatalf("wrong service: %v", err)
	}
	req.ServiceID = p.ServiceID
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := s.Enroll(req, now); results <- e }()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrPairing) {
			t.Fatalf("concurrent enroll: %v", err)
		}
	}
	if success != 1 {
		t.Fatalf("pairing consumed %d times", success)
	}
	p, _ = s.CreatePairing(time.Minute, now)
	req.Code = p.Code
	req.PublicKey = testKey(2)
	if _, err = s.Enroll(req, now.Add(time.Minute)); !errors.Is(err, ErrPairing) {
		t.Fatalf("expiry boundary: %v", err)
	}
}

func TestPersistenceHashesIdentityAndRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreatePairing(time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d := enroll(t, s, "My Mac", 3)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(p.Code)) || bytes.Contains(b, []byte(d.Token)) {
		t.Fatal("plaintext authorization secret persisted")
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("permissions %o", fi.Mode().Perm())
	}
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	device, err := reloaded.Authenticate(d.Token)
	if err != nil {
		t.Fatal(err)
	}
	if device.IP != "100.96.0.2" || device.Address != device.IP+"/32" || device.ID != d.Device.ID || reloaded.ServiceID() != s.ServiceID() {
		t.Fatalf("unstable identity: %+v", device)
	}
	// Force a disk failure; a failed revocation must not silently change memory.
	s.path = filepath.Join(path, "cannot-write.json")
	if err = s.Revoke(d.Device.ID, time.Now()); err == nil {
		t.Fatal("expected persistence error")
	}
	if _, err = s.Authenticate(d.Token); err != nil {
		t.Fatalf("failed transaction changed memory: %v", err)
	}
}

func TestPeersHeartbeatRevocation(t *testing.T) {
	s, _ := NewStore("")
	a := enroll(t, s, "安卓", 1)
	b := enroll(t, s, "Mac", 2)
	if a.Device.Hostname == b.Device.Hostname || a.Device.IP == b.Device.IP {
		t.Fatal("identity collision")
	}
	if _, err := s.Peers("bad token"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	peers, err := s.Peers(a.Token)
	if err != nil || len(peers) != 1 || peers[0].ID != b.Device.ID {
		t.Fatalf("peer list: %+v %v", peers, err)
	}
	d, err := s.Heartbeat(b.Token, Heartbeat{Endpoints: []string{"192.168.1.2:51820", "[2001:db8::1]:51820"}, FileURL: "https://100.96.0.3:8443"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d.Endpoints[0] = "mutated"
	actual, _ := s.Authenticate(b.Token)
	if actual.Endpoints[0] == "mutated" {
		t.Fatal("caller modified registry through returned slice")
	}
	for _, endpoint := range []string{"host:12", "0.0.0.0:12", "224.1.1.1:12", "127.0.0.1:0"} {
		if _, err = s.Heartbeat(b.Token, Heartbeat{Endpoints: []string{endpoint}}, time.Now()); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted endpoint %q: %v", endpoint, err)
		}
	}
	if !s.CanCommunicate(a.Device.ID, b.Device.ID) || s.CanCommunicate(a.Device.ID, a.Device.ID) {
		t.Fatal("peer authorization incorrect")
	}
	revoked := ""
	unsubscribe := s.SubscribeRevocations(func(id string) { revoked = id; _, _ = s.Authenticate(a.Token) })
	defer unsubscribe()
	if err = s.Revoke(b.Device.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if revoked != b.Device.ID {
		t.Fatal("revocation notification missing")
	}
	if _, err = s.Authenticate(b.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked token accepted")
	}
	if _, err = s.Heartbeat(b.Token, Heartbeat{}, time.Now()); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked heartbeat accepted")
	}
	peers, err = s.Peers(a.Token)
	if err != nil || len(peers) != 0 {
		t.Fatal("revoked peer still visible")
	}
	if s.CanCommunicate(a.Device.ID, b.Device.ID) {
		t.Fatal("revoked relay authorization")
	}
}

func request(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, reader)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHTTPAuthorizationAndEnrollment(t *testing.T) {
	s, _ := NewStore("")
	admin := strings.Repeat("a", 40)
	h, err := NewHandler(s, Config{AdminToken: admin})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/device", "/v1/peers", "/v1/devices", "/v1/events"} {
		if w := request(t, h, "GET", path, "", nil); w.Code != 401 {
			t.Fatalf("unauth %s: %d", path, w.Code)
		}
	}
	if w := request(t, h, "POST", "/v1/pairing-codes", "", nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := request(t, h, "POST", "/v1/pairing-codes", admin, nil)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var p PairingCode
	if err = json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	w = request(t, h, "POST", "/v1/enroll", "", Enrollment{ServiceID: p.ServiceID, Code: p.Code, Name: "phone", PublicKey: testKey(10)})
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var result EnrollmentResult
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("secret response cacheable")
	}
	if w = request(t, h, "POST", "/v1/pairing-codes", result.Token, nil); w.Code != 401 {
		t.Fatal("device can administer")
	}
	if w = request(t, h, "GET", "/v1/device", result.Token, nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w = request(t, h, "POST", "/v1/heartbeat", result.Token, map[string]any{"endpoints": []string{}, "unknown": "bad"}); w.Code != 400 {
		t.Fatal("unknown JSON field accepted")
	}
	if w = request(t, h, "POST", "/v1/devices/"+result.Device.ID+"/revoke", admin, nil); w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	if w = request(t, h, "GET", "/v1/peers", result.Token, nil); w.Code != 401 {
		t.Fatal("revoked HTTP token accepted")
	}
}

func TestEventStreamRefreshAndOwnRevocation(t *testing.T) {
	s, _ := NewStore("")
	a := enroll(t, s, "phone", 1)
	h, _ := NewHandler(s, Config{AdminToken: strings.Repeat("a", 40)})
	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+a.Token)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	readEvent := func() string {
		t.Helper()
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		_, _ = reader.ReadString('\n')
		_, _ = reader.ReadString('\n')
		return strings.TrimSpace(line)
	}
	if got := readEvent(); got != "event: ready" {
		t.Fatal(got)
	}
	_ = enroll(t, s, "mac", 2)
	if got := readEvent(); got != "event: peers-changed" {
		t.Fatal(got)
	}
	if err = s.Revoke(a.Device.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := readEvent(); got != "event: revoked" {
		t.Fatal(got)
	}
	if _, err = reader.ReadByte(); err != io.EOF {
		t.Fatalf("revocation stream did not close: %v", err)
	}
}

func TestFileTLSFingerprintValidationAndPublication(t *testing.T) {
	s, _ := NewStore("")
	a := enroll(t, s, "phone", 1)
	b := enroll(t, s, "mac", 2)
	want := strings.Repeat("AB", 32)
	if _, err := s.Heartbeat(b.Token, Heartbeat{FileTLSFingerprint: want}, time.Now()); err != nil {
		t.Fatal(err)
	}
	peers, err := s.Peers(a.Token)
	if err != nil || len(peers) != 1 || peers[0].FileTLSFingerprint != strings.ToLower(want) {
		t.Fatalf("fingerprint unavailable: %+v %v", peers, err)
	}
	for _, invalid := range []string{strings.Repeat("a", 63), strings.Repeat("z", 64), "-----BEGIN PRIVATE KEY-----"} {
		if _, err = s.Heartbeat(b.Token, Heartbeat{FileTLSFingerprint: invalid}, time.Now()); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted invalid fingerprint: %v", err)
		}
		p, _ := s.CreatePairing(time.Minute, time.Now())
		if _, err = s.Enroll(Enrollment{ServiceID: p.ServiceID, Code: p.Code, Name: "new", PublicKey: testKey(3), FileTLSFingerprint: invalid}, time.Now()); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid enrollment fingerprint accepted")
		}
	}
}
