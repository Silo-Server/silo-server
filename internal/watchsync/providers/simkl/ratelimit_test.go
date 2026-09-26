package simkl

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

var rateLimitTestConfig = watchsync.ServerConfig{ClientID: "client-id", ClientSecret: "client-secret"}

var rateLimitTestEvent = watchsync.ScrobbleEvent{
	Kind:            historyimport.KindMovie,
	IMDbID:          "tt1375666",
	PositionSeconds: 60,
	DurationSeconds: 6000,
}

// recordSleeps replaces the provider's in-place retry wait so tests observe
// the requested durations without sleeping.
func recordSleeps(p *Provider) *[]time.Duration {
	var waits []time.Duration
	p.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	return &waits
}

// respondingServer answers every request with status, Retry-After (when
// set), and body, counting attempts.
func respondingServer(t *testing.T, status int, retryAfter, body string, attempts *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func requireRateLimited(t *testing.T, err error) watchsync.RateLimitedError {
	t.Helper()
	var limited watchsync.RateLimitedError
	if !errors.As(err, &limited) {
		t.Fatalf("expected RateLimitedError, got %v", err)
	}
	if limited.Provider != "simkl" {
		t.Fatalf("got provider %q, want simkl", limited.Provider)
	}
	return limited
}

func TestDailyQuotaRateLimitUsesRetryAfterSeconds(t *testing.T) {
	var attempts atomic.Int32
	server := respondingServer(t, http.StatusTooManyRequests, "7200",
		`{"error":"user_limit_exceeded","code":429,"message":"This user has reached their daily API request limit"}`, &attempts)
	provider := NewProvider(server.Client(), server.URL)
	waits := recordSleeps(provider)

	_, err := provider.FetchProgress(context.Background(), rateLimitTestConfig, watchsync.Connection{AccessToken: "token"})
	limited := requireRateLimited(t, err)
	if limited.RetryAfter != 2*time.Hour {
		t.Fatalf("got retry-after %s, want 2h", limited.RetryAfter)
	}
	if attempts.Load() != 1 || len(*waits) != 0 {
		t.Fatalf("got %d attempts and waits %v; a daily quota must defer, not retry in place", attempts.Load(), *waits)
	}
}

func TestRateLimitUsesRetryAfterHTTPDate(t *testing.T) {
	var attempts atomic.Int32
	server := respondingServer(t, http.StatusTooManyRequests,
		time.Now().Add(10*time.Minute).UTC().Format(http.TimeFormat), "", &attempts)
	provider := NewProvider(server.Client(), server.URL)

	_, err := provider.FetchProgress(context.Background(), rateLimitTestConfig, watchsync.Connection{AccessToken: "token"})
	limited := requireRateLimited(t, err)
	// HTTP-dates have one-second resolution, so allow for truncation.
	if limited.RetryAfter <= 10*time.Minute-5*time.Second || limited.RetryAfter > 10*time.Minute {
		t.Fatalf("got retry-after %s, want about 10m", limited.RetryAfter)
	}
}

func TestRateLimitWithoutRetryAfterUsesFallback(t *testing.T) {
	var attempts atomic.Int32
	server := respondingServer(t, http.StatusTooManyRequests, "", "", &attempts)
	provider := NewProvider(server.Client(), server.URL)
	waits := recordSleeps(provider)

	_, err := provider.FetchProgress(context.Background(), rateLimitTestConfig, watchsync.Connection{AccessToken: "token"})
	limited := requireRateLimited(t, err)
	if limited.RetryAfter != defaultRetryAfter {
		t.Fatalf("got retry-after %s, want fallback %s", limited.RetryAfter, defaultRetryAfter)
	}
	if attempts.Load() != 1 || len(*waits) != 0 {
		t.Fatalf("got %d attempts and waits %v, want one attempt", attempts.Load(), *waits)
	}
}

func TestPerSecondRateLimitRetriesInPlaceIgnoringDailyRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			// Simkl documents that this Retry-After carries the daily reset.
			w.Header().Set("Retry-After", "50000")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate_limit","code":429}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	provider := NewProvider(server.Client(), server.URL)
	waits := recordSleeps(provider)

	if _, err := provider.fetchPlayback(context.Background(), rateLimitTestConfig, watchsync.Connection{AccessToken: "token"}, "/sync/playback/movies", ""); err != nil {
		t.Fatalf("fetch playback after in-place retry: %v", err)
	}
	if attempts.Load() != 2 || len(*waits) != 1 || (*waits)[0] != perSecondRetryWait {
		t.Fatalf("got %d attempts and waits %v, want 2 attempts after one %s wait", attempts.Load(), *waits, perSecondRetryWait)
	}
}

func TestPerSecondRateLimitExhaustedDefersForFallback(t *testing.T) {
	var attempts atomic.Int32
	server := respondingServer(t, http.StatusTooManyRequests, "50000", `{"error":"rate_limit","code":429}`, &attempts)
	provider := NewProvider(server.Client(), server.URL)
	waits := recordSleeps(provider)

	_, err := provider.FetchProgress(context.Background(), rateLimitTestConfig, watchsync.Connection{AccessToken: "token"})
	limited := requireRateLimited(t, err)
	if attempts.Load() != maxRetryAttempts+1 || len(*waits) != maxRetryAttempts {
		t.Fatalf("got %d attempts and waits %v, want %d attempts", attempts.Load(), *waits, maxRetryAttempts+1)
	}
	if limited.RetryAfter != defaultRetryAfter {
		t.Fatalf("got retry-after %s, want floored %s", limited.RetryAfter, defaultRetryAfter)
	}
}

func TestWriteLockRetriesInPlaceWithSameBody(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"RATE_LIMIT","code":400}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	provider := NewProvider(server.Client(), server.URL)
	// Keep the write limiter out of the way so only the lock wait runs.
	provider.writes = watchsync.NewCredentialLimiter(time.Nanosecond, 10)
	waits := recordSleeps(provider)

	if err := provider.Start(context.Background(), rateLimitTestConfig, watchsync.Connection{AccessToken: "token"}, rateLimitTestEvent); err != nil {
		t.Fatalf("Start after write lock cleared: %v", err)
	}
	if len(bodies) != 2 || bodies[0] == "" || bodies[0] != bodies[1] {
		t.Fatalf("body not replayed identically: %#v", bodies)
	}
	if len(*waits) != 1 || (*waits)[0] != writeLockRetryWait {
		t.Fatalf("got in-place waits %v, want [%s]", *waits, writeLockRetryWait)
	}
}

func TestPersistentWriteLockReturnsRateLimitedError(t *testing.T) {
	var attempts atomic.Int32
	server := respondingServer(t, http.StatusBadRequest, "", `{"error":"RATE_LIMIT","code":400}`, &attempts)
	provider := NewProvider(server.Client(), server.URL)
	provider.writes = watchsync.NewCredentialLimiter(time.Nanosecond, 10)
	waits := recordSleeps(provider)

	err := provider.Start(context.Background(), rateLimitTestConfig, watchsync.Connection{AccessToken: "token"}, rateLimitTestEvent)
	limited := requireRateLimited(t, err)
	if attempts.Load() != maxRetryAttempts+1 || len(*waits) != maxRetryAttempts {
		t.Fatalf("got %d attempts and waits %v, want %d attempts", attempts.Load(), *waits, maxRetryAttempts+1)
	}
	if limited.RetryAfter != defaultRetryAfter {
		t.Fatalf("got retry-after %s, want %s", limited.RetryAfter, defaultRetryAfter)
	}
}

func TestOtherBadRequestIsNotRateLimited(t *testing.T) {
	var attempts atomic.Int32
	server := respondingServer(t, http.StatusBadRequest, "", `{"error":"wrong_parameter","code":400}`, &attempts)
	provider := NewProvider(server.Client(), server.URL)
	waits := recordSleeps(provider)

	err := provider.Start(context.Background(), rateLimitTestConfig, watchsync.Connection{AccessToken: "token"}, rateLimitTestEvent)
	if err == nil {
		t.Fatal("expected an error for a malformed request")
	}
	if _, ok := watchsync.AsRateLimited(err); ok {
		t.Fatalf("400 wrong_parameter classified as rate limited: %v", err)
	}
	if attempts.Load() != 1 || len(*waits) != 0 {
		t.Fatalf("got %d attempts and waits %v, want no retry", attempts.Load(), *waits)
	}
}

func TestConflictIsNotRateLimited(t *testing.T) {
	var attempts atomic.Int32
	server := respondingServer(t, http.StatusConflict, "", `{"error":"already_watched"}`, &attempts)
	provider := NewProvider(server.Client(), server.URL)

	err := provider.Pause(context.Background(), rateLimitTestConfig, watchsync.Connection{AccessToken: "token"}, rateLimitTestEvent)
	var conflict simklConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected simklConflictError, got %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("got %d attempts, want 1", attempts.Load())
	}
}

func TestWriteLimiterPacesPerTokenAndLeavesReadsAlone(t *testing.T) {
	var writes, reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			reads.Add(1)
			_, _ = w.Write([]byte(`[]`))
			return
		}
		writes.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	provider := NewProvider(server.Client(), server.URL)
	// One write per hour: a second write for the same token can only
	// proceed by waiting, which the one-minute deadline below refuses.
	provider.writes = watchsync.NewCredentialLimiter(time.Hour, 1)
	tokenA := watchsync.Connection{AccessToken: "token-a"}

	if err := provider.Start(context.Background(), rateLimitTestConfig, tokenA, rateLimitTestEvent); err != nil {
		t.Fatalf("first write for token-a: %v", err)
	}
	// The limiter refuses at once when the next slot is past the deadline,
	// so the request never reaches the server. Without pacing it would.
	deadline, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, limited := watchsync.AsRateLimited(provider.Start(deadline, rateLimitTestConfig, tokenA, rateLimitTestEvent)); !limited {
		t.Fatal("second write for token-a must be deferred as rate limited, unsent")
	}
	if err := provider.Start(context.Background(), rateLimitTestConfig, watchsync.Connection{AccessToken: "token-b"}, rateLimitTestEvent); err != nil {
		t.Fatalf("token-b must not wait behind token-a: %v", err)
	}
	if writes.Load() != 2 {
		t.Fatalf("server saw %d writes, want 2", writes.Load())
	}

	for range 3 {
		if _, err := provider.fetchPlayback(context.Background(), rateLimitTestConfig, tokenA, "/sync/playback/movies", ""); err != nil {
			t.Fatalf("reads must not be paced: %v", err)
		}
	}
	if reads.Load() != 3 {
		t.Fatalf("server saw %d reads, want 3", reads.Load())
	}
}
