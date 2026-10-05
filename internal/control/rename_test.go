package control

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRenameDeviceHTTPPersistenceAndPeerNotification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	a := enroll(t, s, "Old Mac", 21)
	b := enroll(t, s, "Phone", 22)
	admin := strings.Repeat("a", 40)
	h, err := NewHandler(s, Config{AdminToken: admin})
	if err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := s.SubscribeEvents()
	defer unsubscribe()

	for _, token := range []string{"", "wrong device token", admin} {
		if w := request(t, h, "POST", "/v1/device/name", token, map[string]string{"name": "Intruder"}); w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized token status: %d", w.Code)
		}
	}
	for _, body := range []any{
		map[string]string{"name": ""},
		map[string]string{"name": "   "},
		map[string]string{"name": " Mac"},
		map[string]string{"name": "Mac "},
		map[string]string{"name": "Mac\nmini"},
		map[string]string{"name": "Mac\tmini"},
		map[string]string{"name": "Mac\u200dmini"},
		map[string]string{"name": strings.Repeat("a", 129)},
		map[string]string{"name": "Mac", "device_id": b.Device.ID},
	} {
		if w := request(t, h, "POST", "/v1/device/name", a.Token, body); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid name/body %v status: %d", body, w.Code)
		}
	}
	select {
	case event := <-events:
		t.Fatalf("rejected rename emitted event: %+v", event)
	default:
	}

	w := request(t, h, "POST", "/v1/device/name", a.Token, map[string]string{"name": "Renamed Mac"})
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	var renamed Device
	if err := json.Unmarshal(w.Body.Bytes(), &renamed); err != nil {
		t.Fatal(err)
	}
	want := a.Device
	want.Name = "Renamed Mac"
	if !reflect.DeepEqual(renamed, want) {
		t.Fatalf("rename altered immutable fields: got %+v want %+v", renamed, want)
	}
	select {
	case event := <-events:
		if event.Type != "peers-changed" || event.DeviceID != a.Device.ID {
			t.Fatalf("unexpected rename event: %+v", event)
		}
	default:
		t.Fatal("rename did not notify peers")
	}

	if w = request(t, h, "GET", "/v1/device", a.Token, nil); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	var self Device
	if err := json.Unmarshal(w.Body.Bytes(), &self); err != nil || !reflect.DeepEqual(self, want) {
		t.Fatalf("GET /v1/device: %+v %v", self, err)
	}
	if w = request(t, h, "GET", "/v1/peers", b.Token, nil); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	var peers struct {
		Peers []Device `json:"peers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &peers); err != nil || len(peers.Peers) != 1 || !reflect.DeepEqual(peers.Peers[0], want) {
		t.Fatalf("peer rename unavailable: %+v %v", peers, err)
	}
	other, err := s.Authenticate(b.Token)
	if err != nil || !reflect.DeepEqual(other, b.Device) {
		t.Fatalf("renamed another device: %+v %v", other, err)
	}
	reopened, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.Authenticate(a.Token)
	if err != nil || !reflect.DeepEqual(persisted, want) {
		t.Fatalf("rename not durable: %+v %v", persisted, err)
	}
	if w = request(t, h, "POST", "/v1/device/name", a.Token, map[string]string{"name": want.Name}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	select {
	case event := <-events:
		t.Fatalf("unchanged name emitted event: %+v", event)
	default:
	}
}

func TestRenameDeviceWriteFailureRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	a := enroll(t, s, "Old Mac", 23)
	h, err := NewHandler(s, Config{AdminToken: strings.Repeat("a", 40)})
	if err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := s.SubscribeEvents()
	defer unsubscribe()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(path, "cannot-write.json")
	w := request(t, h, "POST", "/v1/device/name", a.Token, map[string]string{"name": "Renamed Mac"})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("failed write status: %d", w.Code)
	}
	after, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed write changed registry file: %v", err)
	}
	d, err := s.Authenticate(a.Token)
	if err != nil || !reflect.DeepEqual(d, a.Device) {
		t.Fatalf("failed write changed memory: %+v %v", d, err)
	}
	select {
	case event := <-events:
		t.Fatalf("failed write emitted event: %+v", event)
	default:
	}
	if _, err := s.RenameDevice("invalid token", "Renamed Mac"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("invalid token accepted: %v", err)
	}
}

func TestRenameNameValidationBoundaries(t *testing.T) {
	for _, name := range []string{"Renamed Mac", "南京服务器", strings.Repeat("a", 128)} {
		if !validRenameName(name) {
			t.Fatalf("valid name rejected: %q", name)
		}
	}
	for _, name := range []string{"", " ", " x", "x ", "x\n", "x\tx", "x\u200dx", strings.Repeat("a", 129), string([]byte{0xff})} {
		if validRenameName(name) {
			t.Fatalf("invalid name accepted: %q", name)
		}
	}
}
