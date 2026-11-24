package apiv2

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/blobstore/blobstoretest"
	"github.com/Silo-Server/silo-server/internal/imagecache"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

type posterSourceFake struct {
	paths    map[string]string
	reads    atomic.Int32
	enqueues atomic.Int32
}

func (s *posterSourceFake) ItemPosterPath(_ context.Context, id string) (string, error) {
	s.reads.Add(1)
	path, ok := s.paths[id]
	if !ok {
		return "", metadata.ErrItemNotFound
	}
	return path, nil
}

func (s *posterSourceFake) EnqueueItemPosterCache(context.Context, string) error {
	s.enqueues.Add(1)
	return nil
}

type posterPresignFake struct{ calls int }

func (p *posterPresignFake) PresignURL(ctx context.Context, path, _ string) string {
	p.calls++
	if ctx.Err() != nil {
		return ""
	}
	return "/api/v2/artwork/" + path + "?exp=1&sig=x"
}

type posterFetcherFake struct{ urls []string }

func (f *posterFetcherFake) Fetch(_ context.Context, _, rawURL string) (imagecache.RemoteImage, error) {
	f.urls = append(f.urls, rawURL)
	return imagecache.RemoteImage{Data: []byte("\x89PNG\r\n\x1a\nrest"), ContentType: "image/png"}, nil
}

func (f *posterFetcherFake) FetchBytes(ctx context.Context, _, _ string, load func(context.Context, int64) ([]byte, error)) (imagecache.RemoteImage, error) {
	data, err := load(ctx, 4<<20)
	if err != nil {
		return imagecache.RemoteImage{}, err
	}
	ct, ok := imagecache.ServedImageType(data)
	if !ok {
		return imagecache.RemoteImage{}, imagecache.ErrNotImage
	}
	return imagecache.RemoteImage{Data: data, ContentType: ct}, nil
}

func newPosterHandler(source ItemPosterSource, fetcher RemotePosterFetcher) (http.Handler, *artworkurl.ItemPosters) {
	return newPosterHandlerWithStore(source, fetcher, nil)
}

func newPosterHandlerWithStore(source ItemPosterSource, fetcher RemotePosterFetcher, store blobstore.Store) (http.Handler, *artworkurl.ItemPosters) {
	posters := artworkurl.NewItemPosters("test-secret", time.Hour, &posterPresignFake{})
	return NewHandler(Dependencies{ItemPosters: posters, ItemPosterSource: source, RemotePosters: fetcher, ArtworkStore: store}), posters
}

// A poster link authorizes one item until it expires; nothing else about the
// request can make the server read or fetch.
func TestItemPosterRejectsLinksItDidNotIssue(t *testing.T) {
	source := &posterSourceFake{paths: map[string]string{"movie-a": "https://image.example/a.jpg", "movie-b": "https://image.example/b.jpg"}}
	fetcher := &posterFetcherFake{}
	h, posters := newPosterHandler(source, fetcher)
	valid := posters.Link("movie-a", time.Now())
	_, query, _ := strings.Cut(valid, "?")
	forOther := posters.Link("movie-a", time.Now())
	forOther = strings.Replace(forOther, "/movie-a?", "/movie-b?", 1)
	for name, u := range map[string]string{
		"missing query":     Prefix + "/item-posters/movie-a",
		"missing signature": strings.Split(valid, "&sig=")[0],
		"bad signature":     valid + "x",
		"expired":           posters.Link("movie-a", time.Now().Add(-3*time.Hour)),
		"other item":        forOther,
		"other secret":      artworkurl.NewItemPosters("other-secret", time.Hour, nil).Link("movie-a", time.Now()),
		"artwork signature": Prefix + "/item-posters/movie-a?" + strings.SplitN(artworkSignature(t, "movie-a"), "?", 2)[1],
		"nested path":       Prefix + "/item-posters/movie-a/x?" + query,
	} {
		t.Run(name, func(t *testing.T) {
			requireProblem(t, do(t, h, http.MethodGet, u, "", nil), TypeNotFound)
		})
	}
	if n := source.reads.Load(); n != 0 {
		t.Fatalf("unauthorized catalog reads = %d", n)
	}
	if len(fetcher.urls) != 0 {
		t.Fatalf("unauthorized fetches = %v", fetcher.urls)
	}
}

func artworkSignature(t *testing.T, key string) string {
	t.Helper()
	u, _ := artworkurl.NewSigner("test-secret", time.Hour).Sign(key, time.Now())
	return u
}

// A poster path the server's guard refuses is never requested, whatever the
// item's stored URL says, and the answer does not say why.
func TestItemPosterRefusesLocalNetworkPosterPaths(t *testing.T) {
	var hits atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n"))
	}))
	defer local.Close()
	port := local.URL[strings.LastIndex(local.URL, ":"):]
	paths := map[string]string{
		"store":     local.URL + "/artwork/poster.png",
		"loopback":  local.URL + "/poster.png",
		"localhost": "http://localhost" + port + "/poster.png",
		"private":   "http://10.0.0.1" + port + "/poster.png",
		"metadata":  "http://169.254.169.254/latest/meta-data",
		"file":      "file:///etc/passwd",
	}
	source := &posterSourceFake{paths: paths}
	posters := artworkurl.NewItemPosters("test-secret", time.Hour, &posterPresignFake{})
	h := NewHandler(Dependencies{
		ItemPosters: posters, ItemPosterSource: source, RemotePosters: imagecache.NewRemoteFetcher(),
		ArtworkStore: blobstoretest.NewDirect(local.URL + "/artwork"),
	})
	for id := range paths {
		t.Run(id, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, posters.Link(id, time.Now()), nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			requireProblem(t, rec, TypeNotFound)
		})
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("local server was requested %d times", n)
	}
}

func TestItemPosterServesCurrentPoster(t *testing.T) {
	store := blobstoretest.New()
	cachedBytes1 := []byte("\x89PNG\r\n\x1a\ncached-1")
	cachedBytes2 := []byte("\x89PNG\r\n\x1a\ncached-2")
	if err := store.Put(t.Context(), "tmdb/movies/1/poster/w300.r1.webp", cachedBytes1); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(t.Context(), "tmdb/movies/2/poster/w300.r2.webp", cachedBytes2); err != nil {
		t.Fatal(err)
	}
	source := &posterSourceFake{paths: map[string]string{
		"movie-cached": "tmdb/movies/1/poster/original.r1.webp",
		"movie-remote": "https://image.example/remote.jpg",
		"movie-none":   "",
	}}
	fetcher := &posterFetcherFake{}
	h, posters := newPosterHandlerWithStore(source, fetcher, store)

	cached := do(t, h, http.MethodGet, posters.Link("movie-cached", time.Now()), "", nil)
	if cached.Code != http.StatusOK || cached.Header().Get("Content-Type") != "image/png" ||
		cached.Header().Get("Cache-Control") != itemPosterProxyCache ||
		cached.Header().Get("X-Content-Type-Options") != "nosniff" ||
		cached.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" ||
		cached.Body.String() != string(cachedBytes1) {
		t.Fatalf("cached: %d %v %q", cached.Code, cached.Header(), cached.Body.String())
	}

	link := posters.Link("movie-remote", time.Now())
	for range 2 {
		remote := do(t, h, http.MethodGet, link, "", nil)
		if remote.Code != http.StatusOK || remote.Header().Get("Content-Type") != "image/png" ||
			remote.Header().Get("Cache-Control") != itemPosterProxyCache || remote.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("remote: %d %v", remote.Code, remote.Header())
		}
	}
	if len(fetcher.urls) != 2 || fetcher.urls[0] != "https://image.example/remote.jpg" {
		t.Fatalf("fetched %v", fetcher.urls)
	}
	// Polling the link queues the cache job once per window, not per request.
	if n := source.enqueues.Load(); n != 1 {
		t.Fatalf("cache jobs queued %d times, want 1", n)
	}

	// Once cached, the same link serves bytes directly from the store instead of redirecting.
	source.paths["movie-remote"] = "tmdb/movies/2/poster/original.r2.webp"
	again := do(t, h, http.MethodGet, link, "", nil)
	if again.Code != http.StatusOK || again.Header().Get("Content-Type") != "image/png" ||
		again.Body.String() != string(cachedBytes2) {
		t.Fatalf("after caching: %d %v %q", again.Code, again.Header(), again.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, posters.Link("movie-none", time.Now()), "", nil), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, posters.Link("movie-gone", time.Now()), "", nil), TypeNotFound)
}

type staticPresignFake struct{ url string }

func (p staticPresignFake) PresignURL(context.Context, string, string) string { return p.url }

// The admin poster rule: local storage keeps its rooted /api/v2/artwork/...
// path; off-server storage URLs (S3/CDN) and uncached paths get a signed
// item-posters link; only no poster at all is empty.
func TestItemPostersURLRule(t *testing.T) {
	ctx := t.Context()
	localPosters := artworkurl.NewItemPosters("test-secret", time.Hour, &posterPresignFake{})
	if got := localPosters.URL(ctx, "movie-a", "tmdb/movies/1/poster/original.r1.webp"); !strings.HasPrefix(got, "/api/v2/artwork/tmdb/movies/1/poster/w300.r1.webp") {
		t.Fatalf("local cached = %q", got)
	}
	for name, presigned := range map[string]string{
		"s3 direct":       "https://s3.example/bucket/tmdb/movies/1/poster/w300.r1.webp?X-Amz-Signature=abc",
		"scheme-relative": "//cdn.example/tmdb/movies/1/poster/w300.r1.webp",
	} {
		t.Run(name, func(t *testing.T) {
			posters := artworkurl.NewItemPosters("test-secret", time.Hour, staticPresignFake{url: presigned})
			got := posters.URL(ctx, "movie-a", "tmdb/movies/1/poster/original.r1.webp")
			if !strings.HasPrefix(got, artworkurl.ItemPosterRoute+"movie-a?exp=") {
				t.Fatalf("off-server cached (%s) = %q, want item-posters link", name, got)
			}
		})
	}
	for _, path := range []string{"https://image.tmdb.org/t/p/original/a.jpg", "metadb://images/a/original.jpg"} {
		if got := localPosters.URL(ctx, "movie-a", path); !strings.HasPrefix(got, artworkurl.ItemPosterRoute+"movie-a?exp=") {
			t.Fatalf("%s = %q", path, got)
		}
	}
	for _, path := range []string{"", "-"} {
		if got := localPosters.URL(ctx, "movie-a", path); got != "" {
			t.Fatalf("%q = %q", path, got)
		}
	}
}

type countingStore struct {
	blobstore.Store
	gets atomic.Int32
	err  error
}

func (s *countingStore) Get(ctx context.Context, key string) (io.ReadCloser, blobstore.ObjectInfo, error) {
	s.gets.Add(1)
	if s.err != nil {
		return nil, blobstore.ObjectInfo{}, s.err
	}
	return s.Store.Get(ctx, key)
}

// Stored poster reads share RemoteFetcher's memory, 4 MiB cap, byte-sniffed
// Content-Type, key validation, and storage-error mapping.
func TestItemPosterStoredArtBounds(t *testing.T) {
	base := blobstoretest.New()
	png := []byte("\x89PNG\r\n\x1a\nbytes")
	if err := base.Put(t.Context(), "tmdb/movies/1/poster/w300.r1.webp", png); err != nil {
		t.Fatal(err)
	}
	if err := base.Put(t.Context(), "tmdb/movies/2/poster/w300.r1.webp", []byte("<html>not an image</html>")); err != nil {
		t.Fatal(err)
	}
	huge := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 4<<20)...)
	if err := base.Put(t.Context(), "tmdb/movies/3/poster/w300.r1.webp", huge); err != nil {
		t.Fatal(err)
	}
	store := &countingStore{Store: base}
	source := &posterSourceFake{paths: map[string]string{
		"ok":       "tmdb/movies/1/poster/original.r1.webp",
		"not-img":  "tmdb/movies/2/poster/original.r1.webp",
		"too-big":  "tmdb/movies/3/poster/original.r1.webp",
		"missing":  "tmdb/movies/4/poster/original.r1.webp",
		"unserved": "backups/server/db.dump",
		"invalid":  "tmdb/../secret.webp",
	}}
	posters := artworkurl.NewItemPosters("test-secret", time.Hour, staticPresignFake{url: "https://s3.example/bucket/w300.webp"})
	h := NewHandler(Dependencies{
		ItemPosters: posters, ItemPosterSource: source,
		ArtworkStore: store, RemotePosters: imagecache.NewRemoteFetcher(),
	})

	// Valid image is sniffed from bytes (PNG bytes under .webp key -> image/png)
	// and remembered in RemoteFetcher so a second request does not re-read the store.
	link := posters.Link("ok", time.Now())
	for range 2 {
		rec := do(t, h, http.MethodGet, link, "", nil)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" ||
			rec.Header().Get("X-Content-Type-Options") != "nosniff" ||
			rec.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" ||
			rec.Header().Get("Cache-Control") != itemPosterProxyCache ||
			rec.Body.String() != string(png) {
			t.Fatalf("ok: %d %v", rec.Code, rec.Header())
		}
	}
	if gets := store.gets.Load(); gets != 1 {
		t.Fatalf("store reads for remembered poster = %d, want 1", gets)
	}

	// Non-image, >4 MiB, missing, operational namespace, and invalid keys all answer 404.
	for _, id := range []string{"not-img", "too-big", "missing", "unserved", "invalid"} {
		t.Run(id, func(t *testing.T) {
			requireProblem(t, do(t, h, http.MethodGet, posters.Link(id, time.Now()), "", nil), TypeNotFound)
		})
	}
	// unserved and invalid must never reach store.Get (1 ok + 1 not-img + 1 too-big + 1 missing = 4).
	if gets := store.gets.Load(); gets != 4 {
		t.Fatalf("store reads = %d, want 4 (invalid/unserved must not hit store)", gets)
	}

	// Storage outage returns 503.
	failing := &countingStore{Store: base, err: errors.New("s3 timeout")}
	hFail := NewHandler(Dependencies{
		ItemPosters: posters, ItemPosterSource: source,
		ArtworkStore: failing, RemotePosters: imagecache.NewRemoteFetcher(),
	})
	requireProblem(t, do(t, hFail, http.MethodGet, posters.Link("ok", time.Now()), "", nil), TypeDependencyUnavailable)

	// Unconfigured store returns 503.
	hNoStore := NewHandler(Dependencies{
		ItemPosters: posters, ItemPosterSource: source,
		RemotePosters: imagecache.NewRemoteFetcher(),
	})
	requireProblem(t, do(t, hNoStore, http.MethodGet, posters.Link("ok", time.Now()), "", nil), TypeDependencyUnavailable)
}

// A link needs no credentials, so a plugin's answer for a poster path must be
// reused, and a full memo must refuse new paths instead of forgetting one:
// either gap lets a link holder make the plugin work on every request.
func TestPosterResolveMemoBoundsPluginCalls(t *testing.T) {
	var memo posterResolveMemo
	now := time.Now()
	for i := range itemPosterResolveMax {
		memo.put("tmdb://poster/"+strconv.Itoa(i), "https://images.example/"+strconv.Itoa(i), now)
	}
	if url, ok, _ := memo.get("tmdb://poster/7", now.Add(time.Second)); !ok || url != "https://images.example/7" {
		t.Fatalf("remembered path = %q, %v, want its answer", url, ok)
	}
	if _, ok, full := memo.get("tmdb://poster/new", now.Add(time.Second)); ok || !full {
		t.Fatalf("new path with a full memo: ok=%v full=%v, want refused", ok, full)
	}
	if _, ok, full := memo.get("tmdb://poster/new", now.Add(itemPosterResolveMemo)); ok || full {
		t.Fatalf("new path after expiry: ok=%v full=%v, want room", ok, full)
	}
	// A path whose plugin ran out of time answers busy, then is asked again.
	memo.putBusy("tmdb://poster/slow", now.Add(itemPosterResolveMemo))
	if _, ok, busy := memo.get("tmdb://poster/slow", now.Add(itemPosterResolveMemo+time.Second)); ok || !busy {
		t.Fatalf("path after a plugin timeout: ok=%v busy=%v, want busy", ok, busy)
	}
	if _, ok, busy := memo.get("tmdb://poster/slow", now.Add(itemPosterResolveMemo+itemPosterResolveRetry)); ok || busy {
		t.Fatalf("path after the retry wait: ok=%v busy=%v, want a new plugin call", ok, busy)
	}
}

func TestItemPosterReusesPluginAnswerAfterDisconnect(t *testing.T) {
	presigner := &posterPresignFake{}
	posters := artworkurl.NewItemPosters("test-secret", time.Hour, presigner)
	source := &posterSourceFake{paths: map[string]string{"movie-plugin": "tmdb://poster/1"}}
	h := NewHandler(Dependencies{ItemPosters: posters, ItemPosterSource: source})
	link := posters.Link("movie-plugin", time.Now())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, link, nil))
	if presigner.calls != 1 {
		t.Fatalf("canceled request made %d plugin calls, want 1", presigner.calls)
	}
	next := do(t, h, http.MethodGet, link, "", nil)
	if next.Code != http.StatusFound || presigner.calls != 1 {
		t.Fatalf("second request: status %d, location %q, plugin calls %d", next.Code, next.Header().Get("Location"), presigner.calls)
	}
}

type schemeRelativePresignFake struct{}

func (schemeRelativePresignFake) PresignURL(context.Context, string, string) string {
	return "//images.example/poster.jpg"
}

// A plugin answer of "//host/..." names another host, not a path on this
// server, so the route must not redirect a tool that cannot reach it there.
func TestItemPosterDoesNotRedirectToSchemeRelativeAnswer(t *testing.T) {
	posters := artworkurl.NewItemPosters("test-secret", time.Hour, schemeRelativePresignFake{})
	source := &posterSourceFake{paths: map[string]string{"movie-plugin": "tmdb://poster/1"}}
	h := NewHandler(Dependencies{ItemPosters: posters, ItemPosterSource: source, RemotePosters: &posterFetcherFake{}})
	requireProblem(t, do(t, h, http.MethodGet, posters.Link("movie-plugin", time.Now()), "", nil), TypeNotFound)
}

// A plugin call in flight holds a place in the memo, so concurrent first
// requests for distinct posters cannot start more calls than it has room for.
func TestPosterResolveMemoCountsCallsInFlight(t *testing.T) {
	var memo posterResolveMemo
	now := time.Now()
	if _, ok, busy := memo.reserve("tmdb://poster/first", now); ok || busy {
		t.Fatalf("first reservation: ok=%v busy=%v, want a place", ok, busy)
	}
	for i := range itemPosterResolveMax - 1 {
		memo.put("tmdb://poster/"+strconv.Itoa(i), "https://images.example/"+strconv.Itoa(i), now)
	}
	if _, ok, busy := memo.reserve("tmdb://poster/second", now); ok || !busy {
		t.Fatalf("reservation with the memo full of answers and calls: ok=%v busy=%v, want busy", ok, busy)
	}
	memo.release()
	if _, ok, busy := memo.reserve("tmdb://poster/second", now); ok || busy {
		t.Fatalf("reservation after the call finished: ok=%v busy=%v, want a place", ok, busy)
	}
}
