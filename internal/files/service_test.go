package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fixture struct {
	t       *testing.T
	s       *Service
	config  Config
	dir     string
	revoked atomic.Bool
}

func setup(t *testing.T, readonly bool) *fixture {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "share")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, dir: dir}
	f.config = Config{StateDir: filepath.Join(base, "state"), Shares: []Share{{ID: "docs", Name: "Documents", Path: dir, ReadOnly: readonly}}, MinFreeBytes: 1, Authorize: func(_ context.Context, r *http.Request) (string, error) {
		if f.revoked.Load() {
			return "", errors.New("revoked")
		}
		owner := r.Header.Get("X-Device")
		if owner == "" {
			owner = "device-a"
		}
		return owner, nil
	}}
	var err error
	f.s, err = New(f.config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.s.Close() })
	return f
}

func (f *fixture) request(method, target string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, r)
	return w
}
func assertStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body.String())
	}
}
func encode(t *testing.T, value any) []byte {
	t.Helper()
	result, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func digest(data []byte) string { hash := sha256.Sum256(data); return hex.EncodeToString(hash[:]) }
func (f *fixture) begin(name string, data []byte, overwrite bool) Upload {
	f.t.Helper()
	w := f.request("POST", "/v1/files/uploads", encode(f.t, BeginRequest{ShareID: "docs", Path: name, Size: int64(len(data)), SHA256: digest(data), OverwriteConfirmed: overwrite}), nil)
	assertStatus(f.t, w, 201)
	var upload Upload
	if err := json.Unmarshal(w.Body.Bytes(), &upload); err != nil {
		f.t.Fatal(err)
	}
	return upload
}
func (f *fixture) chunk(task Upload, offset int64, data []byte) *httptest.ResponseRecorder {
	return f.request("PATCH", "/v1/files/uploads/"+task.ID, data, map[string]string{"Upload-Offset": strconv.FormatInt(offset, 10)})
}
func (f *fixture) complete(task Upload) *httptest.ResponseRecorder {
	return f.request("POST", "/v1/files/uploads/"+task.ID+"/complete", nil, nil)
}
func (f *fixture) putFile(name string, data []byte) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), data, 0600); err != nil {
		f.t.Fatal(err)
	}
}

func TestSharesAndContainment(t *testing.T) {
	f := setup(t, false)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.dir, "escape")); err != nil {
		t.Fatal(err)
	}
	f.putFile("visible", []byte("ok"))
	f.putFile(stagingPrefix+"hidden", []byte("not listed"))
	w := f.request("GET", "/v1/files/shares", nil, nil)
	assertStatus(t, w, 200)
	if strings.Contains(w.Body.String(), f.dir) {
		t.Fatal("physical path leaked")
	}
	w = f.request("GET", "/v1/files/entries?share=docs", nil, nil)
	assertStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "escape") || strings.Contains(w.Body.String(), "hidden") {
		t.Fatal("symlink or staging file listed")
	}
	for _, p := range []string{"../secret", "/etc/passwd", "a/../../secret", "a\\..\\secret", stagingPrefix + "hidden", "a/" + stagingPrefix + "hidden"} {
		w = f.request("GET", "/v1/files/download?share=docs&path="+url.QueryEscape(p), nil, nil)
		assertStatus(t, w, 400)
	}
	w = f.request("GET", "/v1/files/download?share=docs&path=escape", nil, nil)
	if w.Code == 200 || strings.Contains(w.Body.String(), "secret") {
		t.Fatal("outside symlink exposed")
	}
	w = f.request("POST", "/v1/files/mkdir", []byte(`{"shareId":"docs","path":"../escape"}`), nil)
	assertStatus(t, w, 400)
	w = f.request("GET", "/v1/files/download?share=unknown&path=visible", nil, nil)
	assertStatus(t, w, 404)
}

func TestReadOnlyAndDirectories(t *testing.T) {
	f := setup(t, true)
	f.putFile("document", []byte("readable"))
	assertStatus(t, f.request("GET", "/v1/files/download?share=docs&path=document", nil, nil), 200)
	assertStatus(t, f.request("POST", "/v1/files/mkdir", []byte(`{"shareId":"docs","path":"one/two"}`), nil), 403)
	assertStatus(t, f.request("POST", "/v1/files/uploads", encode(t, BeginRequest{ShareID: "docs", Path: "x", Size: 0, SHA256: digest(nil)}), nil), 403)
	f.s.shares["docs"].ReadOnly = false
	assertStatus(t, f.request("POST", "/v1/files/mkdir", []byte(`{"shareId":"docs","path":"one/two"}`), nil), 200)
	data := []byte("nested")
	task := f.begin("one/two/x", data, false)
	assertStatus(t, f.chunk(task, 0, data), 200)
	assertStatus(t, f.complete(task), 200)
	content, err := os.ReadFile(filepath.Join(f.dir, "one/two/x"))
	if err != nil || !bytes.Equal(content, data) {
		t.Fatal("folder upload failed", err)
	}
}

func TestResumableUploadsAndDuplicateChunks(t *testing.T) {
	f := setup(t, false)
	data := bytes.Repeat([]byte("abcdef0123456789"), 20000)
	task := f.begin("large.dat", data, false)
	assertStatus(t, f.chunk(task, 0, data[:100001]), 200)
	assertStatus(t, f.chunk(task, 0, data[:100001]), 200)
	w := f.chunk(task, 0, []byte("wrong"))
	assertStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), `"expectedOffset":100001`) {
		t.Fatal("missing resume offset")
	}
	assertStatus(t, f.chunk(task, 100002, data[100001:]), 409)
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate process failure after data write but before the offset snapshot.
	part, err := os.OpenFile(filepath.Join(f.config.StateDir, task.ID+".part"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = part.Write([]byte("uncommitted tail"))
	part.Close()
	if err != nil {
		t.Fatal(err)
	}
	f.s, err = New(f.config)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(f.config.StateDir, task.ID+".part"))
	if err != nil || info.Size() != 100001 {
		t.Fatalf("tail not truncated: %v %v", info, err)
	}
	assertStatus(t, f.chunk(task, 100001, data[100001:]), 200)
	w = f.complete(task)
	assertStatus(t, w, 200)
	var result Upload
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" || result.Offset != int64(len(data)) {
		t.Fatalf("bad completion %+v", result)
	}
	assertStatus(t, f.complete(task), 200)
	assertStatus(t, f.chunk(task, int64(len(data)), []byte("x")), 409)
	actual, err := os.ReadFile(filepath.Join(f.dir, "large.dat"))
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatal("content differs", err)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = New(f.config)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, f.complete(task), 200)
}

func TestCollisionsAndExplicitOverwrite(t *testing.T) {
	f := setup(t, false)
	f.putFile("report.txt", []byte("original"))
	data := []byte("second")
	task := f.begin("report.txt", data, false)
	assertStatus(t, f.chunk(task, 0, data), 200)
	w := f.complete(task)
	assertStatus(t, w, 200)
	var result Upload
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.Path != "report (1).txt" {
		t.Fatalf("wrong collision name %q", result.Path)
	}
	original, _ := os.ReadFile(filepath.Join(f.dir, "report.txt"))
	if string(original) != "original" {
		t.Fatal("original overwritten")
	}
	data = []byte("replacement")
	task = f.begin("report.txt", data, true)
	assertStatus(t, f.chunk(task, 0, data), 200)
	assertStatus(t, f.complete(task), 200)
	actual, _ := os.ReadFile(filepath.Join(f.dir, "report.txt"))
	if !bytes.Equal(actual, data) {
		t.Fatal("explicit overwrite failed")
	}
	if err := os.Symlink("report.txt", filepath.Join(f.dir, "alias")); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, f.request("POST", "/v1/files/uploads", encode(t, BeginRequest{ShareID: "docs", Path: "alias", Size: 1, SHA256: digest([]byte("x")), OverwriteConfirmed: true}), nil), 403)
}

func TestDownloadsUseVersionedRanges(t *testing.T) {
	f := setup(t, false)
	f.putFile("data", []byte("0123456789"))
	w := f.request("GET", "/v1/files/download?share=docs&path=data", nil, nil)
	assertStatus(t, w, 200)
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no etag")
	}
	assertStatus(t, f.request("GET", "/v1/files/download?share=docs&path=data", nil, map[string]string{"Range": "bytes=3-6"}), 428)
	w = f.request("GET", "/v1/files/download?share=docs&path=data", nil, map[string]string{"Range": "bytes=3-6", "If-Match": etag})
	assertStatus(t, w, 206)
	if w.Body.String() != "3456" {
		t.Fatalf("wrong range %q", w.Body.String())
	}
	w = f.request("HEAD", "/v1/files/download?share=docs&path=data", nil, nil)
	assertStatus(t, w, 200)
	if w.Body.Len() != 0 {
		t.Fatal("HEAD has body")
	}
	f.putFile("data", []byte("different-content"))
	assertStatus(t, f.request("GET", "/v1/files/download?share=docs&path=data", nil, map[string]string{"Range": "bytes=3-6", "If-Match": etag}), 412)
	assertStatus(t, f.request("GET", "/v1/files/download?share=docs&path=data", nil, map[string]string{"Range": "bytes=3-6", "If-Match": "W/" + etag}), 412)
}

func TestOwnerIsolationAndRevocation(t *testing.T) {
	f := setup(t, false)
	data := []byte("payload")
	task := f.begin("data", data, false)
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		assertStatus(t, f.request(method, "/v1/files/uploads/"+task.ID, nil, map[string]string{"X-Device": "device-b"}), 404)
	}
	f.revoked.Store(true)
	assertStatus(t, f.request("GET", "/v1/files/shares", nil, nil), 403)
	assertStatus(t, f.chunk(task, 0, data), 403)
	f.revoked.Store(false)
	assertStatus(t, f.chunk(task, 0, data), 200)
	f.revoked.Store(true)
	assertStatus(t, f.complete(task), 403)
	if _, err := os.Stat(filepath.Join(f.dir, "data")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("revoked completed upload")
	}
}

func TestRevocationInterruptsStreams(t *testing.T) {
	f := setup(t, false)
	data := bytes.Repeat([]byte("x"), 512<<10)
	f.putFile("data", data)
	var checks atomic.Int32
	f.s.config.Authorize = func(_ context.Context, _ *http.Request) (string, error) {
		if checks.Add(1) > 3 {
			return "", errors.New("revoked")
		}
		return "device-a", nil
	}
	w := f.request("GET", "/v1/files/download?share=docs&path=data", nil, nil)
	if w.Body.Len() >= len(data) {
		t.Fatal("revoked transfer was not stopped")
	}
	if checks.Load() < 4 {
		t.Fatal("authorization not rechecked")
	}
}

func TestHashMismatchAndCancel(t *testing.T) {
	f := setup(t, false)
	task := f.begin("bad", []byte("right"), false)
	assertStatus(t, f.chunk(task, 0, []byte("wrong")), 200)
	assertStatus(t, f.complete(task), 422)
	if _, err := os.Stat(filepath.Join(f.dir, "bad")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("hash mismatch published")
	}
	assertStatus(t, f.request("DELETE", "/v1/files/uploads/"+task.ID, nil, nil), 200)
	assertStatus(t, f.request("DELETE", "/v1/files/uploads/"+task.ID, nil, nil), 200)
	assertStatus(t, f.complete(task), 409)
	assertStatus(t, f.chunk(task, 0, []byte("right")), 409)
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.s, err = New(f.config)
	if err != nil {
		t.Fatal(err)
	}
	w := f.request("GET", "/v1/files/uploads/"+task.ID, nil, nil)
	assertStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"state":"cancelled"`) {
		t.Fatal("cancellation not durable")
	}
}

func TestLimitsAndMalformedRequests(t *testing.T) {
	f := setup(t, false)
	f.s.config.MaxUploadBytes = 4
	f.s.config.MaxPendingBytes = 4
	f.s.config.MaxChunkBytes = 2
	for _, size := range []int64{-1, 5} {
		assertStatus(t, f.request("POST", "/v1/files/uploads", encode(t, BeginRequest{ShareID: "docs", Path: "x", Size: size, SHA256: digest(nil)}), nil), 413)
	}
	task := f.begin("data", []byte("1234"), false)
	assertStatus(t, f.request("POST", "/v1/files/uploads", encode(t, BeginRequest{ShareID: "docs", Path: "y", Size: 1, SHA256: digest([]byte("y"))}), nil), 507)
	assertStatus(t, f.chunk(task, 0, []byte("123")), 413)
	assertStatus(t, f.chunk(task, 0, []byte("12")), 200)
	assertStatus(t, f.chunk(task, 2, []byte("34")), 200)
	assertStatus(t, f.complete(task), 200)
	f.begin("next", []byte("x"), false)
	assertStatus(t, f.request("POST", "/v1/files/uploads", []byte(`{} {}`), nil), 400)
	f.s.config.MinFreeBytes = 1 << 62
	assertStatus(t, f.request("POST", "/v1/files/uploads", encode(t, BeginRequest{ShareID: "docs", Path: "x", Size: 0, SHA256: digest(nil)}), nil), 507)
}

func TestStateConfigIsolation(t *testing.T) {
	f := setup(t, false)
	config := f.config
	config.StateDir = filepath.Join(f.dir, "state")
	if service, err := New(config); err == nil {
		service.Close()
		t.Fatal("state allowed inside share")
	}
	config = f.config
	config.Authorize = nil
	if service, err := New(config); err == nil {
		service.Close()
		t.Fatal("missing authorizer allowed")
	}
}

func TestEmptyFileAndPublishedCrashRecovery(t *testing.T) {
	f := setup(t, false)
	task := f.begin("empty", nil, false)
	assertStatus(t, f.complete(task), 200)
	data := []byte("recover-me")
	task = f.begin("recover", data, false)
	assertStatus(t, f.chunk(task, 0, data), 200)
	// Emulate a crash immediately after exclusive publication, before the
	// completed state was written. Recovery must not publish a second copy.
	f.s.mu.Lock()
	state := f.s.tasks[task.ID]
	f.s.mu.Unlock()
	state.State = "publishing"
	state.StagePath = stagingPrefix + task.ID
	if err := f.s.save(state); err != nil {
		t.Fatal(err)
	}
	f.putFile("recover", data)
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.s, err = New(f.config)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, f.complete(task), 200)
	if _, err = os.Stat(filepath.Join(f.dir, "recover (1)")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("duplicate file after publication recovery")
	}
}

func TestExclusivePublicationPreservesExistingFilesAndSymlinks(t *testing.T) {
	f := setup(t, false)
	stage := stagingPrefix + strings.Repeat("a", 32)
	f.putFile(stage, []byte("incoming"))
	f.putFile("existing", []byte("original"))
	root := f.s.shares["docs"].root
	if err := publishExclusive(root, stage, "existing"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing destination was not protected: %v", err)
	}
	if err := os.Symlink("existing", filepath.Join(f.dir, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := publishExclusive(root, stage, "alias"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("symlink destination was not protected: %v", err)
	}
	original, err := os.ReadFile(filepath.Join(f.dir, "existing"))
	if err != nil || string(original) != "original" {
		t.Fatal("existing content changed", err)
	}
	if err := publishExclusive(root, stage, "received"); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(f.dir, "received"))
	if err != nil || string(actual) != "incoming" {
		t.Fatal("published content differs", err)
	}
}

func TestExclusivePublicationHasOneWinnerUnderContention(t *testing.T) {
	f := setup(t, false)
	root := f.s.shares["docs"].root
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		id, err := randomID()
		if err != nil {
			t.Fatal(err)
		}
		stage := stagingPrefix + id
		f.putFile(stage, []byte(id))
		wg.Add(1)
		go func(stage string) {
			defer wg.Done()
			err := publishExclusive(root, stage, "winner")
			if err == nil {
				winners.Add(1)
			} else if !errors.Is(err, os.ErrExist) {
				t.Errorf("unexpected publication error: %v", err)
			}
		}(stage)
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("atomic publication had %d winners", winners.Load())
	}
	actual, err := os.ReadFile(filepath.Join(f.dir, "winner"))
	if err != nil || !validUploadID(string(actual)) {
		t.Fatal("publication exposed incomplete contents", err)
	}
}

func TestSourceIdentityChangesOnReplacement(t *testing.T) {
	f := setup(t, false)
	f.putFile("data", []byte("same"))
	info, err := os.Stat(filepath.Join(f.dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	w := f.request("HEAD", "/v1/files/download?share=docs&path=data", nil, nil)
	old := w.Header().Get("ETag")
	f.putFile("replacement", []byte("same"))
	if err = os.Chtimes(filepath.Join(f.dir, "replacement"), time.Now(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(f.dir, "replacement"), filepath.Join(f.dir, "data")); err != nil {
		t.Fatal(err)
	}
	w = f.request("HEAD", "/v1/files/download?share=docs&path=data", nil, nil)
	if w.Header().Get("ETag") == old {
		t.Fatal("file identity missing from ETag")
	}
}

func TestHTTPServerTransfer(t *testing.T) {
	f := setup(t, false)
	server := httptest.NewServer(f.s.Handler())
	defer server.Close()
	data := bytes.Repeat([]byte("network-stream"), 10000)
	f.putFile("download", data)
	response, err := http.Get(server.URL + "/v1/files/download?share=docs&path=download")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, response.Body)
	if err != nil || n != int64(len(data)) || hex.EncodeToString(hash.Sum(nil)) != digest(data) {
		t.Fatal("HTTP streaming mismatch", err)
	}
}

func TestConcurrentPublishKeepsBoth(t *testing.T) {
	f := setup(t, false)
	const count = 8
	tasks := make([]Upload, count)
	for i := range tasks {
		data := []byte(strconv.Itoa(i))
		tasks[i] = f.begin("same.txt", data, false)
		assertStatus(t, f.chunk(tasks[i], 0, data), 200)
	}
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, count)
	for _, task := range tasks {
		wg.Add(1)
		go func(task Upload) { defer wg.Done(); results <- f.complete(task) }(task)
	}
	wg.Wait()
	close(results)
	names := map[string]bool{}
	for response := range results {
		assertStatus(t, response, 200)
		var result Upload
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if names[result.Path] {
			t.Fatal("duplicate destination", result.Path)
		}
		names[result.Path] = true
	}
	entries, err := os.ReadDir(f.dir)
	if err != nil || len(entries) != count {
		t.Fatalf("published %d entries: %v", len(entries), err)
	}
}

type interruptedReader struct{ done bool }

func (r *interruptedReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.ErrUnexpectedEOF
	}
	r.done = true
	return copy(p, "abc"), nil
}

func TestInterruptedChunkDoesNotAdvance(t *testing.T) {
	f := setup(t, false)
	task := f.begin("data", []byte("abcdef"), false)
	request := httptest.NewRequest("PATCH", "/v1/files/uploads/"+task.ID, &interruptedReader{})
	request.Header.Set("Upload-Offset", "0")
	response := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(response, request)
	if response.Code == 200 {
		t.Fatal("incomplete chunk acknowledged")
	}
	status := f.request("GET", "/v1/files/uploads/"+task.ID, nil, nil)
	var state Upload
	if err := json.Unmarshal(status.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Offset != 0 {
		t.Fatal("interrupted chunk advanced offset")
	}
	assertStatus(t, f.chunk(task, 0, []byte("abcdef")), 200)
	assertStatus(t, f.complete(task), 200)
}

func TestStateDirectoryExclusiveLock(t *testing.T) {
	f := setup(t, false)
	if second, err := New(f.config); err == nil {
		second.Close()
		t.Fatal("concurrent service allowed same state")
	}
	assertStatus(t, f.request("GET", "/v1/files/shares", nil, nil), 200)
}

func TestSnapshotFailureDoesNotAcknowledgeProgressOrCancel(t *testing.T) {
	f := setup(t, false)
	data := []byte("abcdef")
	task := f.begin("data", data, false)
	// An unusable temporary snapshot name deterministically simulates a
	// metadata write failure while leaving the last snapshot readable.
	blocked := filepath.Join(f.config.StateDir, task.ID+".json.new")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	response := f.chunk(task, 0, data[:3])
	if response.Code == 200 {
		t.Fatal("failed snapshot acknowledged")
	}
	response = f.request("GET", "/v1/files/uploads/"+task.ID, nil, nil)
	var state Upload
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Offset != 0 {
		t.Fatal("undurable offset exposed")
	}
	response = f.request("DELETE", "/v1/files/uploads/"+task.ID, nil, nil)
	if response.Code == 200 {
		t.Fatal("failed cancellation acknowledged")
	}
	response = f.request("GET", "/v1/files/uploads/"+task.ID, nil, nil)
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.State != "pending" {
		t.Fatal("undurable cancellation exposed")
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, f.chunk(task, 0, data), 200)
	assertStatus(t, f.complete(task), 200)
}

func TestChecksumMatchesDownloadVersion(t *testing.T) {
	f := setup(t, false)
	data := bytes.Repeat([]byte("checksum-data"), 20000)
	f.putFile("data", data)
	head := f.request("HEAD", "/v1/files/download?share=docs&path=data", nil, nil)
	etag := head.Header().Get("ETag")
	response := f.request("GET", "/v1/files/checksum?share=docs&path=data", nil, map[string]string{"If-Match": etag})
	assertStatus(t, response, 200)
	var result struct {
		SHA256  string `json:"sha256"`
		Size    int64  `json:"size"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SHA256 != digest(data) || result.Size != int64(len(data)) || result.Version != etag {
		t.Fatalf("wrong checksum: %+v", result)
	}
	f.putFile("data", []byte("changed"))
	assertStatus(t, f.request("GET", "/v1/files/checksum?share=docs&path=data", nil, map[string]string{"If-Match": etag}), 412)
}

func TestChecksumRejectsMutationAndRevocation(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(strconv.FormatBool(revoke), func(t *testing.T) {
			f := setup(t, false)
			f.putFile("data", bytes.Repeat([]byte("x"), 512<<10))
			var checks atomic.Int32
			f.s.config.Authorize = func(_ context.Context, _ *http.Request) (string, error) {
				if checks.Add(1) == 3 {
					if revoke {
						return "", errors.New("revoked")
					}
					f.putFile("data", []byte("mutated during checksum"))
				}
				return "device-a", nil
			}
			response := f.request("GET", "/v1/files/checksum?share=docs&path=data", nil, nil)
			if revoke {
				assertStatus(t, response, 403)
			} else {
				assertStatus(t, response, 412)
			}
		})
	}
}
