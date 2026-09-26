package catalog

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMeilisearchCursorInitialTimeoutAndCancellation(t *testing.T) {
	for _, callerCancels := range []bool{false, true} {
		name := "provider timeout falls back"
		if callerCancels {
			name = "caller cancellation does not fall back"
		}
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			timeout := 50 * time.Millisecond
			if callerCancels {
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
				t.Fatal("settings request never started")
			}
			if callerCancels {
				cancel()
			}
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("search did not finish after timeout or cancellation")
			}
			if callerCancels {
				if !errors.Is(err, context.Canceled) || provider.lastFallback != "" {
					t.Fatalf("cancellation = %v, fallback = %q", err, provider.lastFallback)
				}
				return
			}
			// The deliberately absent PostgreSQL repository identifies execution of
			// the fallback without requiring a database for this HTTP regression.
			if ctx.Err() != nil || err == nil || !strings.Contains(err.Error(), "postgres search provider requires item repository") || provider.lastFallback == "" {
				t.Fatalf("provider timeout: context = %v, result = %v, fallback = %q", ctx.Err(), err, provider.lastFallback)
			}
		})
	}
}
