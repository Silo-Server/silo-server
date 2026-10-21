package lantls

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

var testMasterKey = []byte(strings.Repeat("k", 48))

func TestNewIsDeterministicPerMasterKey(t *testing.T) {
	first, err := New(testMasterKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	second, err := New(testMasterKey)
	if err != nil {
		t.Fatalf("New again: %v", err)
	}
	if first.Pin() != second.Pin() {
		t.Fatalf("pin changed across processes with one SECRET_KEY: %q vs %q", first.Pin(), second.Pin())
	}

	other, err := New([]byte(strings.Repeat("r", 48)))
	if err != nil {
		t.Fatalf("New with another key: %v", err)
	}
	if other.Pin() == first.Pin() {
		t.Fatal("a different SECRET_KEY must yield a different pin")
	}
}

func TestPinIsTheCertificateSPKIHash(t *testing.T) {
	identity, err := New(testMasterKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	leaf := identity.certificate.Leaf
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	if want := base64.StdEncoding.EncodeToString(sum[:]); identity.Pin() != want {
		t.Fatalf("Pin = %q, want %q", identity.Pin(), want)
	}
	if _, ok := identity.certificate.PrivateKey.(*ecdsa.PrivateKey); !ok {
		t.Fatalf("private key is %T, want *ecdsa.PrivateKey", identity.certificate.PrivateKey)
	}
}

func TestNewRejectsShortMasterKey(t *testing.T) {
	if _, err := New([]byte("short")); err == nil {
		t.Fatal("New accepted a master key below the SECRET_KEY floor")
	}
}

func TestConfigServesHTTP1Only(t *testing.T) {
	identity, err := New(testMasterKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	config := identity.Config()
	if len(config.NextProtos) != 1 || config.NextProtos[0] != alpnHTTP1 {
		t.Fatalf("NextProtos = %v, want [http/1.1]", config.NextProtos)
	}
}
