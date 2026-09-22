package files

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

type revokeOnRead struct {
	io.Reader
	revoke func()
}

func (r revokeOnRead) Read(p []byte) (int, error) {
	r.revoke()
	return r.Reader.Read(p)
}

func TestRevocationWhileReadingMetadataPreventsWrites(t *testing.T) {
	for _, endpoint := range []string{"mkdir", "uploads"} {
		t.Run(endpoint, func(t *testing.T) {
			f := setup(t, false)
			body := encode(t, BeginRequest{ShareID: "docs", Path: "revoked.bin", SHA256: digest(nil)})
			if endpoint == "mkdir" {
				body = []byte(`{"shareId":"docs","path":"revoked"}`)
			}
			r := httptest.NewRequest("POST", "/v1/files/"+endpoint, revokeOnRead{Reader: bytes.NewReader(body), revoke: func() { f.revoked.Store(true) }})
			w := httptest.NewRecorder()
			f.s.Handler().ServeHTTP(w, r)
			assertStatus(t, w, 403)
			if len(f.s.tasks) != 0 {
				t.Fatal("revoked metadata request created an upload")
			}
			if entries, err := os.ReadDir(f.dir); err != nil || len(entries) != 0 {
				t.Fatal("revoked metadata request mutated a share")
			}
		})
	}
}

func TestUploadCreationRetryIsDurableAndOwnerScoped(t *testing.T) {
	f := setup(t, false)
	body := BeginRequest{ShareID: "docs", Path: "retry.bin", Size: 3, SHA256: digest([]byte("abc"))}
	headers := map[string]string{"X-Jungo-Upload-ID": "local-task-1"}
	w := f.request("POST", "/v1/files/uploads", encode(t, body), headers)
	assertStatus(t, w, 201)
	var original Upload
	if err := json.Unmarshal(w.Body.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, f.chunk(original, 0, []byte("abc")), 200)
	assertStatus(t, f.complete(original), 200)
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.s, err = New(f.config)
	if err != nil {
		t.Fatal(err)
	}
	// Completed retries must not reserve new space or create a second task.
	f.s.config.MinFreeBytes = 1 << 62
	w = f.request("POST", "/v1/files/uploads", encode(t, body), headers)
	assertStatus(t, w, 200)
	var retry Upload
	if err := json.Unmarshal(w.Body.Bytes(), &retry); err != nil || retry.ID != original.ID || retry.State != "completed" {
		t.Fatalf("creation retry lost task identity: %+v, %v", retry, err)
	}
	if len(f.s.tasks) != 1 || f.s.reserved != 0 {
		t.Fatal("retry reserved a duplicate task")
	}
	body.Path = "different.bin"
	assertStatus(t, f.request("POST", "/v1/files/uploads", encode(t, body), headers), 409)
	body.Path = "retry.bin"
	f.s.config.MinFreeBytes = 1
	headers["X-Device"] = "device-b"
	w = f.request("POST", "/v1/files/uploads", encode(t, body), headers)
	assertStatus(t, w, 201)
	if err := json.Unmarshal(w.Body.Bytes(), &retry); err != nil || retry.ID == original.ID {
		t.Fatal("request key exposed another device's task")
	}
}

func TestCrashBeforePublicationKeepsIdenticalExistingFile(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "versioned", true: "legacy-stage"}[legacy], func(t *testing.T) {
			f := setup(t, false)
			data := []byte("same contents still require two filenames")
			f.putFile("same.bin", data)
			original, err := os.Stat(filepath.Join(f.dir, "same.bin"))
			if err != nil {
				t.Fatal(err)
			}
			task := f.begin("same.bin", data, false)
			assertStatus(t, f.chunk(task, 0, data), 200)
			state := f.s.tasks[task.ID]
			state.State, state.StagePath = "publishing", stagingPrefix+task.ID
			f.putFile(state.StagePath, data)
			stage, err := os.Stat(filepath.Join(f.dir, state.StagePath))
			if err != nil {
				t.Fatal(err)
			}
			if !legacy {
				state.PublicationVersion = version(stage)
			}
			if err := f.s.save(state); err != nil {
				t.Fatal(err)
			}
			if err := f.s.Close(); err != nil {
				t.Fatal(err)
			}
			f.s, err = New(f.config)
			if err != nil {
				t.Fatal(err)
			}
			w := f.complete(task)
			assertStatus(t, w, 200)
			var completed Upload
			if err := json.Unmarshal(w.Body.Bytes(), &completed); err != nil || completed.Path != "same (1).bin" {
				t.Fatalf("existing identical file swallowed new upload: %+v, %v", completed, err)
			}
			now, err := os.Stat(filepath.Join(f.dir, "same.bin"))
			if err != nil || !os.SameFile(original, now) {
				t.Fatal("existing file was replaced")
			}
		})
	}
}

func TestVersionedPublishedCrashRecoveryDoesNotDuplicate(t *testing.T) {
	f := setup(t, false)
	data := []byte("durably published")
	task := f.begin("published.bin", data, false)
	assertStatus(t, f.chunk(task, 0, data), 200)
	state := f.s.tasks[task.ID]
	state.State, state.StagePath = "publishing", stagingPrefix+task.ID
	f.putFile(state.StagePath, data)
	info, err := os.Stat(filepath.Join(f.dir, state.StagePath))
	if err != nil {
		t.Fatal(err)
	}
	state.PublicationVersion = version(info)
	if err := f.s.save(state); err != nil {
		t.Fatal(err)
	}
	if err := publishExclusive(f.s.shares["docs"].root, state.StagePath, state.Path); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, f.complete(task), 200)
	if _, err := os.Stat(filepath.Join(f.dir, "published (1).bin")); !os.IsNotExist(err) {
		t.Fatal("recovered publication created an extra copy")
	}
}

func TestLongUnicodeFilenameCollisionKeepsBothFiles(t *testing.T) {
	f := setup(t, false)
	name := strings.Repeat("文", 83) + ".txt" // 253 bytes, valid before adding a suffix.
	f.putFile(name, []byte("original"))
	incoming := []byte("incoming")
	task := f.begin(name, incoming, false)
	assertStatus(t, f.chunk(task, 0, incoming), 200)
	w := f.complete(task)
	assertStatus(t, w, 200)
	var completed Upload
	if err := json.Unmarshal(w.Body.Bytes(), &completed); err != nil {
		t.Fatal(err)
	}
	if completed.Path == name || len(completed.Path) > 255 || !utf8.ValidString(completed.Path) || !strings.HasSuffix(completed.Path, " (1).txt") {
		t.Fatalf("invalid collision name %q", completed.Path)
	}
	if original, err := os.ReadFile(filepath.Join(f.dir, name)); err != nil || string(original) != "original" {
		t.Fatal("original long filename was not preserved")
	}
	if actual, err := os.ReadFile(filepath.Join(f.dir, completed.Path)); err != nil || string(actual) != string(incoming) {
		t.Fatal("incoming long filename was not published")
	}
}

func TestCancelUploadWhoseCreationResponseWasLost(t *testing.T) {
	f := setup(t, false)
	body := BeginRequest{ShareID: "docs", Path: "retry.bin", Size: 3, SHA256: digest([]byte("abc"))}
	headers := map[string]string{"X-Jungo-Upload-ID": "client-task"}
	w := f.request("POST", "/v1/files/uploads", encode(t, body), headers)
	assertStatus(t, w, 201)
	var original Upload
	if err := json.Unmarshal(w.Body.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, f.request("DELETE", "/v1/files/uploads/by-request/client-task", nil, map[string]string{"X-Device": "device-b"}), 404)
	w = f.request("DELETE", "/v1/files/uploads/by-request/client-task", nil, nil)
	assertStatus(t, w, 200)
	var cancelled Upload
	if err := json.Unmarshal(w.Body.Bytes(), &cancelled); err != nil || cancelled.State != "cancelled" || cancelled.ID != original.ID {
		t.Fatalf("lost-response cancellation did not target the original upload: %+v", cancelled)
	}
	if f.s.reserved != 0 {
		t.Fatal("cancelled upload still reserved quota")
	}
	if _, err := os.Stat(filepath.Join(f.config.StateDir, original.ID+".part")); !os.IsNotExist(err) {
		t.Fatal("cancelled upload retained partial bytes")
	}
	// A delayed/retried creation is a replay of the cancellation, not a restart.
	w = f.request("POST", "/v1/files/uploads", encode(t, body), headers)
	assertStatus(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &cancelled); err != nil || cancelled.State != "cancelled" {
		t.Fatal("cancelled creation retry restarted its upload")
	}
}

func TestRepeatedCancelRetriesFailedPartialCleanup(t *testing.T) {
	f := setup(t, false)
	task := f.begin("cleanup.bin", []byte("abc"), false)
	partial := filepath.Join(f.config.StateDir, task.ID+".part")
	if err := os.Remove(partial); err != nil {
		t.Fatal(err)
	}
	// A nonempty directory produces a deterministic unlink failure on both
	// macOS and Linux, including when the test user has elevated privileges.
	if err := os.Mkdir(partial, 0700); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(partial, "blocker")
	if err := os.WriteFile(blocker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, f.request("DELETE", "/v1/files/uploads/"+task.ID, nil, nil), 409)
	if f.s.tasks[task.ID].State != "cancelled" || f.s.reserved != 0 {
		t.Fatal("failed cleanup lost cancellation or retained quota")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, f.request("DELETE", "/v1/files/uploads/"+task.ID, nil, nil), 200)
	if _, err := os.Stat(partial); !os.IsNotExist(err) || f.s.reserved != 0 {
		t.Fatal("repeated cancellation did not retry cleanup safely")
	}
}
