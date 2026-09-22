package secure

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPinnedCertificateRejectsImpostor(t *testing.T) {
	cert, fp, e := Certificate(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	cfg, e := PinnedTLS(fp)
	if e != nil {
		t.Fatal(e)
	}
	leaf, e := x509.ParseCertificate(cert.Certificate[0])
	if e != nil {
		t.Fatal(e)
	}
	if e = cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}); e != nil {
		t.Fatal(e)
	}
	other, _, e := Certificate(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	impostor, _ := x509.ParseCertificate(other.Certificate[0])
	if cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{impostor}}) == nil {
		t.Fatal("accepted different identity")
	}
}
func TestFileIdentityReplayAndRevocation(t *testing.T) {
	a, _ := ecdh.X25519().GenerateKey(rand.Reader)
	b, _ := ecdh.X25519().GenerateKey(rand.Reader)
	ah, bh := hex.EncodeToString(a.Bytes()), hex.EncodeToString(b.Bytes())
	ap, bp := hex.EncodeToString(a.PublicKey().Bytes()), hex.EncodeToString(b.PublicKey().Bytes())
	key, e := PairKey(ah, bp)
	if e != nil {
		t.Fatal(e)
	}
	allowed := true
	auth := &Authorizer{PrivateKey: bh, Lookup: func(id string) (string, bool) { return ap, id == "phone" && allowed }}
	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, e := auth.Authorize(r.Context(), r); e != nil || id != "phone" {
			t.Fatalf("authorize %s %v", id, e)
		}
		allowed = false
		if _, e = auth.Authorize(r.Context(), r); e == nil {
			t.Fatal("stream did not notice revocation")
		}
		allowed = true
		w.WriteHeader(204)
	}))
	request := httptest.NewRequest("GET", "https://device/v1/files/shares", nil)
	Sign(request, "phone", key)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != 204 {
		t.Fatal(result.Code, result.Body.String())
	}
	result = httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != 429 {
		t.Fatal("replay accepted", result.Code)
	}
	request = httptest.NewRequest("GET", "https://device/v1/files/shares", nil)
	Sign(request, "phone", key)
	request.URL.Path = "/v1/files/download"
	result = httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != 401 {
		t.Fatal("changed route accepted")
	}
}
