package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/apiv2"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/jellycompat"
)

func TestCompatibilityListenerServesSignedArtwork(t *testing.T) {
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// "images" is a compatibility route word: key casing must remain untouched.
	key := "provider/images/poster/w500.rev.webp"
	if err := store.Put(t.Context(), key, []byte("artwork")); err != nil {
		t.Fatal(err)
	}
	signer := artworkurl.NewSigner("test-secret", time.Hour)
	router := jellycompat.NewRouter(jellycompat.Dependencies{Config: &config.Config{}, ArtworkHandler: apiv2.NewArtworkHandler(store, signer, nil)})
	u, _ := signer.Sign(key, time.Now())
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, u, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", method, rec.Code, rec.Body.String())
		}
		if method == http.MethodGet && rec.Body.String() != "artwork" {
			t.Fatalf("body=%q", rec.Body.String())
		}
	}
}

type noopABSMounter struct{}

func (noopABSMounter) Mount(chi.Router) {}

// The ABS cover handlers redirect to root-relative signed artwork URLs, so the
// ABS listener must answer them itself.
func TestAudiobookshelfListenerServesSignedArtwork(t *testing.T) {
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := "provider/audiobooks/cover/card.rev.webp"
	if err := store.Put(t.Context(), key, []byte("cover")); err != nil {
		t.Fatal(err)
	}
	signer := artworkurl.NewSigner("test-secret", time.Hour)
	srv := newAudiobookshelfListener(":0", noopABSMounter{}, apiv2.NewArtworkHandler(store, signer, nil), nil, nil)
	u, _ := signer.Sign(key, time.Now())
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, httptest.NewRequest(method, u, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", method, rec.Code, rec.Body.String())
		}
		if method == http.MethodGet && rec.Body.String() != "cover" {
			t.Fatalf("body=%q", rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v2/artwork/"+key, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unsigned artwork on the ABS listener: %d", rec.Code)
	}
	unmounted := newAudiobookshelfListener(":0", noopABSMounter{}, nil, nil, nil)
	rec = httptest.NewRecorder()
	unmounted.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, u, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("artwork route without a handler: %d", rec.Code)
	}
}
