package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xiaojohn-eng/JunGo/internal/files"
	"github.com/xiaojohn-eng/JunGo/internal/secure"
)

type Transfer struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Direction     string    `json:"direction"`
	DeviceID      string    `json:"deviceId"`
	ShareID       string    `json:"shareId"`
	Path          string    `json:"path"`
	Source        string    `json:"source"`
	Destination   string    `json:"destination"`
	Size          int64     `json:"size"`
	Completed     int64     `json:"completed"`
	Status        string    `json:"status"`
	Error         string    `json:"error"`
	UploadID      string    `json:"uploadId,omitempty"`
	ChatMessageID string    `json:"chatMessageId,omitempty"`
	Hash          string    `json:"sha256,omitempty"`
	ETag          string    `json:"etag,omitempty"`
	Overwrite     bool      `json:"overwrite,omitempty"`
	CancelRemote  bool      `json:"cancelRemote,omitempty"`
	Created       time.Time `json:"created"`
	Updated       time.Time `json:"updated"`
}

func (e *Engine) taskPath() string { return filepath.Join(e.dir, "transfers.json") }
func (e *Engine) loadTransfers() error {
	b, err := os.ReadFile(e.taskPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var tasks []Transfer
	if err = json.Unmarshal(b, &tasks); err != nil {
		return err
	}
	for _, task := range tasks {
		t := task
		if t.Status == "running" || t.Status == "hashing" {
			t.Status = "queued"
		}
		e.tasks[t.ID] = &t
	}
	return nil
}
func (e *Engine) persistTasksLocked() error {
	tasks := make([]Transfer, 0, len(e.tasks))
	for _, t := range e.tasks {
		tasks = append(tasks, *t)
	}
	b, err := json.Marshal(tasks)
	if err != nil {
		return err
	}
	return secure.WriteFile(e.taskPath(), b)
}
func (e *Engine) transferSnapshot() []Transfer {
	result := e.filteredTransferSnapshot(nil)
	// Sorting retained history must not hold the lock needed by pause/cancel.
	sort.Slice(result, func(i, j int) bool {
		if result[i].Created.Equal(result[j].Created) {
			return result[i].ID < result[j].ID
		}
		return result[i].Created.Before(result[j].Created)
	})
	return result
}
func (e *Engine) filteredTransferSnapshot(include func(*Transfer) bool) []Transfer {
	e.tasksMu.Lock()
	defer e.tasksMu.Unlock()
	capacity := len(e.tasks)
	if include != nil {
		capacity = min(capacity, 16)
	}
	result := make([]Transfer, 0, capacity)
	for _, t := range e.tasks {
		if include == nil || include(t) {
			result = append(result, *t)
		}
	}
	return result
}
func (e *Engine) runnableTransfers() []Transfer {
	result := e.filteredTransferSnapshot(func(t *Transfer) bool {
		return t.Status == "queued" || t.Status == "waiting" || t.Status == "cancelled" && t.CancelRemote
	})
	sort.Slice(result, func(i, j int) bool { return result[i].Created.Before(result[j].Created) })
	return result
}

func (e *Engine) wakeTransfers() {
	select {
	case e.workerWake <- struct{}{}:
	default:
	}
}
func (e *Engine) addTransfer(t Transfer) (Transfer, error) {
	if e.ctx.Err() != nil {
		return t, errors.New("客户端已关闭")
	}
	if t.DeviceID == "" || t.ShareID == "" || t.Path == "" {
		return t, errors.New("必须选择设备、共享目录和文件路径")
	}
	if t.Direction == "upload" && t.Source == "" || (t.Direction == "download" || t.Direction == "save") && t.Destination == "" {
		return t, errors.New("必须选择本地文件")
	}
	t.ID = secure.Random(16)
	t.Name = path.Base(t.Path)
	t.Status = "queued"
	t.Created = time.Now()
	t.Updated = t.Created
	e.tasksMu.Lock()
	if len(e.tasks) >= 10000 {
		e.tasksMu.Unlock()
		return t, errors.New("传输任务数达到上限")
	}
	e.tasks[t.ID] = &t
	err := e.persistTasksLocked()
	if err != nil {
		delete(e.tasks, t.ID)
	}
	result := t
	e.tasksMu.Unlock()
	if err == nil {
		e.wakeTransfers()
	}
	return result, err
}
func (e *Engine) updateTask(id string, fn func(*Transfer)) error {
	e.tasksMu.Lock()
	defer e.tasksMu.Unlock()
	t, ok := e.tasks[id]
	if !ok {
		return errors.New("任务不存在")
	}
	status := t.Status
	fn(t)
	if status == "paused" || status == "cancelled" {
		t.Status = status
	}
	t.Updated = time.Now()
	return e.persistTasksLocked()
}
func (e *Engine) transferAction(id, action string) error {
	e.tasksMu.Lock()
	t, ok := e.tasks[id]
	if !ok {
		e.tasksMu.Unlock()
		return errors.New("任务不存在")
	}
	if t.Status == "complete" {
		e.tasksMu.Unlock()
		return errors.New("任务已完成")
	}
	if t.Status == "cancelled" {
		e.tasksMu.Unlock()
		if action == "cancel" {
			return nil
		}
		return errors.New("已取消任务不能续传，请创建新任务")
	}
	switch action {
	case "pause":
		t.Status = "paused"
	case "cancel":
		t.Status = "cancelled"
		t.CancelRemote = t.Direction == "upload" && (t.UploadID != "" || t.Hash != "")
	case "resume":
		if t.Status == "cancelled" {
			e.tasksMu.Unlock()
			return errors.New("已取消任务不能续传，请创建新任务")
		}
		t.Status = "queued"
		t.Error = ""
	default:
		e.tasksMu.Unlock()
		return errors.New("无效的任务操作")
	}
	if action != "resume" {
		if cancel := e.taskCancels[id]; cancel != nil {
			cancel()
		}
	}
	t.Updated = time.Now()
	err := e.persistTasksLocked()
	e.tasksMu.Unlock()
	e.wakeTransfers()
	return err
}

// Cancellation is a durable local decision. Its remote cleanup can wait for
// connectivity without ever returning the task to the runnable upload queue.
func (e *Engine) cleanupCancelledTransfer(t Transfer) {
	route := "/v1/files/uploads/" + t.UploadID
	if t.UploadID == "" {
		route = "/v1/files/uploads/by-request/" + t.ID
	}
	ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	defer cancel()
	_, err := e.fileJSON(ctx, t.DeviceID, "DELETE", route, nil, nil)
	var apiErr *APIError
	terminal, published := err == nil, false
	if errors.As(err, &apiErr) {
		terminal = apiErr.Status == 404
		if apiErr.Status == 409 {
			var body struct {
				Error struct{ Code string } `json:"error"`
			}
			if json.Unmarshal([]byte(apiErr.Message), &body) == nil && body.Error.Code == "cannot_cancel" {
				terminal, published = true, true
			}
		}
	}
	message := "已取消；设备连接恢复后清理远端临时文件"
	if terminal {
		message = ""
		if published {
			message = "已停止本地传输；远端已进入发布阶段，文件可能已接收"
		}
	}
	if t.CancelRemote == !terminal && t.Error == message {
		return
	}
	_ = e.updateTask(t.ID, func(x *Transfer) {
		x.CancelRemote = !terminal
		x.Error = message
	})
}

func (e *Engine) transferLoop() {
	defer close(e.workerDone)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-e.workerWake:
		case <-ticker.C:
		}
		tasks := e.runnableTransfers()
		for _, task := range tasks {
			if e.ctx.Err() != nil {
				return
			}
			if task.Status == "cancelled" && task.CancelRemote {
				e.cleanupCancelledTransfer(task)
				continue
			}
			if task.Status != "queued" && task.Status != "waiting" {
				continue
			}
			ctx, cancel := context.WithCancel(e.ctx)
			e.tasksMu.Lock()
			current := e.tasks[task.ID]
			if current.Status != "queued" && current.Status != "waiting" {
				e.tasksMu.Unlock()
				cancel()
				continue
			}
			e.taskCancels[task.ID] = cancel
			current.Status = "running"
			e.tasksMu.Unlock()
			var err error
			if task.Direction == "upload" {
				err = e.runUpload(ctx, task)
			} else if task.Direction == "save" {
				err = e.runSave(ctx, task)
			} else {
				err = e.runDownload(ctx, task)
			}
			cancel()
			e.tasksMu.Lock()
			delete(e.taskCancels, task.ID)
			current = e.tasks[task.ID]
			if current.Status != "paused" && current.Status != "cancelled" {
				if err == nil {
					current.Status = "complete"
					current.Completed = current.Size
					current.Error = ""
				} else {
					current.Error = err.Error()
					current.Status = "waiting"
					var apiErr *APIError
					if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 600 && apiErr.Status != 408 && apiErr.Status != 429 && apiErr.Status != 502 && apiErr.Status != 503 && apiErr.Status != 504 {
						current.Status = "failed"
					}
					var fatal *transferError
					if errors.As(err, &fatal) {
						current.Status = "failed"
					}
				}
			}
			current.Updated = time.Now()
			persistErr := e.persistTasksLocked()
			e.tasksMu.Unlock()
			if persistErr != nil {
				e.setError(persistErr)
			}
		}
	}
}

type transferError struct{ error }

func permanent(err error) error {
	if err == nil {
		return nil
	}
	return &transferError{err}
}
func (e *Engine) openFile(name, mode string) (*os.File, error) {
	if strings.HasPrefix(name, "inbox://") {
		if mode != "r" {
			return nil, errors.New("收件箱源文件只能读取")
		}
		root, err := os.OpenRoot(filepath.Join(e.dir, "chat-inbox"))
		if err != nil {
			return nil, err
		}
		defer root.Close()
		return root.Open(strings.TrimPrefix(name, "inbox://"))
	}
	if strings.HasPrefix(name, "content://") {
		if e.platform == nil {
			return nil, errors.New("此平台不支持Android内容URI")
		}
		fd := e.platform.OpenURI(name, mode)
		if fd < 0 {
			return nil, errors.New("无法打开文件，请重新授予文件访问权限")
		}
		return os.NewFile(uintptr(fd), name), nil
	}
	if strings.Contains(name, "://") {
		return nil, errors.New("不支持的本地文件地址")
	}
	if mode == "r" {
		return os.Open(name)
	}
	return os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0600)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}
func (e *Engine) fileRequest(ctx context.Context, id, method, route string, body io.Reader, headers http.Header) (*http.Response, *http.Client, error) {
	client, base, key, self, err := e.peerClient(id)
	if err != nil {
		return nil, nil, err
	}
	// Chunk/metadata calls have bounded duration; large download bodies rely on
	// socket inactivity deadlines so a healthy 5 GiB transfer can keep streaming.
	cancel := func() {}
	if !(method == "GET" && strings.HasPrefix(route, "/v1/files/download?")) {
		limit := 2 * time.Minute
		if strings.Contains(route, "/checksum?") || strings.HasSuffix(route, "/complete") {
			limit = 10 * time.Minute
		}
		ctx, cancel = context.WithTimeout(ctx, limit)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+route, body)
	if err != nil {
		cancel()
		return nil, client, err
	}
	for k, v := range headers {
		req.Header[k] = v
	}
	secure.Sign(req, self, key)
	response, err := client.Do(req)
	if err != nil {
		cancel()
		client.CloseIdleConnections()
		return nil, client, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
		cancel()
		client.CloseIdleConnections()
		return nil, client, &APIError{Status: response.StatusCode, Message: string(b)}
	}
	response.Body = &cancelBody{ReadCloser: response.Body, cancel: cancel}
	return response, client, nil
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error { err := b.ReadCloser.Close(); b.cancel(); return err }
func (e *Engine) fileJSON(ctx context.Context, id, method, route string, input any, headers http.Header) ([]byte, error) {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
		if headers == nil {
			headers = make(http.Header)
		}
		headers.Set("Content-Type", "application/json")
	}
	resp, _, err := e.fileRequest(ctx, id, method, route, body, headers)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}
func (e *Engine) runUpload(ctx context.Context, t Transfer) error {
	f, err := e.openFile(t.Source, "r")
	if err != nil {
		return permanent(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return permanent(err)
	}
	if !info.Mode().IsRegular() && !strings.HasPrefix(t.Source, "content://") {
		return permanent(errors.New("只能上传普通文件"))
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return permanent(errors.New("文件提供方不支持随机读取与断点续传"))
	}
	if err = e.updateTask(t.ID, func(t *Transfer) { t.Status = "hashing" }); err != nil {
		return err
	}
	h := sha256.New()
	size, err := io.CopyBuffer(h, contextReader{ctx, f}, make([]byte, 256<<10))
	if err != nil {
		return err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if t.Hash != "" && (t.Hash != digest || t.Size != size) {
		return permanent(errors.New("本地源文件已变化，不能沿用旧断点；请新建传输"))
	}
	t.Hash = digest
	t.Size = size
	if err = e.updateTask(t.ID, func(x *Transfer) { x.Hash = digest; x.Size = size; x.Status = "running" }); err != nil {
		return err
	}
	if t.UploadID == "" {
		parent := path.Dir(t.Path)
		if parent != "." {
			if _, err = e.fileJSON(ctx, t.DeviceID, "POST", "/v1/files/mkdir", map[string]string{"shareId": t.ShareID, "path": parent}, nil); err != nil {
				return err
			}
		}
		b, err := e.fileJSON(ctx, t.DeviceID, "POST", "/v1/files/uploads", files.BeginRequest{ShareID: t.ShareID, Path: t.Path, Size: size, SHA256: digest, OverwriteConfirmed: t.Overwrite}, http.Header{"X-Jungo-Upload-ID": {t.ID}})
		if err != nil {
			return err
		}
		var upload files.Upload
		if err = json.Unmarshal(b, &upload); err != nil {
			return err
		}
		t.UploadID = upload.ID
		if err = e.updateTask(t.ID, func(x *Transfer) { x.UploadID = t.UploadID }); err != nil {
			return err
		}
	}
	b, err := e.fileJSON(ctx, t.DeviceID, "GET", "/v1/files/uploads/"+t.UploadID, nil, nil)
	if err != nil {
		return err
	}
	var remote files.Upload
	if err = json.Unmarshal(b, &remote); err != nil {
		return err
	}
	if remote.Size != size || remote.SHA256 != digest {
		return permanent(errors.New("远端任务与本地源文件不一致"))
	}
	if remote.State == "completed" {
		return e.updateTask(t.ID, func(x *Transfer) { x.Path = remote.Path; x.Name = path.Base(remote.Path) })
	}
	offset := remote.Offset
	if offset < 0 || offset > size {
		return permanent(errors.New("无效的远端断点"))
	}
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return permanent(err)
	}
	buf := make([]byte, 4<<20)
	for offset < size {
		if err = ctx.Err(); err != nil {
			return err
		}
		n := int64(len(buf))
		if size-offset < n {
			n = size - offset
		}
		if _, err = io.ReadFull(f, buf[:n]); err != nil {
			return permanent(err)
		}
		headers := http.Header{"Upload-Offset": {strconv.FormatInt(offset, 10)}, "Content-Type": {"application/octet-stream"}}
		response, _, err := e.fileRequest(ctx, t.DeviceID, "PATCH", "/v1/files/uploads/"+t.UploadID, bytes.NewReader(buf[:n]), headers)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
		if err != nil {
			return err
		}
		if err = json.Unmarshal(body, &remote); err != nil {
			return err
		}
		if remote.Offset != offset+n {
			return permanent(errors.New("远端确认偏移不正确"))
		}
		offset = remote.Offset
		if err = e.updateTask(t.ID, func(x *Transfer) { x.Completed = offset }); err != nil {
			return err
		}
	}
	b, err = e.fileJSON(ctx, t.DeviceID, "POST", "/v1/files/uploads/"+t.UploadID+"/complete", nil, nil)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, &remote); err != nil {
		return err
	}
	if remote.State != "completed" {
		return errors.New("远端尚未完成完整性验证")
	}
	return e.updateTask(t.ID, func(x *Transfer) { x.Path = remote.Path; x.Name = path.Base(remote.Path); x.Completed = size })
}
func (e *Engine) runDownload(ctx context.Context, t Transfer) error {
	route := "/v1/files/download?share=" + url.QueryEscape(t.ShareID) + "&path=" + url.QueryEscape(t.Path)
	head, _, err := e.fileRequest(ctx, t.DeviceID, "HEAD", route, nil, nil)
	if err != nil {
		return err
	}
	etag, size := head.Header.Get("ETag"), head.ContentLength
	head.Body.Close()
	if size < 0 || etag == "" {
		return permanent(errors.New("远端未提供可靠的文件版本信息"))
	}
	if t.ETag != "" && t.ETag != etag {
		return permanent(errors.New("远端源文件已变化，请重新下载"))
	}
	if t.Completed > size {
		return permanent(errors.New("本地断点超出文件长度"))
	}
	f, err := e.openFile(t.Destination, "rw")
	if err != nil {
		return permanent(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return permanent(err)
	}
	if t.ETag == "" && info.Size() > 0 {
		return permanent(errors.New("目标文件已存在；请选择新文件以避免覆盖"))
	}
	if info.Size() < t.Completed {
		t.Completed = info.Size()
	}
	if _, err = f.Seek(t.Completed, io.SeekStart); err != nil {
		return permanent(errors.New("目标文件提供方不支持断点续传"))
	}
	t.ETag = etag
	t.Size = size
	if err = e.updateTask(t.ID, func(x *Transfer) { x.Size = size; x.ETag = etag; x.Completed = t.Completed }); err != nil {
		return err
	}
	if t.Completed < size {
		headers := http.Header{"If-Match": {etag}}
		if t.Completed > 0 {
			headers.Set("Range", fmt.Sprintf("bytes=%d-", t.Completed))
		}
		resp, _, err := e.fileRequest(ctx, t.DeviceID, "GET", route, nil, headers)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if (t.Completed > 0 && resp.StatusCode != 206) || resp.ContentLength != size-t.Completed || resp.Header.Get("ETag") != etag {
			return permanent(errors.New("远端返回了不匹配的文件版本或范围"))
		}
		offset := t.Completed
		buf := make([]byte, 1<<20)
		durable := offset
		for offset < size {
			if err = ctx.Err(); err != nil {
				return err
			}
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				written, writeErr := f.Write(buf[:n])
				offset += int64(written)
				if writeErr != nil {
					return permanent(writeErr)
				}
				if written != n {
					return permanent(io.ErrShortWrite)
				}
			}
			if offset-durable >= 8<<20 || offset == size {
				if err = f.Sync(); err != nil {
					return permanent(err)
				}
				if err = e.updateTask(t.ID, func(x *Transfer) { x.Completed = offset }); err != nil {
					return err
				}
				durable = offset
			}
			if readErr != nil {
				if readErr == io.EOF && offset == size {
					break
				}
				return readErr
			}
		}
	}
	if err = f.Truncate(size); err != nil {
		return permanent(err)
	}
	if err = f.Sync(); err != nil {
		return permanent(err)
	}
	checksumRoute := "/v1/files/checksum?share=" + url.QueryEscape(t.ShareID) + "&path=" + url.QueryEscape(t.Path)
	b, err := e.fileJSON(ctx, t.DeviceID, "GET", checksumRoute, nil, http.Header{"If-Match": {etag}})
	if err != nil {
		return err
	}
	var expected struct {
		SHA256  string `json:"sha256"`
		Version string `json:"version"`
		Size    int64  `json:"size"`
	}
	if err = json.Unmarshal(b, &expected); err != nil {
		return err
	}
	if expected.Version != etag || expected.Size != size {
		return permanent(errors.New("下载校验时源文件发生变化"))
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return permanent(err)
	}
	h := sha256.New()
	if _, err = io.CopyBuffer(h, contextReader{ctx, f}, make([]byte, 256<<10)); err != nil {
		return err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if digest != expected.SHA256 {
		return permanent(errors.New("下载文件SHA-256不一致，未标记为完成"))
	}
	return e.updateTask(t.ID, func(x *Transfer) { x.Hash = digest; x.Completed = size })
}
