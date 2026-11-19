package tmdb

import (
	"context"

	"errors"

	"net/http"
	"net/http/httptest"
	"net/url"

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
