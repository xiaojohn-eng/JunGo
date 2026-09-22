// Package secure contains device-local key storage, certificate pinning and
// pairwise file authorization. Controller bearer tokens never reach peers.
package secure

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func Random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func WriteFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".jungo-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func Certificate(dir string) (tls.Certificate, string, error) {
	certPath, keyPath := filepath.Join(dir, "identity.crt"), filepath.Join(dir, "identity.key")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return tls.Certificate{}, "", err
	}
	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	if errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist) {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return tls.Certificate{}, "", err
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return tls.Certificate{}, "", err
		}
		tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "JunGo device"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(5, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost"}}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
		if err != nil {
			return tls.Certificate{}, "", err
		}
		key, err := x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			return tls.Certificate{}, "", err
		}
		if err = WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})); err != nil {
			return tls.Certificate{}, "", err
		}
		if err = WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
			return tls.Certificate{}, "", err
		}
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return cert, "", err
	}
	digest := sha256.Sum256(cert.Certificate[0])
	return cert, hex.EncodeToString(digest[:]), nil
}

func PinnedTLS(fingerprint string) (*tls.Config, error) {
	expected, err := hex.DecodeString(strings.ReplaceAll(fingerprint, ":", ""))
	if err != nil || len(expected) != 32 {
		return nil, errors.New("证书指纹必须是64位SHA-256十六进制值")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, // verified explicitly by trusted enrollment fingerprint, not public PKI
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("server supplied no certificate")
			}
			cert := cs.PeerCertificates[0]
			actual := sha256.Sum256(cert.Raw)
			if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
				return errors.New("服务器证书指纹不匹配")
			}
			if now := time.Now(); now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
				return errors.New("服务器证书已过期或尚未生效")
			}
			return nil
		}}, nil
}

func keyBytes(s string) ([]byte, error) {
	if len(s) == 64 {
		if b, e := hex.DecodeString(s); e == nil {
			return b, nil
		}
	}
	b, e := base64.StdEncoding.DecodeString(s)
	if e != nil || len(b) != 32 {
		return nil, errors.New("invalid WireGuard key")
	}
	return b, nil
}
func PairKey(private, public string) ([]byte, error) {
	a, e := keyBytes(private)
	if e != nil {
		return nil, e
	}
	b, e := keyBytes(public)
	if e != nil {
		return nil, e
	}
	priv, e := ecdh.X25519().NewPrivateKey(a)
	if e != nil {
		return nil, e
	}
	pub, e := ecdh.X25519().NewPublicKey(b)
	if e != nil {
		return nil, e
	}
	shared, e := priv.ECDH(pub)
	if e != nil {
		return nil, e
	}
	mac := hmac.New(sha256.New, shared)
	mac.Write([]byte("jungo/files/auth/v1"))
	return mac.Sum(nil), nil
}

func canonical(r *http.Request, stamp, nonce string) string {
	return strings.Join([]string{r.Method, r.URL.RequestURI(), stamp, nonce, r.Header.Get("Upload-Offset"), r.Header.Get("Range"), r.Header.Get("If-Match")}, "\n")
}
func Sign(r *http.Request, id string, key []byte) {
	stamp, nonce := strconv.FormatInt(time.Now().Unix(), 10), Random(18)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(canonical(r, stamp, nonce)))
	r.Header.Set("X-Jungo-Device", id)
	r.Header.Set("X-Jungo-Time", stamp)
	r.Header.Set("X-Jungo-Nonce", nonce)
	r.Header.Set("Authorization", "Jungo "+base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
}

type proofContextKey struct{}
type proof struct {
	ID      string
	KeyHash [32]byte
}
type Authorizer struct {
	PrivateKey string
	Lookup     func(id string) (publicKey string, allowed bool)
	mu         sync.Mutex
	seen       map[string]time.Time
}

// Middleware validates a fresh request once, then file streaming can repeatedly
// call Authorize to recheck revocation without treating the same request as replay.
func (a *Authorizer) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Jungo-Device")
		pub, ok := a.Lookup(id)
		if !ok {
			http.Error(w, "unknown or revoked device", 401)
			return
		}
		key, e := PairKey(a.PrivateKey, pub)
		if e != nil {
			http.Error(w, "invalid identity", 401)
			return
		}
		stamp, nonce := r.Header.Get("X-Jungo-Time"), r.Header.Get("X-Jungo-Nonce")
		sec, e := strconv.ParseInt(stamp, 10, 64)
		if e != nil || len(nonce) < 16 || len(nonce) > 64 || time.Since(time.Unix(sec, 0)).Abs() > 60*time.Second {
			http.Error(w, "expired file authentication", 401)
			return
		}
		provided, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(r.Header.Get("Authorization"), "Jungo "))
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(canonical(r, stamp, nonce)))
		if e != nil || !hmac.Equal(provided, mac.Sum(nil)) {
			http.Error(w, "invalid file authentication", 401)
			return
		}
		a.mu.Lock()
		if a.seen == nil {
			a.seen = make(map[string]time.Time)
		}
		now := time.Now()
		for k, t := range a.seen {
			if now.Sub(t) > 2*time.Minute {
				delete(a.seen, k)
			}
		}
		_, replay := a.seen[id+":"+nonce]
		full := len(a.seen) >= 32768
		if !replay && !full {
			a.seen[id+":"+nonce] = now
		}
		a.mu.Unlock()
		if replay || full {
			http.Error(w, "replayed request or authentication rate limit", 429)
			return
		}
		ctx := context.WithValue(r.Context(), proofContextKey{}, proof{ID: id, KeyHash: sha256.Sum256(key)})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
func (a *Authorizer) Authorize(ctx context.Context, r *http.Request) (string, error) {
	p, ok := ctx.Value(proofContextKey{}).(proof)
	if !ok {
		return "", errors.New("unverified device request")
	}
	pub, ok := a.Lookup(p.ID)
	if !ok {
		return "", errors.New("device revoked")
	}
	key, e := PairKey(a.PrivateKey, pub)
	if e != nil {
		return "", e
	}
	if sha256.Sum256(key) != p.KeyHash {
		return "", fmt.Errorf("device key changed")
	}
	return p.ID, nil
}
