package tmdb

import (
	"context"

	"errors"

	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"

	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient returns a client with the test key whose requests go to a
// test server running handler.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := NewClient("test-key", 1000)
	t.Cleanup(client.Close)
	client.SetBaseURL(server.URL)
	return client
}

func TestRequestErrorsLeaveOutTheAPIKey(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	baseURL := server.URL
	// Nothing listens once the server is closed, so the request fails in
	// transport and the error names the URL it requested.
	server.Close()

	client := NewClient("secret-test-key", 1000)
	client.SetBaseURL(baseURL)

	_, err := client.GetCollectionPreset(context.Background(), "trending", "all", "week", 10)
	if err == nil {
		t.Fatal("GetCollectionPreset returned no error for an unreachable server")
	}
	if strings.Contains(err.Error(), "secret-test-key") {
		t.Fatalf("error carries the API key: %v", err)
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("error %v does not wrap a *url.Error", err)
	}
	if want := baseURL + "/trending/all/week"; urlErr.URL != want {
		t.Fatalf("error URL = %q, want %q", urlErr.URL, want)
	}
}

// countingLimiter counts the requests that wait on it.
type countingLimiter struct{ waits atomic.Int32 }

func (l *countingLimiter) Wait(context.Context) error {
	l.waits.Add(1)
	return nil
}

// A shared budget holds only if every request spends from it, so a retry
// after TMDB throttles waits on the limiter like the first attempt.
func TestRetryWaitsOnTheLimiter(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(server.Close)
	limiter := &countingLimiter{}
	client := NewClientWithLimiter("test-key", limiter)
	t.Cleanup(client.Close)
	client.SetBaseURL(server.URL)

	if _, err := client.SearchMedia(t.Context(), "movie", "Alien", 1); err != nil {
		t.Fatalf("SearchMedia: %v", err)
	}
	if got, want := limiter.waits.Load(), requests.Load(); got != want || want != 2 {
		t.Fatalf("%d requests waited on the limiter %d times, want 2 and 2", want, got)
	}
}

// A shared lookup outlives the caller that started it while another caller
// still waits, so a closed page does not fail a collection sync that joined
// it; once every caller has given up, the fetch stops too, so it does not
// keep waiting for the request budget.
func TestSharedFetchEndsWithItsLastCaller(t *testing.T) {
	client := NewClient("test-key", 1000)
	t.Cleanup(client.Close)

	t.Run("a caller giving up leaves the others their result", func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		fetch := func(ctx context.Context) (any, error) {
			close(started)
			select {
			case <-release:
				return "ids", nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		first, cancelFirst := context.WithCancel(t.Context())
		firstDone := make(chan error, 1)
		go func() {
			_, err := client.sharedFetch(first, "k1", fetch)
			firstDone <- err
		}()
		<-started
		secondDone := make(chan any, 1)
		go func() {
			v, err := client.sharedFetch(t.Context(), "k1", func(context.Context) (any, error) {
				t.Error("second caller started its own fetch instead of joining")
				return nil, nil
			})
			if err != nil {
				t.Errorf("second caller: %v", err)
			}
			secondDone <- v
		}()
		waitUntilWaiters(t, client, "k1", 2)
		cancelFirst()
		if err := <-firstDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("first caller returned %v, want context.Canceled", err)
		}
		close(release)
		if got := <-secondDone; got != "ids" {
			t.Fatalf("second caller got %v, want the shared result", got)
		}
	})

	t.Run("the fetch stops once every caller has given up", func(t *testing.T) {
		fetchEnded := make(chan error, 1)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = client.sharedFetch(ctx, "k2", func(fetchCtx context.Context) (any, error) {
				<-fetchCtx.Done()
				fetchEnded <- fetchCtx.Err()
				return nil, fetchCtx.Err()
			})
		}()
		waitUntilWaiters(t, client, "k2", 1)
		cancel()
		<-done
		select {
		case err := <-fetchEnded:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("fetch ended with %v, want it canceled", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("fetch kept running after its last caller gave up")
		}
	})
}

// waitUntilWaiters waits until n callers are waiting on key's shared fetch.
func waitUntilWaiters(t *testing.T, c *Client, key string, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		c.flightsMu.Lock()
		f := c.flights[key]
		got := 0
		if f != nil {
			got = f.waiters
		}
		c.flightsMu.Unlock()
		if got == n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("%d callers waiting on %s, want %d", got, key, n)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestCanceledRequestErrorKeepsItsCause(t *testing.T) {
	arrived := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-r.Context().Done()
	}))
	defer server.Close()

	client := NewClient("secret-test-key", 1000)
	client.SetBaseURL(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-arrived
		cancel()
	}()
	_, err := client.GetCollectionPreset(ctx, "trending", "all", "week", 10)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if strings.Contains(err.Error(), "secret-test-key") {
		t.Fatalf("error carries the API key: %v", err)
	}
}

// TestGetCertificationCallerStopsWaitingOnCancel pins the DoChan behavior: a
// caller whose context dies mid-flight returns promptly with ctx.Err() while
// the shared detached fetch continues for the surviving waiters.
func TestGetCertificationCallerStopsWaitingOnCancel(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"iso_3166_1":"US","rating":"TV-PG"}]}`))
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := client.GetCertification(ctx, "tv", 1396)
		errCh <- err
	}()
	<-started
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled caller error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled caller still blocked on the shared fetch")
	}

	// The detached fetch is still completable for a fresh caller.
	close(release)
	if cert, err := client.GetCertification(context.Background(), "tv", 1396); err != nil || cert != "TV-PG" {
		t.Fatalf("surviving caller cert = %q, %v; want TV-PG", cert, err)
	}
}

// TestGetCertificationSurvivesFirstCallerCancellation pins the singleflight
// context fix: the shared fetch runs detached from the initiating caller, so
// that caller disconnecting must not poison the result for concurrent waiters.
func TestGetCertificationSurvivesFirstCallerCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"iso_3166_1":"US","rating":"TV-PG"}]}`))
	})

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstErr := make(chan error, 1)
	go func() {
		_, err := client.GetCertification(firstCtx, "tv", 1396)
		firstErr <- err
	}()
	<-started

	secondCert := make(chan string, 1)
	secondErr := make(chan error, 1)
	go func() {
		cert, err := client.GetCertification(context.Background(), "tv", 1396)
		secondCert <- cert
		secondErr <- err
	}()
	// Let the second caller pile onto the in-flight singleflight key, then
	// kill the initiating caller's context before the upstream responds.
	time.Sleep(50 * time.Millisecond)
	cancelFirst()
	time.Sleep(50 * time.Millisecond)
	close(release)

	if err := <-secondErr; err != nil {
		t.Fatalf("second caller failed after first caller cancelled: %v", err)
	}
	if cert := <-secondCert; cert != "TV-PG" {
		t.Fatalf("second caller cert = %q, want TV-PG", cert)
	}
	<-firstErr // first caller may or may not error; just reap it
}

// blockedDetailServer answers /movie/129 once release is closed. started is
// closed when the first request arrives; calls counts every request.
func blockedDetailServer(t *testing.T) (client *Client, started <-chan struct{}, release func(), calls *atomic.Int32) {
	t.Helper()
	startedCh := make(chan struct{})
	releaseCh := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	calls = new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		startOnce.Do(func() { close(startedCh) })
		<-releaseCh
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": 129, "title": "Spirited Away"}`))
	}))
	release = func() { releaseOnce.Do(func() { close(releaseCh) }) }
	client = NewClient("key", 1000)
	client.SetBaseURL(server.URL)
	t.Cleanup(func() {
		release()
		server.Close()
		client.Close()
	})
	return client, startedCh, release, calls
}

// waitingCtx closes waiting the first time its caller selects on Done. A
// follower does that only once it has joined the in-flight fetch.
type waitingCtx struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
}

func newWaitingCtx(parent context.Context) *waitingCtx {
	return &waitingCtx{Context: parent, waiting: make(chan struct{})}
}

func (c *waitingCtx) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

type detailResult struct {
	detail *MediaDetail
	err    error
}

func getMediaDetailAsync(ctx context.Context, client *Client) <-chan detailResult {
	out := make(chan detailResult, 1)
	go func() {
		detail, err := client.GetMediaDetail(ctx, "movie", 129)
		out <- detailResult{detail, err}
	}()
	return out
}

func awaitDetail(t *testing.T, who string, ch <-chan detailResult) detailResult {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(5 * time.Second):
		t.Fatalf("%s is still blocked on the shared detail fetch", who)
		return detailResult{}
	}
}

// The caller that starts a shared detail fetch may disconnect; everyone else
// waiting on the same title still gets it.
func TestGetMediaDetailSurvivesLeaderCancellation(t *testing.T) {
	client, started, release, calls := blockedDetailServer(t)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	leader := getMediaDetailAsync(leaderCtx, client)
	<-started

	followerCtx := newWaitingCtx(context.Background())
	follower := getMediaDetailAsync(followerCtx, client)
	select {
	case <-followerCtx.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("follower never waited on its own context")
	}

	cancelLeader()
	if got := awaitDetail(t, "canceled leader", leader); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("leader err = %v, want context.Canceled", got.err)
	}
	release()
	got := awaitDetail(t, "follower", follower)
	if got.err != nil || got.detail.Title != "Spirited Away" {
		t.Fatalf("follower = %+v, %v; want the detail after the leader canceled", got.detail, got.err)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1 shared fetch", calls.Load())
	}
}

// A waiting caller that disconnects stops waiting at once, and the shared
// fetch still completes for the caller that started it.
func TestGetMediaDetailFollowerStopsWaitingOnCancel(t *testing.T) {
	client, started, release, calls := blockedDetailServer(t)

	leader := getMediaDetailAsync(context.Background(), client)
	<-started

	followerCtx, cancelFollower := context.WithCancel(context.Background())
	defer cancelFollower()
	waiting := newWaitingCtx(followerCtx)
	follower := getMediaDetailAsync(waiting, client)
	select {
	case <-waiting.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("follower never waited on its own context")
	}

	cancelFollower()
	if got := awaitDetail(t, "canceled follower", follower); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("follower err = %v, want context.Canceled", got.err)
	}
	release()
	got := awaitDetail(t, "leader", leader)
	if got.err != nil || got.detail.Title != "Spirited Away" {
		t.Fatalf("leader = %+v, %v; want the detail after the follower canceled", got.detail, got.err)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1 shared fetch", calls.Load())
	}
}

// A collection is fetched in the language asked for, the way the TMDB
// metadata plugin asks for titles: English as en-US, any other language as
// given, and no language at all when none is asked for.
func TestGetCollectionLanguage(t *testing.T) {
	for _, tc := range []struct{ language, sent string }{
		{"de", "de"},
		{"en", "en-US"},
		{"", ""},
	} {
		t.Run(tc.language, func(t *testing.T) {
			var got url.Values
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query()
				_, _ = w.Write([]byte(`{"id":10,"name":"Star Wars - Saga","poster_path":"/fr.jpg","parts":[]}`))
			})
			if _, err := client.GetCollection(context.Background(), 10, tc.language); err != nil {
				t.Fatal(err)
			}
			if lang := got.Get("language"); lang != tc.sent {
				t.Errorf("language = %q, want %q", lang, tc.sent)
			}
		})
	}
}

// The detail carries the fields outside-title embedding text is built from,
// for both of TMDB's keyword shapes.
func TestGetMediaDetailDecodesListsKeywordsAndCrew(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("append_to_response"); !strings.Contains(got, "similar") {
			t.Errorf("%s append_to_response = %q, want similar", r.URL.Path, got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/movie/603":
			_, _ = w.Write([]byte(`{"id": 603, "title": "The Matrix", "original_language": "en", "vote_count": 27000, "popularity": 81.5,
				"keywords": {"keywords": [{"id": 1, "name": "hacker"}, {"id": 2, "name": "simulated reality"}]},
				"credits": {"crew": [
					{"name": "Lana Wachowski", "job": "Director", "department": "Directing"},
					{"name": "Lilly Wachowski", "job": "Director", "department": "Directing"},
					{"name": "Lana Wachowski", "job": "Writer", "department": "Writing"},
					{"name": "Lilly Wachowski", "job": "Writer", "department": "Writing"},
					{"name": "Lana Wachowski", "job": "Screenplay", "department": "Writing"},
					{"name": "Joel Silver", "job": "Producer", "department": "Production"},
					{"name": "Bill Pope", "job": "Director of Photography", "department": "Camera"}
				]},
				"recommendations": {"results": [{"id": 604, "title": "The Matrix Reloaded"}]},
				"similar": {"results": [{"id": 1858, "title": "Transformers"}, {"id": 680, "title": "Pulp Fiction"}]},
				"release_dates": {"results": [{"iso_3166_1": "US", "release_dates": [
					{"certification": "NR", "type": 1}, {"certification": "R", "type": 3}, {"certification": "NC-17", "type": 4}
				]}]}}`))
		case "/tv/1396":
			_, _ = w.Write([]byte(`{"id": 1396, "name": "Breaking Bad", "original_language": "en", "vote_count": 15000, "popularity": 412.25,
				"production_companies": [{"id": 11073, "name": "Sony Pictures Television Studios"}, {"id": 33742, "name": " High Bridge Productions "}],
				"keywords": {"results": [{"id": 3, "name": "drug dealer"}]},
				"credits": {"crew": [
					{"name": "Vince Gilligan", "job": "Writer", "department": "Writing"},
					{"name": "Michelle MacLaren", "job": "Director", "department": "Directing"}
				]},
				"similar": {"results": [{"id": 60059, "name": "Better Call Saul"}]}}`))
		default:
			http.NotFound(w, r)
		}
	})

	movie, err := client.GetMediaDetail(context.Background(), "movie", 603)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(movie.Keywords, []string{"hacker", "simulated reality"}) {
		t.Errorf("movie keywords = %v", movie.Keywords)
	}
	if !slices.Equal(movie.Directors, []string{"Lana Wachowski", "Lilly Wachowski"}) || movie.Director != "Lana Wachowski" {
		t.Errorf("movie directors = %v, director = %q", movie.Directors, movie.Director)
	}
	if !slices.Equal(movie.Writers, []string{"Lana Wachowski", "Lilly Wachowski"}) {
		t.Errorf("movie writers = %v", movie.Writers)
	}
	if len(movie.Recommendations) != 1 || len(movie.Similar) != 2 || movie.Similar[0].ID != 1858 || movie.Similar[0].MediaType != "movie" {
		t.Errorf("movie recommendations = %+v, similar = %+v", movie.Recommendations, movie.Similar)
	}
	// Two or more US certifications: the plugin's rating is the theatrical
	// one; the enforcement rating stays the strictest.
	if movie.USContentRating != "R" || movie.USCertification != "NC-17" {
		t.Errorf("movie US content rating = %q, US certification = %q; want R and NC-17", movie.USContentRating, movie.USCertification)
	}
	if movie.VoteCount != 27000 || movie.OriginalLanguage != "en" || movie.Popularity != 81.5 {
		t.Errorf("movie vote count = %d, original language = %q, popularity = %v", movie.VoteCount, movie.OriginalLanguage, movie.Popularity)
	}

	series, err := client.GetMediaDetail(context.Background(), "series", 1396)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(series.Keywords, []string{"drug dealer"}) {
		t.Errorf("series keywords = %v", series.Keywords)
	}
	// Series studios are the production companies, as the metadata plugin
	// stores them; company IDs stay movie-only, the ones request routing reads.
	if !slices.Equal(series.ProductionCompanies, []string{"Sony Pictures Television Studios", "High Bridge Productions"}) || len(series.CompanyIDs) != 0 {
		t.Errorf("series production companies = %v, company IDs = %v", series.ProductionCompanies, series.CompanyIDs)
	}
	if series.Popularity != 412.25 {
		t.Errorf("series popularity = %v", series.Popularity)
	}
	if !slices.Equal(series.Writers, []string{"Vince Gilligan"}) || !slices.Equal(series.Directors, []string{"Michelle MacLaren"}) {
		t.Errorf("series writers = %v, directors = %v", series.Writers, series.Directors)
	}
	if len(series.Similar) != 1 || series.Similar[0].MediaType != "series" || series.Similar[0].Title != "Better Call Saul" {
		t.Errorf("series similar = %+v", series.Similar)
	}
	if series.VoteCount != 15000 || series.OriginalLanguage != "en" {
		t.Errorf("series vote count = %d, original language = %q", series.VoteCount, series.OriginalLanguage)
	}
}

// A detail is fetched and cached per language, and the callers that want
// TMDB's default language share the request GetMediaDetail always made.
func TestGetMediaDetailInLanguageKeysTheCacheByLanguage(t *testing.T) {
	var mu sync.Mutex
	var languages []string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		language, sent := r.URL.Query()["language"]
		mu.Lock()
		languages = append(languages, strings.Join(language, ","))
		mu.Unlock()
		title := "The Matrix"
		if sent {
			title = "Matrix (" + language[0] + ")"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": 603, "title": "` + title + `"}`))
	})
	ctx := context.Background()

	for _, tc := range []struct{ language, title string }{
		{"", "The Matrix"},
		{"fr", "Matrix (fr)"},
		{"pt-BR", "Matrix (pt-BR)"},
		{"FR", "Matrix (fr)"},             // same entry as "fr"
		{"en", "The Matrix"},              // English is TMDB's default
		{"x-silo-original", "The Matrix"}, // no single language to ask for
	} {
		detail, err := client.GetMediaDetailInLanguage(ctx, "movie", 603, tc.language)
		if err != nil {
			t.Fatal(err)
		}
		if detail.Title != tc.title {
			t.Errorf("language %q title = %q, want %q", tc.language, detail.Title, tc.title)
		}
	}
	if detail, err := client.GetMediaDetail(ctx, "movie", 603); err != nil || detail.Title != "The Matrix" {
		t.Fatalf("GetMediaDetail = %+v, %v; want the default-language detail", detail, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(languages, []string{"", "fr", "pt-BR"}) {
		t.Fatalf("language parameters sent = %q, want one request each for default, fr and pt-BR", languages)
	}
}
