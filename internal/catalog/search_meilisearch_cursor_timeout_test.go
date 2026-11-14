package catalog

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMeilisearchCursorInitialTimeoutAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name string
		// searchFails answers the settings request and fails the search with a
		// 503; otherwise every request hangs until the client gives up.
		searchFails   bool
		callerCancels bool
		wantRequests  int32
	}{
		{name: "settings timeout falls back and opens the circuit", wantRequests: 1},
		{name: "search failure falls back and opens the circuit", searchFails: true, wantRequests: 2},
		{name: "caller cancellation does not fall back", callerCancels: true, wantRequests: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			started := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if tc.searchFails {
					if strings.HasSuffix(r.URL.Path, "/settings") {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"pagination":{"maxTotalHits":1000}}`))
						return
					}
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				select {
				case started <- struct{}{}:
				default:
				}
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
			req := CatalogSearchRequest{CursorPaging: true, Query: "movie", ItemTypes: []string{"movie"}, Limit: 60}
			done := make(chan error, 1)
			go func() {
				_, searchErr := provider.Search(ctx, req)
				done <- searchErr
			}()
			if tc.callerCancels {
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("settings request never started")
				}
				cancel()
			}
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("search did not finish after timeout or cancellation")
			}
			_, blocked := provider.circuitBlocked(time.Now())
			if tc.callerCancels {
				if !errors.Is(err, context.Canceled) || provider.lastFallback != "" || blocked {
					t.Fatalf("cancellation = %v, fallback = %q, circuit open = %v", err, provider.lastFallback, blocked)
				}
				return
			}
			// The deliberately absent PostgreSQL repository identifies execution of
			// the fallback without requiring a database for this HTTP regression.
			if ctx.Err() != nil || err == nil || !strings.Contains(err.Error(), "postgres search provider requires item repository") || provider.lastFallback == "" {
				t.Fatalf("provider failure: context = %v, result = %v, fallback = %q", ctx.Err(), err, provider.lastFallback)
			}
			if !blocked {
				t.Fatal("provider failure did not open the circuit")
			}
			// With the circuit open, the next search falls back without asking
			// Meilisearch again.
			if _, err := provider.Search(ctx, req); err == nil || !strings.Contains(err.Error(), "postgres search provider requires item repository") {
				t.Fatalf("search with open circuit = %v, want PostgreSQL fallback", err)
			}
			if got := requests.Load(); got != tc.wantRequests {
				t.Fatalf("Meilisearch requests = %d, want %d (open circuit skips Meilisearch)", got, tc.wantRequests)
			}
		})
	}
}
