package engine

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func renameRequest(t *testing.T, e *Engine, name string) error {
	t.Helper()
	b, err := json.Marshal(map[string]any{"method": "renameDevice", "params": map[string]string{"name": name}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Request(string(b))
	return err
}

func TestRenameDeviceUpdatesRegistryAndLocalNameOnly(t *testing.T) {
	mac, peer, store := fixture(t)
	before := mac.config()
	if err := renameRequest(t, mac, "Renamed Mac"); err != nil {
		t.Fatal(err)
	}
	after := mac.config()
	if after.Device.Name != "Renamed Mac" || mac.State().Device.Name != "Renamed Mac" {
		t.Fatal("renamed device is not visible in local state")
	}
	remote, err := store.Authenticate(before.Token)
	if err != nil || remote.Name != "Renamed Mac" {
		t.Fatalf("controller did not persist the new name: %v", err)
	}
	peers, err := store.Peers(peer.config().Token)
	if err != nil || len(peers) != 1 || peers[0].Name != "Renamed Mac" {
		t.Fatalf("peer did not receive the new name: %v", err)
	}
	// A display-name change must preserve all identity, network and share state.
	after.Device.Name = before.Device.Name
	if !reflect.DeepEqual(after, before) {
		t.Fatal("rename changed a non-name configuration field")
	}
	var persisted Config
	b, err := os.ReadFile(filepath.Join(mac.dir, "device.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &persisted); err != nil || persisted.Device.Name != "Renamed Mac" {
		t.Fatalf("renamed device was not saved locally: %v", err)
	}
}

func TestRenameDeviceServerFailureLeavesLocalStateUntouched(t *testing.T) {
	mac, _, store := fixture(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/device/name" {
				http.Error(w, "controller temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	before := mac.config()
	if err := renameRequest(t, mac, "Renamed Mac"); err == nil {
		t.Fatal("failed controller request appeared successful")
	}
	if !reflect.DeepEqual(mac.config(), before) {
		t.Fatal("failed controller request changed local config")
	}
	remote, err := store.Authenticate(before.Token)
	if err != nil || remote.Name != before.Device.Name {
		t.Fatalf("failed controller request changed remote name: %v", err)
	}
	for _, invalid := range []string{"", " trailing ", "line\nfeed", strings.Repeat("a", 129)} {
		if err := renameRequest(t, mac, invalid); err == nil {
			t.Fatalf("invalid name %q was accepted", invalid)
		}
	}
}

func TestRenameDeviceRecoversAfterServerSuccessAndLocalCommitFailure(t *testing.T) {
	mac, _, store := fixture(t)
	before := mac.config()
	statePath := filepath.Join(mac.dir, "device.json")
	backup := statePath + ".backup"
	if err := os.Rename(statePath, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0700); err != nil {
		t.Fatal(err)
	}
	blocked := true
	restore := func() {
		if !blocked {
			return
		}
		if err := os.Remove(statePath); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(backup, statePath); err != nil {
			t.Fatal(err)
		}
		blocked = false
	}
	t.Cleanup(restore)
	if err := renameRequest(t, mac, "Renamed Mac"); err == nil {
		t.Fatal("local commit failure appeared successful")
	}
	if mac.State().Device.Name != before.Device.Name {
		t.Fatal("failed local commit changed in-memory name")
	}
	remote, err := store.Authenticate(before.Token)
	if err != nil || remote.Name != "Renamed Mac" {
		t.Fatalf("controller update did not precede local commit: %v", err)
	}
	restore()
	call(t, mac, "network", map[string]bool{"mesh": true})
	wait(t, 10*time.Second, func() bool { return mac.State().Device.Name == "Renamed Mac" })
	after := mac.config()
	after.Device.Name = before.Device.Name
	after.MeshEnabled = before.MeshEnabled // network request explicitly enabled mesh.
	if !reflect.DeepEqual(after, before) {
		t.Fatal("recovery changed a non-name configuration field")
	}
}
