package engine

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaojohn-eng/JunGo/internal/chat"
	"github.com/xiaojohn-eng/JunGo/internal/secure"
)

type chatRevocationReader struct {
	io.Reader
	revoke func()
}

func (r chatRevocationReader) Read(p []byte) (int, error) {
	r.revoke()
	return r.Reader.Read(p)
}

func TestChatRevocationDuringRequestBodyPreventsDelivery(t *testing.T) {
	store, err := chat.New(filepath.Join(t.TempDir(), "state", "chat.json"))
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{chatStore: store}
	a, _ := ecdh.X25519().GenerateKey(rand.Reader)
	b, _ := ecdh.X25519().GenerateKey(rand.Reader)
	allowed := true
	auth := &secure.Authorizer{PrivateKey: hex.EncodeToString(b.Bytes()), Lookup: func(id string) (string, bool) {
		return hex.EncodeToString(a.PublicKey().Bytes()), id == "sender" && allowed
	}}
	key, err := secure.PairKey(hex.EncodeToString(a.Bytes()), hex.EncodeToString(b.PublicKey().Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(wireChatMessage{ID: "new-message", Kind: "text", Text: "should not be delivered", Revision: 1, Created: time.Now(), Status: "queued"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/chat/messages", chatRevocationReader{Reader: bytes.NewReader(body), revoke: func() { allowed = false }})
	secure.Sign(r, "sender", key)
	w := httptest.NewRecorder()
	auth.Middleware(e.chatHandler(auth)).ServeHTTP(w, r)
	if w.Code != 401 || len(store.List()) != 0 {
		t.Fatalf("revoked in-flight message was accepted: status=%d, messages=%d", w.Code, len(store.List()))
	}
}
