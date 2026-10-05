package control

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Config struct {
	AdminToken string
	PairingTTL time.Duration
	Now        func() time.Time
}

// NewHandler returns the control API. The embedding server must enforce TLS;
// HTTP is only suitable on loopback for tests or behind a trusted TLS proxy.
func NewHandler(store *Store, cfg Config) (http.Handler, error) {
	if store == nil || len(cfg.AdminToken) < 32 {
		return nil, errors.New("control requires a store and admin token of at least 32 bytes")
	}
	if cfg.PairingTTL == 0 {
		cfg.PairingTTL = 10 * time.Minute
	}
	if cfg.PairingTTL <= 0 || cfg.PairingTTL > 24*time.Hour {
		return nil, ErrInvalid
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	adminHash := sha256.Sum256([]byte(cfg.AdminToken))
	isAdmin := func(r *http.Request) bool {
		h := sha256.Sum256([]byte(BearerToken(r)))
		return subtle.ConstantTimeCompare(adminHash[:], h[:]) == 1
	}
	mux := http.NewServeMux()
	var streamMu sync.Mutex
	streamCounts := make(map[string]int)
	streamTotal := 0
	mux.HandleFunc("GET /v1/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"service_id": store.ServiceID(), "version": 1})
	})
	mux.HandleFunc("POST /v1/pairing-codes", func(w http.ResponseWriter, r *http.Request) {
		if !isAdmin(r) {
			apiError(w, ErrUnauthorized)
			return
		}
		code, err := store.CreatePairing(cfg.PairingTTL, cfg.Now())
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, code)
	})
	mux.HandleFunc("POST /v1/enroll", func(w http.ResponseWriter, r *http.Request) {
		var req Enrollment
		if !decodeJSON(w, r, &req) {
			return
		}
		result, err := store.Enroll(req, cfg.Now())
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, result)
	})
	mux.HandleFunc("GET /v1/device", func(w http.ResponseWriter, r *http.Request) {
		d, err := store.Authenticate(BearerToken(r))
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, d)
	})
	mux.HandleFunc("POST /v1/device/name", func(w http.ResponseWriter, r *http.Request) {
		token := BearerToken(r)
		if _, err := store.Authenticate(token); err != nil {
			apiError(w, err)
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		d, err := store.RenameDevice(token, req.Name)
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, d)
	})
	mux.HandleFunc("GET /v1/peers", func(w http.ResponseWriter, r *http.Request) {
		peers, err := store.Peers(BearerToken(r))
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"peers": peers})
	})
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		token := BearerToken(r)
		device, err := store.Authenticate(token)
		if err != nil {
			apiError(w, err)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		streamMu.Lock()
		if streamTotal >= 128 || streamCounts[device.ID] >= 4 {
			streamMu.Unlock()
			http.Error(w, "event stream limit reached", http.StatusTooManyRequests)
			return
		}
		streamCounts[device.ID]++
		streamTotal++
		streamMu.Unlock()
		defer func() {
			streamMu.Lock()
			streamCounts[device.ID]--
			if streamCounts[device.ID] == 0 {
				delete(streamCounts, device.ID)
			}
			streamTotal--
			streamMu.Unlock()
		}()
		events, unsubscribe := store.SubscribeEvents()
		defer unsubscribe()
		if _, err = store.Authenticate(token); err != nil {
			apiError(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")
		controller := http.NewResponseController(w)
		send := func(kind string, event Event) bool {
			_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
			body, _ := json.Marshal(event)
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, body); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}
		if !send("ready", Event{Type: "ready", DeviceID: device.ID}) {
			return
		}
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case event := <-events:
				if _, err = store.Authenticate(token); err != nil {
					send("revoked", Event{Type: "revoked", DeviceID: device.ID})
					return
				}
				event.Type = "peers-changed"
				if !send(event.Type, event) {
					return
				}
			case <-ticker.C:
				if _, err = store.Authenticate(token); err != nil {
					send("revoked", Event{Type: "revoked", DeviceID: device.ID})
					return
				}
				if !send("heartbeat", Event{Type: "heartbeat", DeviceID: device.ID}) {
					return
				}
			}
		}
	})
	mux.HandleFunc("GET /v1/devices", func(w http.ResponseWriter, r *http.Request) {
		if !isAdmin(r) {
			apiError(w, ErrUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"devices": store.ListDevices()})
	})
	mux.HandleFunc("POST /v1/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		if _, err := store.Authenticate(BearerToken(r)); err != nil {
			apiError(w, err)
			return
		}
		var req Heartbeat
		if !decodeJSON(w, r, &req) {
			return
		}
		d, err := store.Heartbeat(BearerToken(r), req, cfg.Now())
		if err != nil {
			apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, d)
	})
	mux.HandleFunc("POST /v1/devices/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		if !isAdmin(r) {
			apiError(w, ErrUnauthorized)
			return
		}
		if err := store.Revoke(r.PathValue("id"), cfg.Now()); err != nil {
			apiError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	}), nil
}

func BearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if strings.ContainsAny(token, " \t\r\n") {
		return ""
	}
	return token
}

func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		apiError(w, ErrInvalid)
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		apiError(w, ErrInvalid)
		return false
	}
	return true
}

func apiError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "internal error"
	switch {
	case errors.Is(err, ErrUnauthorized):
		status = http.StatusUnauthorized
		message = ErrUnauthorized.Error()
	case errors.Is(err, ErrPairing):
		status = http.StatusForbidden
		message = ErrPairing.Error()
	case errors.Is(err, ErrInvalid):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, ErrNotFound):
		status = http.StatusNotFound
		message = ErrNotFound.Error()
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
