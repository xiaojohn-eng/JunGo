package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaojohn-eng/JunGo/internal/chat"
)

func TestPrivateChatTextFileAndSave(t *testing.T) {
	a, b, _ := fixture(t)
	call(t, a, "network", map[string]bool{"mesh": true})
	call(t, b, "network", map[string]bool{"mesh": true})
	wait(t, 15*time.Second, func() bool { return len(a.State().Peers) == 1 && len(b.State().Peers) == 1 })
	var sent chat.Message
	if err := json.Unmarshal([]byte(call(t, a, "chatSendText", map[string]string{"deviceId": b.State().Device.ID, "text": "手机、Mac 与服务器互发消息"})), &sent); err != nil {
		t.Fatal(err)
	}
	wait(t, 45*time.Second, func() bool { m, ok := a.chatStore.Get(sent.ID); return ok && m.Status == "sent" })
	received, ok := b.chatStore.Get(sent.ID)
	if !ok || received.Text != sent.Text || received.DeviceID != a.State().Device.ID || received.Direction != "incoming" || received.Status != "received" {
		t.Fatalf("invalid received text: %+v", received)
	}
	// The authenticated sender, not a caller supplied device ID, owns the row.
	wire := wireChatMessage{ID: sent.ID, Kind: "text", Text: sent.Text, Status: "queued", Created: sent.Created, Updated: sent.Updated, Revision: 1}
	for i := 0; i < 2; i++ {
		if _, err := a.fileJSON(context.Background(), b.State().Device.ID, "POST", "/v1/chat/messages", wire, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(b.chatStore.List()) != 1 {
		t.Fatal("retry duplicated a message")
	}
	wire.Text = "forged edit"
	wire.Revision = 2
	if _, err := a.fileJSON(context.Background(), b.State().Device.ID, "POST", "/v1/chat/messages", wire, nil); err == nil {
		t.Fatal("message identity could be rewritten")
	}
	payload := bytes.Repeat([]byte("chat-file-content\n"), 16000)
	source := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(source, payload, 0600); err != nil {
		t.Fatal(err)
	}
	var attachment chat.Message
	if err := json.Unmarshal([]byte(call(t, a, "chatSendFile", map[string]string{"deviceId": b.State().Device.ID, "source": source, "name": "聊天文件.txt"})), &attachment); err != nil {
		t.Fatal(err)
	}
	wait(t, 2*time.Minute, func() bool {
		m, ok := b.chatStore.Get(attachment.ID)
		return ok && m.Status == "complete" && m.Completed == int64(len(payload))
	})
	dest := filepath.Join(t.TempDir(), "saved.txt")
	var saved Transfer
	if err := json.Unmarshal([]byte(call(t, b, "chatSaveFile", map[string]string{"id": attachment.ID, "destination": dest})), &saved); err != nil {
		t.Fatal(err)
	}
	wait(t, 15*time.Second, func() bool {
		for _, task := range b.transferSnapshot() {
			if task.ID == saved.ID {
				if task.Status == "failed" {
					t.Fatal(task.Error)
				}
				return task.Status == "complete"
			}
		}
		return false
	})
	actual, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(actual, payload) {
		t.Fatal("saved chat attachment differs", err)
	}
	if len(a.chatStore.List()) != 2 || len(b.chatStore.List()) != 2 {
		t.Fatal("unexpected conversation history")
	}
}

func TestOfflineChatIsDurableAndDeliveredAfterReconnect(t *testing.T) {
	a, b, _ := fixture(t)
	call(t, a, "network", map[string]bool{"mesh": true})
	call(t, b, "network", map[string]bool{"mesh": true})
	wait(t, 15*time.Second, func() bool { return len(a.State().Peers) == 1 && len(b.State().Peers) == 1 })
	call(t, b, "network", map[string]bool{"mesh": false})
	var m chat.Message
	json.Unmarshal([]byte(call(t, a, "chatSendText", map[string]string{"deviceId": b.State().Device.ID, "text": "设备恢复后继续投递"})), &m)
	if _, ok := a.chatStore.Get(m.ID); !ok {
		t.Fatal("outgoing message not persisted")
	}
	call(t, b, "network", map[string]bool{"mesh": true})
	wait(t, 45*time.Second, func() bool { received, ok := b.chatStore.Get(m.ID); return ok && received.Text == m.Text })
	if _, err := b.Request(`{"method":"chatSaveFile","params":{"id":"missing","destination":"/tmp/should-not-exist"}}`); err == nil {
		t.Fatal("unknown attachment saved")
	}
}
