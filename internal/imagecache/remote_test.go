package imagecache

import (
	"context"
	"errors"
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
		if r.URL.Path == "/huge.png" {
			_, _ = w.Write(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, remoteResultMaxBytes)...))
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
	// An image too large to keep is refused and remembered, not re-downloaded.
	for range 3 {
		if _, err := f.Fetch(ctx, "movie-3", srv.URL+"/huge.png"); !errors.Is(err, ErrRemoteTooLarge) {
			t.Fatalf("Fetch of an oversized image = %v, want ErrRemoteTooLarge", err)
		}
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("provider saw %d requests after a remembered oversized image, want 3", got)
	}
	hits.Store(2)
	// A poster that changed its source is a different download.
	if _, err := f.Fetch(ctx, "movie-1", srv.URL+"/other.png"); err != nil || hits.Load() != 3 {
		t.Fatalf("a new URL for the same item: err %v, provider requests %d, want 3", err, hits.Load())
	}
}

// A poster whose source changed while the old source is still downloading
// gets the new source's image, not a share of the old download.
func TestRemoteFetcherDoesNotShareAcrossSources(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old.png" {
			close(started)
			<-release
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nnew"))
	}))
	defer srv.Close()
	defer close(release)
	f := newRemoteFetcher(srv.Client())
	ctx := netguard.WithPrivateAccess(context.Background())

	go func() { _, _ = f.Fetch(ctx, "movie-1", srv.URL+"/old.png") }()
	<-started
	image, err := f.Fetch(ctx, "movie-1", srv.URL+"/new.png")
	if err != nil || string(image.Data) != "\x89PNG\r\n\x1a\nnew" {
		t.Fatalf("Fetch of the new source = %q, %v", image.Data, err)
	}
}
