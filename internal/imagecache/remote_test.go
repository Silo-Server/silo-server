package imagecache

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

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

// More large posters than the byte budget holds: the ones that lose their
// bytes answer busy until their window ends, and are not downloaded again.
func TestRemoteFetcherKeepsAMarkerForEvictedImages(t *testing.T) {
	f := newRemoteFetcher(http.DefaultClient)
	now := time.Now()
	big := RemoteImage{Data: make([]byte, remoteResultMaxBytes), ContentType: "image/png"}
	posters := remoteResultsBudget/remoteResultMaxBytes + 1
	for i := range posters {
		f.remember(strconv.Itoa(i), big, nil, now.Add(time.Duration(i)*time.Second))
	}
	if f.bytes > remoteResultsBudget {
		t.Fatalf("memory holds %d bytes, over its %d budget", f.bytes, remoteResultsBudget)
	}
	evicted, ok := f.recent("0", now.Add(time.Minute))
	if !ok || !errors.Is(evicted.err, ErrRemoteBusy) || len(evicted.image.Data) != 0 {
		t.Fatalf("the evicted poster = %v, found %v; want a busy marker", evicted.err, ok)
	}
	if kept, ok := f.recent(strconv.Itoa(posters-1), now.Add(time.Minute)); !ok || kept.err != nil {
		t.Fatalf("the newest poster = %v, found %v; want its image", kept.err, ok)
	}
}

// A full memory must refuse new artwork instead of forgetting an outcome:
// cycling more links than it holds would otherwise repeat every download.
func TestRemoteFetcherRefusesNewArtworkWhenFull(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nrest"))
	}))
	defer srv.Close()
	f := newRemoteFetcher(srv.Client())
	ctx := netguard.WithPrivateAccess(context.Background())

	until := time.Now().Add(time.Hour)
	f.results = make(map[string]remoteResult, remoteResultsMax)
	for i := range remoteResultsMax {
		f.results["movie-"+strconv.Itoa(i)+"\x00"+srv.URL+"/poster.png"] = remoteResult{err: ErrNotImage, until: until}
	}
	if _, err := f.Fetch(ctx, "movie-new", srv.URL+"/poster.png"); !errors.Is(err, ErrRemoteBusy) {
		t.Fatalf("Fetch of new artwork with a full memory = %v, want ErrRemoteBusy", err)
	}
	if _, err := f.Fetch(ctx, "movie-0", srv.URL+"/poster.png"); !errors.Is(err, ErrNotImage) {
		t.Fatalf("Fetch of remembered artwork = %v, want its remembered outcome", err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("provider saw %d requests with a full memory, want 0", got)
	}
}

// Evicting stored posters read via FetchBytes must drop them cleanly so
// revisiting them reloads from the store instead of returning ErrRemoteBusy,
// and stored catalog walks must neither evict nor starve provider downloads.
func TestRemoteFetcherFetchBytesEvictionReloadsAndPreservesProviders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nprovider"))
	}))
	defer srv.Close()

	f := newRemoteFetcher(srv.Client())
	ctx := netguard.WithPrivateAccess(context.Background())

	// Seed a provider download first so we can verify stored reads never evict it.
	if img, err := f.Fetch(ctx, "provider-1", srv.URL+"/p.png"); err != nil || img.ContentType != "image/png" {
		t.Fatalf("seed provider Fetch = %q, %v", img.ContentType, err)
	}

	// Fill past remoteResultsBudget with stored FetchBytes entries.
	png := make([]byte, remoteResultMaxBytes)
	copy(png, "\x89PNG\r\n\x1a\n")
	var loads atomic.Int32
	load := func(_ context.Context, _ int64) ([]byte, error) {
		loads.Add(1)
		return png, nil
	}
	posters := remoteResultsBudget/remoteResultMaxBytes + 2
	for i := range posters {
		if _, err := f.FetchBytes(ctx, "movie-"+strconv.Itoa(i), "store:k"+strconv.Itoa(i), load); err != nil {
			t.Fatalf("FetchBytes(%d) = %v, want nil", i, err)
		}
	}
	if f.bytes > remoteResultsBudget {
		t.Fatalf("memory holds %d bytes, over its %d budget", f.bytes, remoteResultsBudget)
	}
	// Revisiting the earliest evicted stored poster reloads cleanly instead of ErrRemoteBusy.
	before := loads.Load()
	if img, err := f.FetchBytes(ctx, "movie-0", "store:k0", load); err != nil || img.ContentType != "image/png" {
		t.Fatalf("revisit evicted stored poster = %q, %v; want image/png", img.ContentType, err)
	}
	if loads.Load() != before+1 {
		t.Fatalf("expected evicted stored poster to reload once, loads delta = %d", loads.Load()-before)
	}
	// The seeded provider poster must still be in memory with its bytes, not ErrRemoteBusy.
	providerEntry, ok := f.recent("provider-1\x00"+srv.URL+"/p.png", time.Now())
	if !ok || providerEntry.err != nil || len(providerEntry.image.Data) == 0 {
		t.Fatalf("provider entry after stored walk = %v (found=%v, bytes=%d), want intact image", providerEntry.err, ok, len(providerEntry.image.Data))
	}

	// A memory full of evictable stored entries must still admit both stored reads and provider fetches.
	f2 := newRemoteFetcher(srv.Client())
	smallLoad := func(_ context.Context, _ int64) ([]byte, error) {
		return []byte("\x89PNG\r\n\x1a\nsmall"), nil
	}
	for i := range remoteResultsMax {
		if _, err := f2.FetchBytes(ctx, "s-"+strconv.Itoa(i), "store:k"+strconv.Itoa(i), smallLoad); err != nil {
			t.Fatalf("seed stored entry %d = %v, want nil", i, err)
		}
	}
	if _, err := f2.FetchBytes(ctx, "s-new", "store:new", smallLoad); err != nil {
		t.Fatalf("FetchBytes with memory full of stored entries = %v, want nil", err)
	}
	if _, err := f2.Fetch(ctx, "p-new", srv.URL+"/new.png"); err != nil {
		t.Fatalf("Fetch with memory full of stored entries = %v, want nil", err)
	}

	// An uncacheable stored image (provider entries leave less than its size)
	// must succeed without flushing existing cached stored posters.
	f3 := newRemoteFetcher(srv.Client())
	now := time.Now()
	// Fill 63 MiB with provider entries, leaving 1 MiB for stored posters.
	for i := range 15 {
		f3.remember("p-"+strconv.Itoa(i), RemoteImage{Data: make([]byte, 4<<20), ContentType: "image/png"}, nil, now)
	}
	f3.remember("p-15", RemoteImage{Data: make([]byte, 3<<20), ContentType: "image/png"}, nil, now)
	smallStored := make([]byte, 512<<10)
	copy(smallStored, "\x89PNG\r\n\x1a\n")
	if _, err := f3.FetchBytes(ctx, "s-keep", "store:keep", func(_ context.Context, _ int64) ([]byte, error) {
		return smallStored, nil
	}); err != nil {
		t.Fatalf("seed small stored poster = %v, want nil", err)
	}
	tooBigToCache := make([]byte, 2<<20)
	copy(tooBigToCache, "\x89PNG\r\n\x1a\n")
	if img, err := f3.FetchBytes(ctx, "s-uncacheable", "store:uncacheable", func(_ context.Context, _ int64) ([]byte, error) {
		return tooBigToCache, nil
	}); err != nil || len(img.Data) != len(tooBigToCache) {
		t.Fatalf("uncacheable stored poster = %d bytes, %v; want %d bytes", len(img.Data), err, len(tooBigToCache))
	}
	if kept, ok := f3.recent("s-keep\x00store:keep", time.Now()); !ok || len(kept.image.Data) != len(smallStored) {
		t.Fatalf("existing stored poster was flushed by uncacheable image: ok=%v, bytes=%d", ok, len(kept.image.Data))
	}
}
