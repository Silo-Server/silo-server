package catalog

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMeilisearchCursorInitialTimeoutAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name string
		// hangOn is the Meilisearch request that never answers.
		hangOn        string
		callerCancels bool
	}{
		{name: "settings timeout falls back and opens the breaker", hangOn: "/settings"},
		{name: "ranking search timeout falls back and opens the breaker", hangOn: "/search"},
		{name: "caller cancellation does not fall back", hangOn: "/settings", callerCancels: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, tc.hangOn) {
					_, _ = w.Write([]byte(`{"pagination":{"maxTotalHits":1000}}`))
					return
				}
				// The server notices the client giving up only once the
				// request body has been read.
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			timeout := 50 * time.Millisecond
			if tc.callerCancels {
				timeout = 5 * time.Second
			}
			provider, err := NewMeilisearchSearchProvider(nil, nil, nil, MeilisearchProviderConfig{
				URL: server.URL, Timeout: timeout, IndexTypes: []string{"movie"},
			})
			if err != nil {
				t.Fatal(err)
			}
			provider.stateRepo = fakeMeilisearchIndexStateStore{state: SearchIndexState{
				ActiveIndexUID: "timeout_test",
				SchemaVersion:  catalogSearchMeilisearchSchemaVersion(provider.config.Embedder, provider.config.IndexTypes, false, false),
			}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, searchErr := provider.Search(ctx, CatalogSearchRequest{CursorPaging: true, Query: "movie", ItemTypes: []string{"movie"}, Limit: 60})
				done <- searchErr
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatalf("%s request never started", tc.hangOn)
			}
			if tc.callerCancels {
				cancel()
			}
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("search did not finish after timeout or cancellation")
			}
			_, circuitOpen := provider.circuitBlocked(time.Now())
			if tc.callerCancels {
				if !errors.Is(err, context.Canceled) || provider.lastFallback != "" || circuitOpen {
					t.Fatalf("cancellation = %v, fallback = %q, circuit open = %v", err, provider.lastFallback, circuitOpen)
				}
				return
			}
			// The deliberately absent PostgreSQL repository identifies execution of
			// the fallback without requiring a database for this HTTP regression.
			if ctx.Err() != nil || err == nil || !strings.Contains(err.Error(), "postgres search provider requires item repository") || provider.lastFallback == "" {
				t.Fatalf("provider timeout: context = %v, result = %v, fallback = %q", ctx.Err(), err, provider.lastFallback)
			}
			// The open breaker sends later cursor searches straight to PostgreSQL
			// instead of each waiting out the timeout (issue #3369).
			if !circuitOpen {
				t.Fatal("provider timeout left the circuit breaker closed")
			}
		})
	}
}
