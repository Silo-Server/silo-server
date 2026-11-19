package imagecache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/netguard"
)

// A link needs no credentials, so repeating it must not repeat the provider
// download, whether the download succeeded or failed.
func TestRemoteFetcherRemembersAnOutcome(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/missing.png" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nrest"))
	}))
	defer srv.Close()
	f := newRemoteFetcher(srv.Client())
	ctx := netguard.WithPrivateAccess(context.Background())

	for range 3 {
		image, err := f.Fetch(ctx, "movie-1", srv.URL+"/poster.png")
		if err != nil || image.ContentType != "image/png" {
			t.Fatalf("Fetch = %q, %v", image.ContentType, err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("provider saw %d requests for one poster, want 1", got)
	}
	for range 3 {
		if _, err := f.Fetch(ctx, "movie-2", srv.URL+"/missing.png"); err == nil {
			t.Fatal("Fetch of a missing image succeeded")
		}
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("provider saw %d requests after a remembered failure, want 2", got)
	}
	// A poster that changed its source is a different download.
	if _, err := f.Fetch(ctx, "movie-1", srv.URL+"/other.png"); err != nil || hits.Load() != 3 {
		t.Fatalf("a new URL for the same item: err %v, provider requests %d, want 3", err, hits.Load())
	}
}
