package trakt

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

func rateLimitedServer(t *testing.T, retryAfter string, attempts *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(http.StatusTooManyRequests)
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
	if limited.Provider != "trakt" {
		t.Fatalf("got provider %q, want trakt", limited.Provider)
	}
	return limited
}

func TestRateLimitUsesRetryAfterSeconds(t *testing.T) {
	var attempts atomic.Int32
	server := rateLimitedServer(t, "120", &attempts)
	provider := NewProvider(server.Client(), server.URL)
	waits := recordSleeps(provider)

	_, err := provider.FetchProgress(context.Background(), rateLimitTestConfig, watchsync.Connection{AccessToken: "token"})
	limited := requireRateLimited(t, err)
	if limited.RetryAfter != 2*time.Minute {
		t.Fatalf("got retry-after %s, want 2m", limited.RetryAfter)
	}
	if attempts.Load() != 1 || len(*waits) != 0 {
		t.Fatalf("got %d attempts and waits %v; a long Retry-After must defer, not retry in place", attempts.Load(), *waits)
	}
}

func TestRateLimitUsesRetryAfterHTTPDate(t *testing.T) {
	var attempts atomic.Int32
	server := rateLimitedServer(t, time.Now().Add(10*time.Minute).UTC().Format(http.TimeFormat), &attempts)
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
	server := rateLimitedServer(t, "", &attempts)
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

func TestRateLimitShortRetryAfterRetriesInPlaceWithSameBody(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		if len(bodies) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	provider := NewProvider(server.Client(), server.URL)
	// Keep the write limiter out of the way so only the Retry-After wait runs.
	provider.writes = watchsync.NewCredentialLimiter(time.Nanosecond, 10)
	waits := recordSleeps(provider)

	err := provider.Start(context.Background(), rateLimitTestConfig, watchsync.Connection{AccessToken: "token"}, watchsync.ScrobbleEvent{
		Kind:            historyimport.KindMovie,
		IMDbID:          "tt123",
		PositionSeconds: 60,
		DurationSeconds: 600,
	})
	if err != nil {
		t.Fatalf("Start after in-place retry: %v", err)
	}
	if len(bodies) != 2 || bodies[0] == "" || bodies[0] != bodies[1] {
		t.Fatalf("body not replayed identically: %#v", bodies)
	}
	if len(*waits) != 1 || (*waits)[0] != time.Second {
		t.Fatalf("got in-place waits %v, want [1s]", *waits)
	}
}

func TestRateLimitExhaustedInPlaceRetriesDeferForFallback(t *testing.T) {
	var attempts atomic.Int32
	server := rateLimitedServer(t, "1", &attempts)
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
	event := watchsync.ScrobbleEvent{Kind: historyimport.KindMovie, IMDbID: "tt123", DurationSeconds: 600}
	tokenA := watchsync.Connection{AccessToken: "token-a"}

	if err := provider.Start(context.Background(), rateLimitTestConfig, tokenA, event); err != nil {
		t.Fatalf("first write for token-a: %v", err)
	}
	// The limiter refuses at once when the next slot is past the deadline,
	// so the request never reaches the server. Without pacing it would.
	deadline, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, limited := watchsync.AsRateLimited(provider.Start(deadline, rateLimitTestConfig, tokenA, event)); !limited {
		t.Fatal("second write for token-a must be deferred as rate limited, unsent")
	}
	if err := provider.Start(context.Background(), rateLimitTestConfig, watchsync.Connection{AccessToken: "token-b"}, event); err != nil {
		t.Fatalf("token-b must not wait behind token-a: %v", err)
	}
	if writes.Load() != 2 {
		t.Fatalf("server saw %d writes, want 2", writes.Load())
	}

	for range 3 {
		if _, err := provider.FetchProgress(context.Background(), rateLimitTestConfig, tokenA); err != nil {
			t.Fatalf("reads must not be paced: %v", err)
		}
	}
	if reads.Load() != 3 {
		t.Fatalf("server saw %d reads, want 3", reads.Load())
	}
}

func TestStartDeviceAuthReportsRateLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	_, err := NewProvider(server.Client(), server.URL).StartDeviceAuth(context.Background(), rateLimitTestConfig)
	limited, ok := watchsync.AsRateLimited(err)
	if !ok || limited.RetryAfter != 30*time.Second {
		t.Fatalf("err = %v, want a 30s RateLimitedError", err)
	}
}
