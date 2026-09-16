package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/apiv2"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/jellycompat"
)

func TestCompatibilityListenerServesSignedArtwork(t *testing.T) {
	store, err := artworkstore.NewFilesystem(t.TempDir())
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
