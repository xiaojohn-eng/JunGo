package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaojohn-eng/JunGo/internal/files"
	"github.com/xiaojohn-eng/JunGo/internal/secure"
)

// Seed only a confirmed protocol checkpoint. Both continuations then run through
// the normal queue, a newly created Engine, real WireGuard and authenticated TLS.
func TestClientRestartResumesConfirmedUploadAndDownload(t *testing.T) {
	sender, receiver, _ := fixture(t)
	share := t.TempDir()
	call(t, receiver, "shareAdd", map[string]any{"name": "Recovery", "path": share})
	shareID := receiver.State().Shares[0].ID
	peerID := receiver.State().Device.ID
	call(t, sender, "network", map[string]bool{"mesh": true})
	call(t, receiver, "network", map[string]bool{"mesh": true})
	waitRecoveryPeer(t, sender, peerID)
	defer func() {
		if t.Failed() {
			t.Logf("recovery state: %+v", sender.State())
		}
	}()

	const total = 512 << 10
	const checkpoint = 128 << 10
	payload := make([]byte, total)
	var random uint64 = 0x9e3779b97f4a7c15
	for i := range payload {
		random ^= random << 13
		random ^= random >> 7
		random ^= random << 17
		payload[i] = byte(random)
	}
	digest := sha256.Sum256(payload)
	expectedHash := hex.EncodeToString(digest[:])
	source := filepath.Join(t.TempDir(), "original.bin")
	if err := os.WriteFile(source, payload, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	b, err := sender.fileJSON(ctx, peerID, "POST", "/v1/files/uploads", files.BeginRequest{ShareID: shareID, Path: "recovered.bin", Size: total, SHA256: expectedHash}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var remote files.Upload
	if err := json.Unmarshal(b, &remote); err != nil {
		t.Fatal(err)
	}
	response, client, err := sender.fileRequest(ctx, peerID, "PATCH", "/v1/files/uploads/"+remote.ID, bytes.NewReader(payload[:checkpoint]), http.Header{"Upload-Offset": {"0"}, "Content-Type": {"application/octet-stream"}})
	if err != nil {
		t.Fatal(err)
	}
	err = json.NewDecoder(response.Body).Decode(&remote)
	response.Body.Close()
	client.CloseIdleConnections()
	if err != nil || remote.Offset != checkpoint || remote.State != "pending" {
		t.Fatalf("remote checkpoint was not confirmed: %+v, %v", remote, err)
	}
	uploadID := remote.ID
	upload := saveRecoveryCheckpoint(t, sender, Transfer{Direction: "upload", DeviceID: peerID, ShareID: shareID, Path: "recovered.bin", Source: source, Size: total, Completed: checkpoint, Hash: expectedHash, UploadID: uploadID})

	sender = restartRecoveryClient(t, sender, peerID)
	assertRecoveryCheckpoint(t, sender, upload)
	remote = recoveryUpload(t, sender, peerID, uploadID)
	if remote.Offset != checkpoint || remote.State != "pending" {
		t.Fatalf("restart changed the paused remote checkpoint: %+v", remote)
	}
	call(t, sender, "transferAction", map[string]string{"id": upload.ID, "action": "resume"})
	finished := waitRecoveryTransfer(t, sender, upload.ID)
	if finished.UploadID != uploadID || finished.Hash != expectedHash || finished.Completed != total {
		t.Fatalf("upload continuation lost its original identity/checkpoint: %+v", finished)
	}
	remote = recoveryUpload(t, sender, peerID, uploadID)
	if remote.State != "completed" || remote.Offset != total || remote.SHA256 != expectedHash {
		t.Fatalf("original remote upload did not complete: %+v", remote)
	}
	assertRecoveryFile(t, filepath.Join(share, finished.Path), total, expectedHash)

	// Download the first checkpoint through authenticated HTTPS rather than
	// copying the original locally. Preserve the server's version for If-Match.
	destination := filepath.Join(t.TempDir(), "download.bin")
	route := "/v1/files/download?share=" + url.QueryEscape(shareID) + "&path=" + url.QueryEscape(finished.Path)
	downloadCtx, stopDownload := context.WithTimeout(context.Background(), 120*time.Second)
	defer stopDownload()
	response, client, err = sender.fileRequest(downloadCtx, peerID, "HEAD", route, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	etag := response.Header.Get("ETag")
	response.Body.Close()
	client.CloseIdleConnections()
	if etag == "" {
		t.Fatal("server did not provide a file version")
	}
	response, client, err = sender.fileRequest(downloadCtx, peerID, "GET", route, nil, http.Header{"Range": {fmt.Sprintf("bytes=0-%d", checkpoint-1)}, "If-Match": {etag}})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusPartialContent || response.ContentLength != checkpoint || response.Header.Get("ETag") != etag {
		response.Body.Close()
		client.CloseIdleConnections()
		t.Fatal("server did not provide a versioned 128 KiB range")
	}
	local, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		response.Body.Close()
		client.CloseIdleConnections()
		t.Fatal(err)
	}
	count, copyErr := io.Copy(local, response.Body)
	syncErr := local.Sync()
	closeErr := local.Close()
	response.Body.Close()
	client.CloseIdleConnections()
	if copyErr != nil || syncErr != nil || closeErr != nil || count != checkpoint {
		t.Fatalf("download checkpoint not durable: bytes=%d copy=%v sync=%v close=%v", count, copyErr, syncErr, closeErr)
	}
	download := saveRecoveryCheckpoint(t, sender, Transfer{Direction: "download", DeviceID: peerID, ShareID: shareID, Path: finished.Path, Destination: destination, Size: total, Completed: checkpoint, ETag: etag})
	sender = restartRecoveryClient(t, sender, peerID)
	assertRecoveryCheckpoint(t, sender, download)
	if info, err := os.Stat(destination); err != nil || info.Size() != checkpoint {
		t.Fatalf("restart changed the paused local checkpoint: %v, %v", info, err)
	}
	call(t, sender, "transferAction", map[string]string{"id": download.ID, "action": "resume"})
	finished = waitRecoveryTransfer(t, sender, download.ID)
	if finished.Completed != total || finished.ETag != etag || finished.Hash != expectedHash {
		t.Fatalf("download continuation lost its version or checksum: %+v", finished)
	}
	assertRecoveryFile(t, destination, total, expectedHash)
}

func saveRecoveryCheckpoint(t *testing.T, e *Engine, transfer Transfer) Transfer {
	t.Helper()
	transfer.ID = secure.Random(16)
	transfer.Name = filepath.Base(transfer.Path)
	transfer.Status = "paused"
	transfer.Created = time.Now()
	transfer.Updated = transfer.Created
	e.tasksMu.Lock()
	e.tasks[transfer.ID] = &transfer
	err := e.persistTasksLocked()
	e.tasksMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return transfer
}

func restartRecoveryClient(t *testing.T, old *Engine, peerID string) *Engine {
	t.Helper()
	dir := old.dir
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { next.Close() })
	waitRecoveryPeer(t, next, peerID)
	return next
}

func waitRecoveryPeer(t *testing.T, e *Engine, peerID string) {
	t.Helper()
	wait(t, 45*time.Second, func() bool {
		for _, peer := range e.State().Peers {
			if peer.ID == peerID && peer.FileTLSFingerprint != "" {
				return true
			}
		}
		return false
	})
}

func assertRecoveryCheckpoint(t *testing.T, e *Engine, expected Transfer) {
	t.Helper()
	// Force an ordinary worker wake as well: a persisted user pause must survive
	// startup and connectivity notifications until the explicit resume action.
	e.wakeTransfers()
	for _, actual := range e.transferSnapshot() {
		if actual.ID == expected.ID {
			if actual.Status != "paused" || actual.Completed != expected.Completed || actual.UploadID != expected.UploadID || actual.ETag != expected.ETag || actual.Hash != expected.Hash {
				t.Fatalf("checkpoint not restored: got %+v, want %+v", actual, expected)
			}
			return
		}
	}
	t.Fatal("persisted task missing after Engine restart")
}

func recoveryUpload(t *testing.T, e *Engine, peerID, uploadID string) files.Upload {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	b, err := e.fileJSON(ctx, peerID, "GET", "/v1/files/uploads/"+uploadID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var upload files.Upload
	if err := json.Unmarshal(b, &upload); err != nil {
		t.Fatal(err)
	}
	return upload
}

func waitRecoveryTransfer(t *testing.T, e *Engine, id string) Transfer {
	t.Helper()
	var completed Transfer
	wait(t, 120*time.Second, func() bool {
		for _, task := range e.transferSnapshot() {
			if task.ID == id {
				if task.Status == "failed" {
					t.Fatalf("recovery failed: %s", task.Error)
				}
				completed = task
				return task.Status == "complete"
			}
		}
		return false
	})
	return completed
}

func assertRecoveryFile(t *testing.T, name string, size int64, digest string) {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil || n != size || hex.EncodeToString(h.Sum(nil)) != digest {
		t.Fatalf("recovered file content differs: bytes=%d expected=%d error=%v", n, size, err)
	}
}
