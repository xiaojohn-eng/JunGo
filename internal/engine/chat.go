package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xiaojohn-eng/JunGo/internal/chat"
	"github.com/xiaojohn-eng/JunGo/internal/secure"
)

const chatInboxShare = "chat-inbox"

// Wire messages never carry local content URIs, absolute paths or local task IDs.
type wireChatMessage struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	Completed int64     `json:"completed"`
	Status    string    `json:"status"`
	Path      string    `json:"path"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
	Revision  uint64    `json:"revision"`
}

func (e *Engine) wakeChat() {
	select {
	case e.chatWake <- struct{}{}:
	default:
	}
}

func (e *Engine) chatRequest(r Request) (string, error) {
	if e.ctx.Err() != nil {
		return "", errors.New("客户端已关闭")
	}
	if r.Method == "chatList" {
		var p struct {
			Version string `json:"version"`
		}
		if len(r.Params) > 0 {
			if err := decode(r.Params, &p); err != nil {
				return "", err
			}
		}
		return e.chatStore.JSON(p.Version)
	}
	var p struct{ DeviceID, Text, Source, Name, ID, Destination string }
	if err := decode(r.Params, &p); err != nil {
		return "", err
	}
	if r.Method == "chatSaveFile" {
		m, ok := e.chatStore.Get(p.ID)
		if !ok || m.Direction != "incoming" || m.Kind != "file" || m.Status != "complete" || !chatFilePath(m.DeviceID, m.ID, m.Path) {
			return "", errors.New("文件尚未接收完成或不存在")
		}
		t, err := e.addTransfer(Transfer{Direction: "save", DeviceID: m.DeviceID, ShareID: chatInboxShare, Path: m.Path, Source: "inbox://" + m.Path, Destination: p.Destination})
		if err != nil {
			return "", err
		}
		return marshal(t)
	}
	if r.Method != "chatSendText" && r.Method != "chatSendFile" {
		return "", errors.New("未知会话操作")
	}
	c := e.config()
	if c.Token == "" || p.DeviceID == "" || p.DeviceID == c.Device.ID {
		return "", errors.New("请先选择已配对的其他设备")
	}
	e.mu.RLock()
	_, known := e.peers[p.DeviceID]
	e.mu.RUnlock()
	if !known {
		known = e.chatStore.HasDevice(p.DeviceID)
	}
	if !known {
		return "", errors.New("找不到该配对设备，请开启组网并同步设备列表")
	}
	now := time.Now().UTC()
	m := chat.Message{ID: secure.Random(18), DeviceID: p.DeviceID, Direction: "outgoing", Kind: "text", Text: p.Text, Status: "queued", Created: now, Updated: now, Revision: 1}
	if r.Method == "chatSendText" {
		if strings.TrimSpace(p.Text) == "" || utf8.RuneCountInString(p.Text) > 4096 {
			return "", errors.New("消息不能为空，最多4096个字符")
		}
		if err := e.chatStore.Put(m); err != nil {
			return "", err
		}
	} else {
		if p.Source == "" || p.Name == "" || len(p.Name) > 255 || strings.ContainsAny(p.Name, "/\\\x00") || p.Name == "." || p.Name == ".." {
			return "", errors.New("请选择有效文件")
		}
		m.Kind, m.Text, m.Name = "file", "", p.Name
		m.Path = path.Join(c.Device.ID, m.ID, p.Name)
		if err := e.chatStore.Put(m); err != nil {
			return "", err
		}
		t, err := e.addTransfer(Transfer{Direction: "upload", DeviceID: p.DeviceID, ShareID: chatInboxShare, Path: m.Path, Source: p.Source, ChatMessageID: m.ID})
		m.Revision++
		m.Updated = time.Now().UTC()
		if err != nil {
			m.Status, m.Error = "failed", err.Error()
		} else {
			m.TransferID = t.ID
		}
		if saveErr := e.chatStore.Put(m); saveErr != nil {
			return "", saveErr
		}
		if err != nil {
			return "", err
		}
	}
	e.wakeChat()
	return marshal(m)
}

func chatFilePath(sender, id, file string) bool {
	return file != "" && path.Dir(file) == sender+"/"+id && path.Base(file) != "." && !strings.ContainsAny(file, "\\\x00")
}

func (e *Engine) chatHandler(authorizer *secure.Authorizer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}
		sender, err := authorizer.Authorize(r.Context(), r)
		if err != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
		var wire wireChatMessage
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&wire); err != nil {
			http.Error(w, "invalid message", 400)
			return
		}
		var trailing any
		if err = decoder.Decode(&trailing); err != io.EOF {
			http.Error(w, "invalid message", 400)
			return
		}
		// A request may have authenticated before spending time reading its
		// body. Revocation during that interval must prevent committing it.
		if err = r.Context().Err(); err != nil {
			http.Error(w, "request cancelled", 408)
			return
		}
		if current, err := authorizer.Authorize(r.Context(), r); err != nil || current != sender {
			http.Error(w, "device access revoked", 401)
			return
		}
		m := chat.Message{ID: wire.ID, DeviceID: sender, Direction: "incoming", Kind: wire.Kind, Text: wire.Text, Name: wire.Name, Size: wire.Size, Completed: wire.Completed, Status: wire.Status, Path: wire.Path, Created: wire.Created, Updated: wire.Updated, Revision: wire.Revision}
		m.Updated = time.Now().UTC()
		if m.Kind == "text" {
			m.Status = "received"
			m.Path = ""
		} else if m.Kind == "file" && !chatFilePath(sender, m.ID, m.Path) {
			http.Error(w, "invalid attachment path", 400)
			return
		}
		if err = e.chatStore.Put(m); err != nil {
			http.Error(w, "invalid or conflicting message", 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(`{"received":true}`))
	})
}

func (e *Engine) chatLoop() {
	defer close(e.chatDone)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-e.chatWake:
		case <-ticker.C:
		}
		tasks := map[string]Transfer{}
		for _, task := range e.filteredTransferSnapshot(func(t *Transfer) bool { return t.ChatMessageID != "" }) {
			if task.ChatMessageID != "" {
				tasks[task.ChatMessageID] = task
			}
		}
		failedPeer := map[string]bool{}
		for _, m := range e.chatStore.WorkList() {
			if e.ctx.Err() != nil {
				return
			}
			if m.Direction != "outgoing" {
				continue
			}
			if m.Kind == "file" {
				if t, ok := tasks[m.ID]; ok {
					if m.TransferID != t.ID || m.Size != t.Size || m.Completed != t.Completed || m.Status != t.Status || m.Path != t.Path || m.Error != t.Error {
						m.TransferID, m.Size, m.Completed, m.Status, m.Path, m.Error = t.ID, t.Size, t.Completed, t.Status, t.Path, t.Error
						m.Revision++
						m.Updated = time.Now().UTC()
						if err := e.chatStore.Put(m); err != nil {
							e.setError(err)
							continue
						}
					}
				} else if m.TransferID == "" && time.Since(m.Created) > 10*time.Second && m.Status != "failed" {
					m.Status, m.Error = "failed", "传输创建被中断，请重新发送文件"
					m.Revision++
					m.Updated = time.Now().UTC()
					_ = e.chatStore.Put(m)
				}
			}
			if m.SyncedRevision >= m.Revision || failedPeer[m.DeviceID] {
				continue
			}
			wire := wireChatMessage{ID: m.ID, Kind: m.Kind, Text: m.Text, Name: m.Name, Size: m.Size, Completed: m.Completed, Status: m.Status, Path: m.Path, Created: m.Created, Updated: m.Updated, Revision: m.Revision}
			ctx, cancel := context.WithTimeout(e.ctx, 8*time.Second)
			_, err := e.fileJSON(ctx, m.DeviceID, "POST", "/v1/chat/messages", wire, nil)
			cancel()
			if err != nil {
				failedPeer[m.DeviceID] = true
				if m.Kind == "text" && (m.Status != "waiting" || m.Error != err.Error()) {
					m.Status, m.Error = "waiting", err.Error()
					m.Revision++
					m.Updated = time.Now().UTC()
					_ = e.chatStore.Put(m)
				}
				continue
			}
			if m.Kind == "text" && m.Status != "sent" {
				m.Status, m.Error = "sent", ""
				m.Revision++
				m.Updated = time.Now().UTC()
				if err = e.chatStore.Put(m); err != nil {
					e.setError(err)
					continue
				}
			}
			if err = e.chatStore.MarkSynced(m.ID, m.Revision); err != nil {
				e.setError(err)
			}
		}
	}
}
