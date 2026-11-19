package apiv2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
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

type posterPresignFake struct{}

func (posterPresignFake) PresignURL(_ context.Context, path, _ string) string {
	return "/api/v2/artwork/" + path + "?exp=1&sig=x"
}

type posterFetcherFake struct{ urls []string }

func (f *posterFetcherFake) Fetch(_ context.Context, _, rawURL string) (imagecache.RemoteImage, error) {
	f.urls = append(f.urls, rawURL)
	return imagecache.RemoteImage{Data: []byte("\x89PNG\r\n\x1a\nrest"), ContentType: "image/png"}, nil
}

func newPosterHandler(source ItemPosterSource, fetcher RemotePosterFetcher) (http.Handler, *artworkurl.ItemPosters) {
	posters := artworkurl.NewItemPosters("test-secret", time.Hour, posterPresignFake{})
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
		"loopback":  local.URL + "/poster.png",
		"localhost": "http://localhost" + port + "/poster.png",
		"private":   "http://10.0.0.1" + port + "/poster.png",
		"metadata":  "http://169.254.169.254/latest/meta-data",
		"file":      "file:///etc/passwd",
	}
	source := &posterSourceFake{paths: paths}
	h, posters := newPosterHandler(source, imagecache.NewRemoteFetcher())
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
	posters := artworkurl.NewItemPosters("test-secret", time.Hour, posterPresignFake{})
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
