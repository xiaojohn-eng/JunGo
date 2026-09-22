package files

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

func (s *Service) enoughSpace(root *os.Root, directory string, needed int64) error {
	f, err := root.Open(directory)
	if err != nil {
		return err
	}
	defer f.Close()
	available, err := freeBytes(f)
	if err != nil {
		return err
	}
	if available < s.config.MinFreeBytes || needed > available-s.config.MinFreeBytes {
		return errSpace
	}
	return nil
}

func (s *Service) begin(w http.ResponseWriter, r *http.Request, owner string) {
	var body BeginRequest
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
	if body.Size < 0 || body.Size > s.config.MaxUploadBytes {
		fail(w, 413, "file_too_large", "File size exceeds the configured limit")
		return
	}
	if !validDigest(body.SHA256) {
		fail(w, 400, "invalid_hash", "A SHA-256 digest is required")
		return
	}
	requestID := r.Header.Get("X-Jungo-Upload-ID")
	if requestID != "" && !validID(requestID) {
		fail(w, 400, "invalid_request_id", "Upload request ID is invalid")
		return
	}
	parent, err := share.root.Stat(path.Dir(body.Path))
	if err != nil {
		fileError(w, err)
		return
	}
	if !parent.IsDir() {
		fail(w, 400, "not_directory", "Parent must be a directory")
		return
	}
	if info, e := share.root.Lstat(body.Path); e == nil && body.OverwriteConfirmed && !info.Mode().IsRegular() {
		fail(w, 409, "not_regular", "Only regular files may be overwritten")
		return
	} else if e != nil && !errors.Is(e, fs.ErrNotExist) {
		fileError(w, e)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.check(r, owner); err != nil {
		fileError(w, err)
		return
	}
	// A dropped creation response must not reserve another full upload on every
	// retry. This optional header is ignored safely by older file services.
	if requestID != "" {
		if existing := s.requests[uploadRequestKey{owner, requestID}]; existing != nil {
			// Drop the index lock before the per-task lock: completion releases
			// quota while holding the task lock, in the opposite order.
			s.mu.Unlock()
			existing.mu.Lock()
			if err := s.check(r, owner); err != nil {
				fileError(w, err)
			} else if existing.ShareID != body.ShareID || existing.OriginalPath != body.Path || existing.Size != body.Size || existing.SHA256 != strings.ToLower(body.SHA256) || existing.OverwriteConfirmed != body.OverwriteConfirmed {
				fail(w, 409, "request_conflict", "Upload request ID was already used for different contents")
			} else {
				writeJSON(w, 200, existing.Upload)
			}
			existing.mu.Unlock()
			s.mu.Lock()
			return
		}
	}
	if err = s.enoughSpace(s.state, ".", body.Size); err != nil {
		fileError(w, err)
		return
	}
	if body.Size > s.config.MaxPendingBytes-s.reserved {
		fail(w, 507, "quota_exceeded", "Pending uploads exceed the configured quota")
		return
	}
	id, err := randomID()
	if err != nil {
		fail(w, 500, "internal_error", "Unable to create upload")
		return
	}
	f, err := s.state.OpenFile(id+".part", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		fileError(w, err)
		return
	}
	err = f.Sync()
	err = errors.Join(err, f.Close())
	if err != nil {
		_ = s.state.Remove(id + ".part")
		fileError(w, err)
		return
	}
	task := &uploadTask{diskUpload: diskUpload{Upload: Upload{ID: id, ShareID: body.ShareID, Path: body.Path, Size: body.Size, State: "pending", SHA256: strings.ToLower(body.SHA256)}, OwnerID: owner, OriginalPath: body.Path, OverwriteConfirmed: body.OverwriteConfirmed, RequestID: requestID}}
	if err = s.save(task); err != nil {
		// A failed directory fsync can happen after the snapshot rename. Keep
		// matching contents whenever metadata exists, so restart is recoverable.
		if _, statErr := s.state.Stat(id + ".json"); errors.Is(statErr, fs.ErrNotExist) {
			_ = s.state.Remove(id + ".part")
		} else {
			s.tasks[id] = task
			if requestID != "" {
				s.requests[uploadRequestKey{owner, requestID}] = task
			}
			s.reserved += body.Size
		}
		fileError(w, err)
		return
	}
	s.tasks[id] = task
	if requestID != "" {
		s.requests[uploadRequestKey{owner, requestID}] = task
	}
	s.reserved += body.Size
	writeJSON(w, 201, task.Upload)
}

func (s *Service) upload(w http.ResponseWriter, r *http.Request, owner string) {
	remainder := strings.TrimPrefix(r.URL.Path, "/v1/files/uploads/")
	parts := strings.Split(remainder, "/")
	if len(parts) == 2 && parts[0] == "by-request" && validID(parts[1]) && r.Method == http.MethodDelete {
		// Resolve creation responses lost in transit without allocating another
		// upload merely to cancel it. Request IDs are scoped to the sender.
		s.mu.Lock()
		found := s.requests[uploadRequestKey{owner, parts[1]}]
		s.mu.Unlock()
		if found == nil {
			fail(w, 404, "not_found", "Upload not found")
			return
		}
		parts = []string{found.ID}
	}
	if len(parts) > 2 || !validUploadID(parts[0]) {
		fail(w, 404, "not_found", "Upload not found")
		return
	}
	s.mu.Lock()
	task := s.tasks[parts[0]]
	s.mu.Unlock()
	if task == nil {
		fail(w, 404, "not_found", "Upload not found")
		return
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	if err := s.check(r, owner); err != nil {
		fileError(w, err)
		return
	}
	if task.OwnerID != owner {
		fail(w, 404, "not_found", "Upload not found")
		return
	}
	if len(parts) == 2 && parts[1] == "complete" && r.Method == http.MethodPost {
		s.complete(w, r, owner, task)
		return
	}
	if len(parts) != 1 {
		fail(w, 404, "not_found", "Unknown upload endpoint")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, task.Upload)
	case http.MethodPatch:
		s.chunk(w, r, owner, task)
	case http.MethodDelete:
		s.cancel(w, task)
	default:
		fail(w, 405, "method_not_allowed", "Unsupported upload method")
	}
}

func (s *Service) chunk(w http.ResponseWriter, r *http.Request, owner string, task *uploadTask) {
	if task.State != "pending" {
		fail(w, 409, "upload_not_pending", "Upload cannot accept more chunks")
		return
	}
	if s.share(w, task.ShareID, task.OriginalPath, true, false) == nil {
		return
	}
	offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || offset < 0 {
		fail(w, 400, "invalid_offset", "Upload-Offset must be a nonnegative integer")
		return
	}
	if offset > task.Offset {
		offsetConflict(w, task.Offset)
		return
	}
	if r.ContentLength > s.config.MaxChunkBytes {
		fail(w, 413, "chunk_too_large", "Chunk exceeds the configured maximum")
		return
	}
	file, err := s.state.OpenFile(task.ID+".part", os.O_RDWR, 0600)
	if err != nil {
		fileError(w, err)
		return
	}
	defer file.Close()
	body := checkedReader{read: io.LimitReader(r.Body, s.config.MaxChunkBytes+1), check: func() error { return s.check(r, owner) }}
	if offset < task.Offset {
		count, err := compareChunk(body, io.NewSectionReader(file, offset, task.Offset-offset), s.config.MaxChunkBytes)
		if errors.Is(err, errUnauthorized) {
			fileError(w, err)
			return
		}
		if err != nil || count == 0 {
			offsetConflict(w, task.Offset)
			return
		}
		writeJSON(w, 200, task.Upload)
		return
	}
	if r.ContentLength > task.Size-task.Offset {
		fail(w, 413, "size_exceeded", "Chunk exceeds the declared file size")
		return
	}
	if err = s.enoughSpace(s.state, ".", min(s.config.MaxChunkBytes, task.Size-task.Offset)); err != nil {
		fileError(w, err)
		return
	}
	if _, err = file.Seek(task.Offset, io.SeekStart); err != nil {
		fileError(w, err)
		return
	}
	count, err := io.CopyBuffer(file, io.LimitReader(body, min(s.config.MaxChunkBytes, task.Size-task.Offset)+1), make([]byte, 64<<10))
	if err == nil {
		err = s.check(r, owner)
	}
	tooLarge := count > s.config.MaxChunkBytes || count > task.Size-task.Offset
	if err != nil || tooLarge || count == 0 {
		_ = file.Truncate(task.Offset)
		if err != nil {
			fileError(w, err)
		} else if tooLarge {
			fail(w, 413, "chunk_too_large", "Chunk exceeds upload or request size limits")
		} else {
			fail(w, 400, "empty_chunk", "Chunk cannot be empty")
		}
		return
	}
	if err = file.Sync(); err != nil {
		_ = file.Truncate(task.Offset)
		fileError(w, err)
		return
	}
	previousOffset := task.Offset
	task.Offset += count
	// Never truncate after attempting a metadata commit: rename may have
	// succeeded even if syncing its directory subsequently failed.
	if err = s.save(task); err != nil {
		task.Offset = previousOffset
		fileError(w, err)
		return
	}
	writeJSON(w, 200, task.Upload)
}

func compareChunk(incoming, existing io.Reader, limit int64) (int64, error) {
	a := make([]byte, 64<<10)
	b := make([]byte, 64<<10)
	var total int64
	for {
		n, err := incoming.Read(a)
		if n > 0 {
			total += int64(n)
			if total > limit {
				return total, errors.New("chunk exceeds limit")
			}
			m, e := io.ReadFull(existing, b[:n])
			if e != nil || m != n || !bytes.Equal(a[:n], b[:n]) {
				return total, errors.New("chunk differs")
			}
		}
		if errors.Is(err, io.EOF) {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

func (s *Service) cancel(w http.ResponseWriter, task *uploadTask) {
	if task.State == "completed" || task.State == "publishing" {
		fail(w, 409, "cannot_cancel", "Completed or publishing uploads cannot be cancelled")
		return
	}
	if task.State != "cancelled" {
		task.State = "cancelled"
		if err := s.save(task); err != nil {
			task.State = "pending"
			fileError(w, err)
			return
		}
		s.release(task.Size)
	}
	// Cancellation metadata may have committed before a failed unlink. Retry
	// cleanup even for an already cancelled task, without releasing quota twice.
	if err := s.state.Remove(task.ID + ".part"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fileError(w, err)
		return
	}
	writeJSON(w, 200, task.Upload)
}

func (s *Service) release(size int64) { s.mu.Lock(); s.reserved -= size; s.mu.Unlock() }

func (s *Service) complete(w http.ResponseWriter, r *http.Request, owner string, task *uploadTask) {
	if task.State == "completed" {
		writeJSON(w, 200, task.Upload)
		return
	}
	if task.State == "cancelled" {
		fail(w, 409, "cancelled", "Upload was cancelled")
		return
	}
	share := s.share(w, task.ShareID, task.OriginalPath, true, false)
	if share == nil {
		return
	}
	if task.Offset != task.Size {
		offsetConflict(w, task.Offset)
		return
	}
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	check := func() error { return s.check(r, owner) }
	// Recover a crash between publishing the complete file and saving the
	// completed snapshot. Hash verification prevents false completion.
	if task.State == "publishing" {
		if ok, e := s.publishedMatches(share.root, task, check); e == nil && ok {
			s.finish(w, share, task)
			return
		} else if errors.Is(e, errUnauthorized) {
			fileError(w, e)
			return
		}
	}
	part, err := s.state.Open(task.ID + ".part")
	if err != nil {
		fileError(w, err)
		return
	}
	defer part.Close()
	if err = s.enoughSpace(share.root, path.Dir(task.OriginalPath), task.Size); err != nil {
		fileError(w, err)
		return
	}
	stage := path.Join(path.Dir(task.OriginalPath), stagingPrefix+task.ID)
	// Staging names are reserved, hidden from listings and rejected by every
	// client path API. This supports state and shares on different volumes.
	if err = share.root.Remove(stage); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fileError(w, err)
		return
	}
	output, err := share.root.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		fileError(w, err)
		return
	}
	keepStage := false
	defer func() {
		output.Close()
		if !keepStage {
			_ = share.root.Remove(stage)
		}
	}()
	hasher := sha256.New()
	count, err := io.CopyBuffer(io.MultiWriter(output, hasher), checkedReader{read: part, check: check}, make([]byte, 64<<10))
	if err == nil {
		err = check()
	}
	if err != nil {
		fileError(w, err)
		return
	}
	if count != task.Size || hex.EncodeToString(hasher.Sum(nil)) != task.SHA256 {
		fail(w, 422, "hash_mismatch", "Uploaded content does not match the declared SHA-256 digest")
		return
	}
	if err = output.Sync(); err != nil {
		fileError(w, err)
		return
	}
	stageInfo, err := output.Stat()
	if err != nil {
		fileError(w, err)
		return
	}
	if err = output.Close(); err != nil {
		fileError(w, err)
		return
	}
	if err = syncDirectory(share.root, path.Dir(stage)); err != nil {
		fileError(w, err)
		return
	}
	task.State = "publishing"
	task.StagePath = stage
	task.PublicationVersion = version(stageInfo)
	for suffix := 0; suffix < 10000; suffix++ {
		destination := collisionName(task.OriginalPath, suffix)
		if task.OverwriteConfirmed {
			destination = task.OriginalPath
		}
		task.Path = destination
		if err = s.save(task); err != nil {
			keepStage = true
			fileError(w, err)
			return
		}
		if err = check(); err != nil {
			keepStage = true
			fileError(w, err)
			return
		}
		if task.OverwriteConfirmed {
			if info, e := share.root.Lstat(destination); e == nil && !info.Mode().IsRegular() {
				fail(w, 409, "not_regular", "Only regular files may be overwritten")
				return
			} else if e != nil && !errors.Is(e, fs.ErrNotExist) {
				fileError(w, e)
				return
			}
			err = share.root.Rename(stage, destination)
		} else {
			// Publication atomically fails when a destination exists, including
			// when an external process races with it. Android's app SELinux
			// domain forbids hardlinks, so Linux uses RENAME_NOREPLACE.
			err = publishExclusive(share.root, stage, destination)
		}
		if errors.Is(err, fs.ErrExist) && !task.OverwriteConfirmed {
			continue
		}
		if err != nil {
			keepStage = true
			fileError(w, err)
			return
		}
		keepStage = true
		if err = syncDirectory(share.root, path.Dir(destination)); err != nil {
			fileError(w, err)
			return
		}
		s.finish(w, share, task)
		return
	}
	fail(w, 409, "too_many_collisions", "Too many files with the same name")
}

func collisionName(original string, n int) string {
	if n == 0 {
		return original
	}
	ext := path.Ext(original)
	base := strings.TrimSuffix(path.Base(original), ext)
	suffix := fmt.Sprintf(" (%d)", n)
	// Most supported filesystems limit a single name to 255 bytes, not Unicode
	// characters. Adding a suffix to an already valid long name must still be
	// able to preserve both files. Keep the extension whenever it can fit.
	budget := 255 - len(suffix) - len(ext)
	if budget < 1 {
		base, ext = path.Base(original), ""
		budget = 255 - len(suffix)
	}
	if len(base) > budget {
		base = base[:budget]
		for !utf8.ValidString(base) && len(base) > 0 {
			base = base[:len(base)-1]
		}
	}
	return path.Join(path.Dir(original), base+suffix+ext)
}

func (s *Service) publishedMatches(root *os.Root, task *uploadTask, check func() error) (bool, error) {
	info, err := root.Lstat(task.Path)
	if err != nil {
		return false, err
	}
	if task.PublicationVersion != "" {
		if version(info) != task.PublicationVersion {
			return false, nil
		}
	} else if task.StagePath != "" {
		// Legacy snapshots do not contain an inode version. A remaining stage
		// proves publication only when the destination is its hard link. A
		// missing stage retains recovery of old Linux rename checkpoints.
		stageInfo, stageErr := root.Lstat(task.StagePath)
		if stageErr == nil && !os.SameFile(info, stageInfo) {
			return false, nil
		}
		if stageErr != nil && !errors.Is(stageErr, fs.ErrNotExist) {
			return false, stageErr
		}
	}
	return s.matches(root, task.Path, task.Size, task.SHA256, check)
}

func (s *Service) matches(root *os.Root, name string, size int64, digest string, check func() error) (bool, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return false, nil
	}
	f, err := root.Open(name)
	if err != nil {
		return false, err
	}
	defer f.Close()
	hash := sha256.New()
	_, err = io.CopyBuffer(hash, checkedReader{read: f, check: check}, make([]byte, 64<<10))
	return err == nil && hex.EncodeToString(hash.Sum(nil)) == digest, err
}

func (s *Service) finish(w http.ResponseWriter, share *openShare, task *uploadTask) {
	old := task.State
	task.State = "completed"
	if err := s.save(task); err != nil {
		task.State = old
		fileError(w, err)
		return
	}
	s.release(task.Size)
	// Cleanup is idempotent; the committed snapshot remains the source of truth.
	_ = s.state.Remove(task.ID + ".part")
	if task.StagePath != "" {
		_ = share.root.Remove(task.StagePath)
	}
	writeJSON(w, 200, task.Upload)
}
