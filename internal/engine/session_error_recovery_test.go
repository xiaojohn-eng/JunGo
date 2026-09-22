package engine

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaojohn-eng/JunGo/internal/control"
)

func TestSessionRecoveryClearsOnlyItsOwnError(t *testing.T) {
	var blocked atomic.Bool
	var targetToken atomic.Value
	targetToken.Store("")
	var successfulPeers atomic.Int32
	a, b, store := fixture(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ownPeerRequest := r.URL.Path == "/v1/peers" && control.BearerToken(r) == targetToken.Load().(string)
			if ownPeerRequest && blocked.Load() {
				http.Error(w, "controller restarting", http.StatusServiceUnavailable)
				return
			}
			next.ServeHTTP(w, r)
			if ownPeerRequest {
				successfulPeers.Add(1)
			}
		})
	})
	targetToken.Store(a.config().Token)
	call(t, a, "network", map[string]bool{"mesh": true})
	call(t, b, "network", map[string]bool{"mesh": true})
	peerID := b.config().Device.ID
	wait(t, 8*time.Second, func() bool { return len(a.State().Peers) == 1 && len(b.State().Peers) == 1 && a.State().Error == "" })
	blocked.Store(true)
	wait(t, 8*time.Second, func() bool { return strings.Contains(a.State().Error, "controller restarting") })
	// Publish a fresh actual peer heartbeat so recovery proves that the new
	// authenticated peer snapshot was applied, not merely that the UI hid an error.
	marker := time.Now().UTC()
	b.mu.RLock()
	endpoints := b.node.LocalEndpoints()
	b.mu.RUnlock()
	_, err := store.Heartbeat(b.config().Token, control.Heartbeat{Endpoints: endpoints, FileURL: "https://" + b.config().Device.IP + ":8443", FileTLSFingerprint: b.certFingerprint}, marker)
	if err != nil {
		t.Fatal(err)
	}
	blocked.Store(false)
	wait(t, 12*time.Second, func() bool {
		state := a.State()
		if !state.MeshRunning || state.Error != "" {
			return false
		}
		for _, peer := range state.Peers {
			if peer.ID == peerID && !peer.LastSeen.Before(marker) {
				return true
			}
		}
		return false
	})
	// Keep another subsystem's error through two subsequent real successful
	// control requests. That ensures the prior recovery callback has run.
	other := errors.New("test: local disk checkpoint failed")
	a.setError(other)
	before := successfulPeers.Load()
	wait(t, 12*time.Second, func() bool { return successfulPeers.Load() >= before+2 })
	if got := a.State().Error; got != other.Error() {
		t.Fatalf("healthy session cleared another operation's error: %q", got)
	}
}

func TestErrorRecoveryOwnershipIncludesIdenticalText(t *testing.T) {
	e := &Engine{}
	first := e.setError(errors.New("same message"))
	second := e.setError(errors.New("same message"))
	e.clearErrorIfCurrent(first)
	e.mu.RLock()
	message, revision := e.lastError, e.errorRevision
	e.mu.RUnlock()
	if message != "same message" || revision != second {
		t.Fatal("stale recovery cleared another owner's identical error")
	}
	e.clearErrorIfCurrent(second)
	e.mu.RLock()
	message = e.lastError
	e.mu.RUnlock()
	if message != "" {
		t.Fatal("current owner could not clear its recovered error")
	}
}
