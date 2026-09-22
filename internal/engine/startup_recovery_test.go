package engine

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaojohn-eng/JunGo/internal/control"
	"github.com/xiaojohn-eng/JunGo/internal/relay"
	"github.com/xiaojohn-eng/JunGo/internal/secure"
)

func startupRecoveryFixture(t *testing.T, failureStatus ...int) (*Engine, *atomic.Bool, *atomic.Int32) {
	t.Helper()
	store, err := control.NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := control.NewHandler(store, control.Config{AdminToken: secure.Random(32)})
	if err != nil {
		t.Fatal(err)
	}
	rel, err := relay.NewHandler(store, relay.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rel.Close() })
	mux := http.NewServeMux()
	mux.Handle("/v1/relay", rel)
	mux.Handle("/", controller)
	status := http.StatusServiceUnavailable
	if len(failureStatus) > 0 {
		status = failureStatus[0]
	}
	var unavailable atomic.Bool
	var attempts atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/device" {
			attempts.Add(1)
			if unavailable.Load() {
				http.Error(w, "temporarily restarting", status)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	server.StartTLS()
	t.Cleanup(server.Close)
	fp := sha256.Sum256(server.Certificate().Raw)
	dir := filepath.Join(t.TempDir(), "device")
	initial, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := store.CreatePairing(time.Minute, time.Now())
	if err != nil {
		initial.Close()
		t.Fatal(err)
	}
	call(t, initial, "pair", map[string]any{"server": server.URL, "fingerprint": hex.EncodeToString(fp[:]), "serviceId": pair.ServiceID, "code": pair.Code, "name": "restored Mac"})
	c := initial.config()
	c.MeshEnabled = true
	if err = initial.commit(c); err != nil {
		initial.Close()
		t.Fatal(err)
	}
	if err = initial.Close(); err != nil {
		t.Fatal(err)
	}
	unavailable.Store(true)
	restored, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restored.Close() })
	wait(t, 3*time.Second, func() bool { return attempts.Load() == 1 && restored.State().Error != "" })
	if restored.State().MeshRunning || !restored.State().MeshEnabled {
		t.Fatal("startup failure lost enabled intent or ran unexpectedly")
	}
	return restored, &unavailable, &attempts
}

func TestStartupMeshRecoversAfterControllerRestart(t *testing.T) {
	e, unavailable, attempts := startupRecoveryFixture(t)
	unavailable.Store(false)
	// Node publication precedes completion of file-service initialization. Wait
	// for the recovery operation's completed observable state, not that midpoint.
	wait(t, 15*time.Second, func() bool { state := e.State(); return state.MeshRunning && state.Error == "" })
	if attempts.Load() != 2 {
		t.Fatalf("startup retries were not bounded: %d", attempts.Load())
	}
	if got := e.State().Error; got != "" {
		t.Fatalf("recovered startup retained connection error: %s", got)
	}
}

func TestStartupMeshRetryRespectsDisableAndClose(t *testing.T) {
	for _, action := range []string{"disable", "close"} {
		t.Run(action, func(t *testing.T) {
			e, unavailable, attempts := startupRecoveryFixture(t)
			if action == "disable" {
				call(t, e, "network", map[string]bool{"mesh": false})
			} else if err := e.Close(); err != nil {
				t.Fatal(err)
			}
			unavailable.Store(false)
			// Cover the production retry tick: this is a real restored Engine.New
			// goroutine, not a helper with a test-only global timer override.
			timer := time.NewTimer(11 * time.Second)
			defer timer.Stop()
			<-timer.C
			if attempts.Load() != 1 || e.State().MeshRunning {
				t.Fatal("explicit stop was resurrected by startup recovery")
			}
			if action == "disable" && e.config().MeshEnabled {
				t.Fatal("disable preference was overwritten")
			}
		})
	}
}

func TestStartupMeshDoesNotRetryRejectedIdentity(t *testing.T) {
	e, unavailable, attempts := startupRecoveryFixture(t, http.StatusUnauthorized)
	unavailable.Store(false)
	timer := time.NewTimer(11 * time.Second)
	defer timer.Stop()
	<-timer.C
	if attempts.Load() != 1 || e.State().MeshRunning {
		t.Fatal("rejected identity automatically reconnected")
	}
}
