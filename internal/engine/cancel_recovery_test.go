package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaojohn-eng/JunGo/internal/files"
)

func TestOfflineCancelCleanupSurvivesRestartWithoutResuming(t *testing.T) {
	sender, receiver, _ := fixture(t)
	share := t.TempDir()
	call(t, receiver, "shareAdd", map[string]any{"name": "Cancel", "path": share})
	shareID, peerID := receiver.State().Shares[0].ID, receiver.State().Device.ID
	call(t, sender, "network", map[string]bool{"mesh": true})
	call(t, receiver, "network", map[string]bool{"mesh": true})
	waitRecoveryPeer(t, sender, peerID)
	digest := sha256.Sum256([]byte("abc"))
	checksum := hex.EncodeToString(digest[:])
	var checkpoints []Transfer
	var remoteIDs []string
	var cleanupBlocker string
	for _, lostResponse := range []bool{false, true} {
		name := "known.bin"
		if lostResponse {
			name = "lost-response.bin"
		}
		local := saveRecoveryCheckpoint(t, sender, Transfer{Direction: "upload", DeviceID: peerID, ShareID: shareID, Path: name, Source: "/deliberately-missing/source", Size: 3, Hash: checksum})
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		b, err := sender.fileJSON(ctx, peerID, "POST", "/v1/files/uploads", files.BeginRequest{ShareID: shareID, Path: name, Size: 3, SHA256: checksum}, http.Header{"X-Jungo-Upload-ID": {local.ID}})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		var remote files.Upload
		if err := json.Unmarshal(b, &remote); err != nil {
			t.Fatal(err)
		}
		if !lostResponse {
			if err := sender.updateTask(local.ID, func(x *Transfer) { x.UploadID = remote.ID }); err != nil {
				t.Fatal(err)
			}
			partial := filepath.Join(receiver.dir, "files", remote.ID+".part")
			if err := os.Remove(partial); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(partial, 0700); err != nil {
				t.Fatal(err)
			}
			cleanupBlocker = filepath.Join(partial, "blocker")
			if err := os.WriteFile(cleanupBlocker, nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		remoteIDs = append(remoteIDs, remote.ID)
		checkpoints = append(checkpoints, local)
	}
	call(t, sender, "network", map[string]bool{"mesh": false})
	for _, task := range checkpoints {
		if err := sender.transferAction(task.ID, "cancel"); err != nil {
			t.Fatal(err)
		}
	}
	dir := sender.dir
	if err := sender.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	sender, err = New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sender.Close() })
	for _, task := range sender.transferSnapshot() {
		if task.Status != "cancelled" || !task.CancelRemote {
			t.Fatalf("offline cancellation lost its durable cleanup: %+v", task)
		}
	}
	call(t, sender, "network", map[string]bool{"mesh": true})
	waitRecoveryPeer(t, sender, peerID)
	wait(t, 45*time.Second, func() bool {
		return recoveryUpload(t, sender, peerID, remoteIDs[0]).State == "cancelled"
	})
	for _, task := range sender.transferSnapshot() {
		if task.ID == checkpoints[0].ID && !task.CancelRemote {
			t.Fatal("unlink failure was mistaken for completed publication")
		}
	}
	if err := os.Remove(cleanupBlocker); err != nil {
		t.Fatal(err)
	}
	sender.wakeTransfers()
	wait(t, 45*time.Second, func() bool {
		for _, task := range sender.transferSnapshot() {
			if task.Status != "cancelled" {
				t.Fatalf("cancelled task resumed: %+v", task)
			}
			if task.CancelRemote {
				return false
			}
		}
		return true
	})
	for _, id := range remoteIDs {
		if remote := recoveryUpload(t, sender, peerID, id); remote.State != "cancelled" {
			t.Fatalf("remote task was not cleaned up: %+v", remote)
		}
	}
	if entries, err := os.ReadDir(share); err != nil || len(entries) != 0 {
		t.Fatal("cancelled transfer unexpectedly published a file")
	}
}
