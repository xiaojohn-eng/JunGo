package files

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

func New(config Config) (_ *Service, err error) {
	if config.Authorize == nil {
		return nil, errors.New("files: Authorize is required")
	}
	if !filepath.IsAbs(config.StateDir) {
		return nil, errors.New("files: StateDir must be absolute")
	}
	if config.MaxUploadBytes < 0 || config.MaxPendingBytes < 0 || config.MaxChunkBytes < 0 || config.MinFreeBytes < 0 {
		return nil, errors.New("files: limits cannot be negative")
	}
	if config.MaxUploadBytes == 0 {
		config.MaxUploadBytes = 1 << 40
	}
	if config.MaxPendingBytes == 0 {
		config.MaxPendingBytes = 2 << 40
	}
	if config.MaxChunkBytes == 0 {
		config.MaxChunkBytes = DefaultMaxChunkBytes
	}
	if config.MaxChunkBytes > 1<<30 {
		return nil, errors.New("files: chunk limit exceeds 1 GiB")
	}
	if config.MinFreeBytes == 0 {
		config.MinFreeBytes = 64 << 20
	}
	s := &Service{config: config, shares: make(map[string]*openShare), tasks: make(map[string]*uploadTask), requests: make(map[uploadRequestKey]*uploadTask)}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()
	statePath, err := canonicalFuturePath(config.StateDir)
	if err != nil {
		return nil, err
	}
	for _, share := range config.Shares {
		if !validID(share.ID) || share.Name == "" || !filepath.IsAbs(share.Path) {
			return nil, errors.New("files: share needs valid ID, name and absolute path")
		}
		if s.shares[share.ID] != nil {
			return nil, errors.New("files: duplicate share ID")
		}
		sharePath, e := filepath.EvalSymlinks(share.Path)
		if e != nil {
			return nil, fmt.Errorf("files: share %s: %w", share.ID, e)
		}
		if beneath(sharePath, statePath) || beneath(statePath, sharePath) {
			return nil, errors.New("files: share and state directory must not contain one another")
		}
		for _, existing := range s.shares {
			if beneath(existing.Path, sharePath) || beneath(sharePath, existing.Path) {
				return nil, errors.New("files: overlapping shares are not allowed")
			}
		}
		root, e := os.OpenRoot(sharePath)
		if e != nil {
			return nil, e
		}
		share.Path = sharePath
		s.shares[share.ID] = &openShare{Share: share, root: root}
	}
	if err = os.MkdirAll(statePath, 0700); err != nil {
		return nil, fmt.Errorf("files: state directory: %w", err)
	}
	if err = os.Chmod(statePath, 0700); err != nil {
		return nil, err
	}
	s.state, err = os.OpenRoot(statePath)
	if err != nil {
		return nil, err
	}
	s.lockFile, err = s.state.OpenFile(".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockState(s.lockFile); err != nil {
		return nil, errors.New("files: state directory is already in use or cannot be locked")
	}
	f, err := s.state.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !validUploadID(id) || entry.Type()&os.ModeSymlink != 0 {
			return nil, errors.New("files: invalid upload metadata")
		}
		data, e := s.state.ReadFile(entry.Name())
		if e != nil {
			return nil, e
		}
		task := &uploadTask{}
		if e = json.Unmarshal(data, &task.diskUpload); e != nil {
			return nil, errors.New("files: corrupt upload metadata")
		}
		if task.ID != id || task.OwnerID == "" || task.Size < 0 || task.Offset < 0 || task.Offset > task.Size || !validDigest(task.SHA256) || !validPath(task.OriginalPath, false) || !validPath(task.Path, false) {
			return nil, errors.New("files: invalid persisted upload")
		}
		if task.StagePath != "" && (path.Dir(task.StagePath) != path.Dir(task.OriginalPath) || path.Base(task.StagePath) != stagingPrefix+id) {
			return nil, errors.New("files: invalid persisted staging path")
		}
		switch task.State {
		case "pending", "publishing":
			// A removed share makes the task unavailable without exposing or deleting it.
			part, e := s.state.OpenFile(id+".part", os.O_RDWR, 0600)
			if e != nil {
				return nil, fmt.Errorf("files: missing upload contents: %w", e)
			}
			info, e := part.Stat()
			if e == nil && (!info.Mode().IsRegular() || info.Size() < task.Offset) {
				e = errors.New("files: incomplete durable upload")
			}
			if e == nil {
				e = part.Truncate(task.Offset)
			}
			part.Close()
			if e != nil {
				return nil, e
			}
			if task.Size > (1<<63-1)-s.reserved {
				return nil, errors.New("files: reserved-byte overflow")
			}
			s.reserved += task.Size
			if task.State == "pending" {
				if share := s.shares[task.ShareID]; share != nil {
					_ = share.root.Remove(path.Join(path.Dir(task.OriginalPath), stagingPrefix+id))
				}
			}
		case "completed", "cancelled":
			_ = s.state.Remove(id + ".part")
			if share := s.shares[task.ShareID]; share != nil && task.StagePath != "" {
				_ = share.root.Remove(task.StagePath)
			}
		default:
			return nil, errors.New("files: unknown upload state")
		}
		if task.RequestID != "" {
			key := uploadRequestKey{task.OwnerID, task.RequestID}
			if !validID(task.RequestID) || s.requests[key] != nil {
				return nil, errors.New("files: conflicting persisted upload request")
			}
			s.requests[key] = task
		}
		s.tasks[id] = task
	}
	// A process may stop while creating a task, before its first snapshot, or
	// while writing an atomic snapshot. Only our own recognized temp names are
	// collected; arbitrary operator files are untouched.
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".json.new") && validUploadID(strings.TrimSuffix(name, ".json.new")) {
			_ = s.state.Remove(name)
		}
		if strings.HasSuffix(name, ".part") {
			id := strings.TrimSuffix(name, ".part")
			if validUploadID(id) && s.tasks[id] == nil {
				_ = s.state.Remove(name)
			}
		}
	}
	return s, nil
}

// Resolve existing symlinked parents before creating anything, so an invalid
// overlapping state directory cannot change share permissions or contents.
func canonicalFuturePath(name string) (string, error) {
	resolved, err := filepath.EvalSymlinks(name)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(name)
	if parent == name {
		return "", err
	}
	resolved, err = canonicalFuturePath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(name)), nil
}

func beneath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 80 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func validUploadID(id string) bool { b, e := hex.DecodeString(id); return e == nil && len(b) == 16 }
func validDigest(digest string) bool {
	b, e := hex.DecodeString(digest)
	return e == nil && len(b) == 32
}
func validPath(value string, allowRoot bool) bool {
	if value == "" || value == "." {
		return allowRoot
	}
	if len(value) > 4096 || strings.ContainsAny(value, "\\\x00") || strings.HasPrefix(value, "/") {
		return false
	}
	for _, p := range strings.Split(value, "/") {
		if p == "" || p == "." || p == ".." || strings.HasPrefix(p, stagingPrefix) || strings.Contains(p, ":") {
			return false
		}
	}
	return true
}

func (s *Service) Close() error {
	var result error
	s.closeOnce.Do(func() {
		for _, share := range s.shares {
			result = errors.Join(result, share.root.Close())
		}
		if s.state != nil {
			result = errors.Join(result, s.state.Close())
		}
		if s.lockFile != nil {
			result = errors.Join(result, s.lockFile.Close())
		}
	})
	return result
}

func (s *Service) save(task *uploadTask) error {
	data, err := json.Marshal(task.diskUpload)
	if err != nil {
		return err
	}
	tmp := task.ID + ".json.new"
	f, err := s.state.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		_ = s.state.Remove(tmp)
		return err
	}
	if err = s.state.Rename(tmp, task.ID+".json"); err != nil {
		return err
	}
	return syncDirectory(s.state, ".")
}

func syncDirectory(root *os.Root, p string) error {
	f, err := root.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func randomID() (string, error) {
	b := make([]byte, 16)
	_, e := rand.Read(b)
	return hex.EncodeToString(b), e
}
func version(info os.FileInfo) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", fileIdentity(info), info.Size(), info.ModTime().UnixNano())))
	return `"` + hex.EncodeToString(digest[:]) + `"`
}

type apiError struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	ExpectedOffset *int64 `json:"expectedOffset,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": apiError{Code: code, Message: message}})
}
func offsetConflict(w http.ResponseWriter, offset int64) {
	writeJSON(w, 409, map[string]any{"error": apiError{Code: "offset_mismatch", Message: "Resume from the acknowledged offset", ExpectedOffset: &offset}})
}
func fileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUnauthorized):
		fail(w, 403, "unauthorized", "Device access denied")
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT), errors.Is(err, errSpace):
		fail(w, 507, "insufficient_storage", "Not enough available storage")
	case errors.Is(err, fs.ErrNotExist):
		fail(w, 404, "not_found", "File or directory not found")
	case errors.Is(err, fs.ErrExist):
		fail(w, 409, "already_exists", "Destination already exists")
	case errors.Is(err, fs.ErrPermission):
		fail(w, 403, "permission_denied", "File access denied")
	case errors.Is(err, fs.ErrInvalid), errors.Is(err, syscall.ENOTDIR), errors.Is(err, syscall.EISDIR):
		fail(w, 400, "invalid_path", "Invalid file or directory path")
	default:
		fail(w, 500, "file_error", "File operation failed; check device storage and retry")
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "invalid_json", "Invalid request JSON")
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		fail(w, 400, "invalid_json", "Expected one JSON value")
		return false
	}
	return true
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	owner, err := s.config.Authorize(r.Context(), r)
	if err != nil || owner == "" {
		fail(w, 403, "unauthorized", "Device access denied")
		return
	}
	switch {
	case r.URL.Path == "/v1/files/shares" && r.Method == http.MethodGet:
		s.listShares(w)
	case r.URL.Path == "/v1/files/entries" && r.Method == http.MethodGet:
		s.entries(w, r)
	case r.URL.Path == "/v1/files/mkdir" && r.Method == http.MethodPost:
		s.mkdir(w, r, owner)
	case r.URL.Path == "/v1/files/download" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		s.download(w, r, owner)
	case r.URL.Path == "/v1/files/checksum" && r.Method == http.MethodGet:
		s.checksum(w, r, owner)
	case r.URL.Path == "/v1/files/uploads" && r.Method == http.MethodPost:
		s.begin(w, r, owner)
	case strings.HasPrefix(r.URL.Path, "/v1/files/uploads/"):
		s.upload(w, r, owner)
	default:
		fail(w, 404, "not_found", "Unknown file endpoint or method")
	}
}

func (s *Service) listShares(w http.ResponseWriter) {
	shares := make([]Share, 0, len(s.shares))
	for _, share := range s.shares {
		shares = append(shares, share.Share)
	}
	sort.Slice(shares, func(i, j int) bool { return shares[i].ID < shares[j].ID })
	writeJSON(w, 200, map[string]any{"shares": shares, "maxChunkBytes": s.config.MaxChunkBytes})
}

func (s *Service) share(w http.ResponseWriter, id, p string, write, allowRoot bool) *openShare {
	share := s.shares[id]
	if share == nil {
		fail(w, 404, "share_not_found", "Shared directory not found")
		return nil
	}
	if !validPath(p, allowRoot) {
		fail(w, 400, "invalid_path", "Path must be relative to the shared directory")
		return nil
	}
	if write && share.ReadOnly {
		fail(w, 403, "read_only", "Shared directory is read-only")
		return nil
	}
	// Keep internal aliases to reserved staging names out of the API as well.
	// os.Root remains the containment boundary if a local process races this
	// inspection by replacing path components.
	if err := rejectSymlinkPath(share.root, p); err != nil {
		fileError(w, err)
		return nil
	}
	return share
}

func rejectSymlinkPath(root *os.Root, name string) error {
	if name == "" || name == "." {
		return nil
	}
	prefix := ""
	for _, component := range strings.Split(name, "/") {
		prefix = path.Join(prefix, component)
		info, err := root.Lstat(prefix)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fs.ErrPermission
		}
	}
	return nil
}

func (s *Service) entries(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	share := s.share(w, r.URL.Query().Get("share"), p, false, true)
	if share == nil {
		return
	}
	if p == "" {
		p = "."
	}
	info, err := share.root.Stat(p)
	if err != nil {
		fileError(w, err)
		return
	}
	if !info.IsDir() {
		fail(w, 400, "not_directory", "Path must be a directory")
		return
	}
	dir, err := share.root.Open(p)
	if err != nil {
		fileError(w, err)
		return
	}
	defer dir.Close()
	items, err := dir.ReadDir(-1)
	if err != nil {
		fileError(w, err)
		return
	}
	entries := make([]Entry, 0, len(items))
	for _, item := range items {
		if strings.HasPrefix(item.Name(), stagingPrefix) || item.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, e := item.Info()
		if e != nil {
			continue
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			continue
		}
		entry := Entry{Name: item.Name(), Path: path.Join(p, item.Name()), Directory: info.IsDir(), Size: info.Size(), Modified: info.ModTime().UTC()}
		if info.Mode().IsRegular() {
			entry.Version = version(info)
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Directory != entries[j].Directory {
			return entries[i].Directory
		}
		return entries[i].Name < entries[j].Name
	})
	writeJSON(w, 200, map[string]any{"entries": entries})
}

func (s *Service) mkdir(w http.ResponseWriter, r *http.Request, owner string) {
	var body struct {
		ShareID string `json:"shareId"`
		Path    string `json:"path"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.check(r, owner); err != nil {
		fileError(w, err)
		return
	}
	share := s.share(w, body.ShareID, body.Path, true, false)
	if share == nil {
		return
	}
	if err := share.root.MkdirAll(body.Path, 0750); err != nil {
		fileError(w, err)
		return
	}
	if err := syncDirectory(share.root, path.Dir(body.Path)); err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"path": body.Path})
}

var errUnauthorized = errors.New("authorization revoked")
var errSpace = errors.New("insufficient space")

func (s *Service) check(r *http.Request, owner string) error {
	if err := r.Context().Err(); err != nil {
		return err
	}
	identity, err := s.config.Authorize(r.Context(), r)
	if err != nil || identity != owner {
		return errUnauthorized
	}
	return nil
}

type checkedReader struct {
	read  io.Reader
	check func() error
}

func (r checkedReader) Read(p []byte) (int, error) {
	if err := r.check(); err != nil {
		return 0, err
	}
	if len(p) > 64<<10 {
		p = p[:64<<10]
	}
	return r.read.Read(p)
}

type checkedReadSeeker struct {
	*os.File
	check func() error
}

func (r checkedReadSeeker) Read(p []byte) (int, error) {
	return (checkedReader{read: r.File, check: r.check}).Read(p)
}

func (s *Service) download(w http.ResponseWriter, r *http.Request, owner string) {
	p := r.URL.Query().Get("path")
	share := s.share(w, r.URL.Query().Get("share"), p, false, false)
	if share == nil {
		return
	}
	before, err := share.root.Stat(p)
	if err != nil {
		fileError(w, err)
		return
	}
	if !before.Mode().IsRegular() {
		fail(w, 400, "not_regular", "Only regular files can be downloaded")
		return
	}
	f, err := share.root.Open(p)
	if err != nil {
		fileError(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		fileError(w, err)
		return
	}
	if !info.Mode().IsRegular() {
		fail(w, 400, "not_regular", "Only regular files can be downloaded")
		return
	}
	etag := version(info)
	match := r.Header.Get("If-Match")
	if r.Header.Get("Range") != "" && match == "" {
		fail(w, 428, "version_required", "Resuming requires the original ETag in If-Match")
		return
	}
	if match != "" && match != etag {
		fail(w, 412, "source_changed", "Source changed; restart the download")
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(path.Base(p)))
	check := func() error {
		if e := s.check(r, owner); e != nil {
			return e
		}
		now, e := f.Stat()
		if e != nil {
			return e
		}
		if version(now) != etag {
			return errors.New("source changed")
		}
		return nil
	}
	http.ServeContent(w, r, path.Base(p), info.ModTime(), checkedReadSeeker{File: f, check: check})
}

func (s *Service) checksum(w http.ResponseWriter, r *http.Request, owner string) {
	p := r.URL.Query().Get("path")
	share := s.share(w, r.URL.Query().Get("share"), p, false, false)
	if share == nil {
		return
	}
	info, err := share.root.Stat(p)
	if err != nil {
		fileError(w, err)
		return
	}
	if !info.Mode().IsRegular() {
		fail(w, 400, "not_regular", "Only regular files have checksums")
		return
	}
	f, err := share.root.Open(p)
	if err != nil {
		fileError(w, err)
		return
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		fileError(w, err)
		return
	}
	if !info.Mode().IsRegular() {
		fail(w, 400, "not_regular", "Only regular files have checksums")
		return
	}
	etag := version(info)
	if match := r.Header.Get("If-Match"); match != "" && match != etag {
		fail(w, 412, "source_changed", "Source changed; restart the download")
		return
	}
	changed := errors.New("checksum source changed")
	check := func() error {
		if err := s.check(r, owner); err != nil {
			return err
		}
		current, err := f.Stat()
		if err != nil {
			return err
		}
		if version(current) != etag {
			return changed
		}
		// Also detect an atomic replacement of the pathname while the original
		// inode remains open and unchanged.
		current, err = share.root.Stat(p)
		if err != nil {
			return changed
		}
		if version(current) != etag {
			return changed
		}
		return nil
	}
	hash := sha256.New()
	count, err := io.CopyBuffer(hash, checkedReader{read: io.LimitReader(f, info.Size()+1), check: check}, make([]byte, 64<<10))
	if err == nil {
		err = check()
	}
	if errors.Is(err, changed) || count != info.Size() && err == nil {
		fail(w, 412, "source_changed", "Source changed during checksum; restart the download")
		return
	}
	if err != nil {
		fileError(w, err)
		return
	}
	w.Header().Set("ETag", etag)
	writeJSON(w, 200, map[string]any{"sha256": hex.EncodeToString(hash.Sum(nil)), "size": info.Size(), "version": etag})
}
