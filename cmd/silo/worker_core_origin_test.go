package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/Silo-Server/silo-server/internal/workerregistration"
)

func TestWorkerCoreOriginAcceptsSavedPublicURLSpellings(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		publicURL string
		want      string
		allowHTTP bool
	}{
		{"https://silo.example.test", "https://silo.example.test", false},
		{"https://Silo.Example.Test/", "https://silo.example.test", false},
		{"https://silo.example.test:443", "https://silo.example.test", false},
		{"http://silo.example.test:80", "http://silo.example.test", true},
		{"http://silo.example.test:08097", "http://silo.example.test:8097", true},
		{"HTTP://Silo.Example.Test:8097", "http://silo.example.test:8097", true},
	} {
		t.Run(tc.publicURL, func(t *testing.T) {
			origin, allowHTTP, err := workerCoreOrigin(tc.publicURL)
			if err != nil || origin != tc.want || allowHTTP != tc.allowHTTP {
				t.Fatalf("workerCoreOrigin(%q) = %q, %v, %v; want %q, %v", tc.publicURL, origin, allowHTTP, err, tc.want, tc.allowHTTP)
			}
			// Startup is fatal when the enrollment handler rejects the origin.
			if _, err := workerregistration.NewHandler(workerregistration.Options{Repository: workerregistration.New(nil, nil), Origin: origin, PrivateKey: key, AllowHTTP: allowHTTP}); err != nil {
				t.Fatalf("enrollment handler rejected %q: %v", origin, err)
			}
		})
	}
	if _, _, err := workerCoreOrigin("https://silo.example.test/prefix"); err == nil {
		t.Fatal("prefixed public URL must keep using the no-worker fallback")
	}
}
