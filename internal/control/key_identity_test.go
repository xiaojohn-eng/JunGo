package control

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func TestEnrollmentCanonicalizesPublicKeyIdentity(t *testing.T) {
	store, err := NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	key := testKey(42)
	// A trailing newline and noncanonical padding bits decode to identical
	// public key bytes with StdEncoding, as used by WireGuard enrollment.
	variant := key[:len(key)-2] + "B=\n"
	decoded, err := base64.StdEncoding.DecodeString(variant)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != key {
		t.Fatal("test must use two encodings of the same key")
	}
	code, _ := store.CreatePairing(time.Minute, now)
	first, err := store.Enroll(Enrollment{ServiceID: code.ServiceID, Code: code.Code, Name: "phone", PublicKey: variant}, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Device.PublicKey != key {
		t.Fatal("enrolled key not stored canonically")
	}
	secondCode, _ := store.CreatePairing(time.Minute, now)
	request := Enrollment{ServiceID: secondCode.ServiceID, Code: secondCode.Code, Name: "duplicate", PublicKey: key}
	if _, err := store.Enroll(request, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate key identity accepted: %v", err)
	}
	request.PublicKey = testKey(43)
	if _, err := store.Enroll(request, now); err != nil {
		t.Fatalf("invalid duplicate consumed pairing code: %v", err)
	}
}
