package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

type fakeDirectLibraryResolver struct {
	reference string
	identity  artworkurl.LibraryIdentity
}

func (f *fakeDirectLibraryResolver) ResolveFile(_ context.Context, reference string, identity artworkurl.LibraryIdentity, _ string) (metadata.DirectLibraryArtworkFile, error) {
	f.reference = reference
	f.identity = identity
	file, err := os.CreateTemp("", "artwork-handler-test")
	if err != nil {
		return metadata.DirectLibraryArtworkFile{}, err
	}
	if _, err := file.WriteString("sidecar-original"); err != nil {
		return metadata.DirectLibraryArtworkFile{}, err
	}
	_, _ = file.Seek(0, 0)
	return metadata.DirectLibraryArtworkFile{File: file, Fingerprint: identity.Fingerprint, MediaType: "image/jpeg", Size: 16}, nil
}

func TestDirectLibraryArtworkHandlerServesOnlySignedOpaqueIdentity(t *testing.T) {
	signer, err := artworkurl.NewSigner("test-secret", func() time.Duration { return time.Hour })
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	identity := artworkurl.LibraryIdentity{Surface: "item posters", Keys: []string{"movie-1"}, Fingerprint: strings.Repeat("b", 64)}
	reference, err := signer.LibraryReference(identity)
	if err != nil {
		t.Fatalf("LibraryReference: %v", err)
	}
	signed, err := signer.SignLibraryReference(reference, time.Now())
	if err != nil {
		t.Fatalf("SignLibraryReference: %v", err)
	}
	parsed, err := url.Parse(signed.URL)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	resolver := &fakeDirectLibraryResolver{}
	handler := NewDirectLibraryArtworkHandler(resolver, signer)
	router := chi.NewRouter()
	router.Get("/api/v1/artwork-library/{"+DirectLibraryArtworkParam+"}", handler.ServeHTTP)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, signed.URL, nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "sidecar-original" {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
	if resolver.reference != reference || resolver.identity.Fingerprint != identity.Fingerprint {
		t.Fatalf("resolver received %q %#v", resolver.reference, resolver.identity)
	}
	if strings.Contains(parsed.Path, "movie-1") || strings.Contains(parsed.Path, "file://") {
		t.Fatalf("route exposed target/source details: %q", parsed.Path)
	}

	// Substitute a character guaranteed to differ from the original so the
	// tamper is real even when the signature already ends with the substitute.
	flipped := "A"
	if strings.HasSuffix(signed.URL, "A") {
		flipped = "B"
	}
	tampered := signed.URL[:len(signed.URL)-1] + flipped
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tampered, nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("tampered status = %d, want 404", recorder.Code)
	}
}
