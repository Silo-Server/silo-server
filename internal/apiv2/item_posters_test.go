package apiv2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
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

func newPosterHandler(source ItemPosterSource, fetcher RemotePosterFetcher) (http.Handler, *artworkurl.ItemPosters) {
	posters := artworkurl.NewItemPosters("test-secret", time.Hour, &posterPresignFake{})
	return NewHandler(Dependencies{ItemPosters: posters, ItemPosterSource: source, RemotePosters: fetcher}), posters
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
	source := &posterSourceFake{paths: map[string]string{
		"movie-cached": "tmdb/movies/1/poster/original.r1.webp",
		"movie-remote": "https://image.example/remote.jpg",
		"movie-none":   "",
	}}
	fetcher := &posterFetcherFake{}
	h, posters := newPosterHandler(source, fetcher)

	cached := do(t, h, http.MethodGet, posters.Link("movie-cached", time.Now()), "", nil)
	if cached.Code != http.StatusFound || cached.Header().Get("Location") != "/api/v2/artwork/tmdb/movies/1/poster/w300.r1.webp?exp=1&sig=x" ||
		cached.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cached: %d %v", cached.Code, cached.Header())
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

	// Once cached, the same link redirects.
	source.paths["movie-remote"] = "tmdb/movies/2/poster/original.r2.webp"
	if again := do(t, h, http.MethodGet, link, "", nil); again.Code != http.StatusFound {
		t.Fatalf("after caching: %d", again.Code)
	}
	requireProblem(t, do(t, h, http.MethodGet, posters.Link("movie-none", time.Now()), "", nil), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, posters.Link("movie-gone", time.Now()), "", nil), TypeNotFound)
}

// The admin poster rule: cached art keeps its stored URL, anything else is a
// poster link, and only no poster at all is empty.
func TestItemPostersURLRule(t *testing.T) {
	posters := artworkurl.NewItemPosters("test-secret", time.Hour, &posterPresignFake{})
	ctx := t.Context()
	if got := posters.URL(ctx, "movie-a", "tmdb/movies/1/poster/original.r1.webp"); !strings.HasPrefix(got, "/api/v2/artwork/tmdb/movies/1/poster/w300.r1.webp") {
		t.Fatalf("cached = %q", got)
	}
	for _, path := range []string{"https://image.tmdb.org/t/p/original/a.jpg", "metadb://images/a/original.jpg"} {
		if got := posters.URL(ctx, "movie-a", path); !strings.HasPrefix(got, artworkurl.ItemPosterRoute+"movie-a?exp=") {
			t.Fatalf("%s = %q", path, got)
		}
	}
	for _, path := range []string{"", "-"} {
		if got := posters.URL(ctx, "movie-a", path); got != "" {
			t.Fatalf("%q = %q", path, got)
		}
	}
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
