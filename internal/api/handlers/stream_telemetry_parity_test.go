package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

func enabledParityHandler(t *testing.T) *StreamTelemetryParityHandler {
	t.Helper()
	cfg := streamtelemetry.DefaultConfig("parity-test")
	cfg.Enabled = true
	cfg.Retention = time.Minute
	store := streamtelemetry.NewLocalStore()
	registry := streamtelemetry.NewRegistry(cfg, store, nil)
	t.Cleanup(func() { _ = registry.Stop(context.Background()) })
	// Publish one snapshot so the merged view has a publisher and is buildable.
	if err := store.Publish(context.Background(), registry.Snapshot()); err != nil {
		t.Fatal(err)
	}
	return &StreamTelemetryParityHandler{
		Registry:  registry,
		ViewCache: streamtelemetry.NewViewCache(registry, time.Minute, nil),
	}
}

// The completeness flag has to travel with the diff: a degraded view is missing
// sessions by construction, so a parity report built on one is evidence of
// blindness rather than disagreement.
func TestStreamTelemetryParitySurfacesViewCompleteness(t *testing.T) {
	response := enabledParityHandler(t).ReadStreamTelemetryParity(t.Context())
	if !response.Enabled {
		t.Fatalf("response = %+v", response)
	}
	if len(response.Sources) != 2 {
		t.Fatalf("sources = %+v", response.Sources)
	}
	wantSources := map[string]bool{"playback_sessions_sync": false, "node_sessions_redis": false}
	for _, source := range response.Sources {
		if _, known := wantSources[source.Source]; !known {
			t.Fatalf("unexpected source %q", source.Source)
		}
		wantSources[source.Source] = true
		if source.Available {
			t.Fatalf("%s reported available with no backing store", source.Source)
		}
		if source.Error == "" {
			t.Fatalf("%s reported unavailable with no reason", source.Source)
		}
		if source.Report != nil {
			t.Fatalf("%s produced a report it could not have computed", source.Source)
		}
	}
	for name, seen := range wantSources {
		if !seen {
			t.Fatalf("source %q was omitted from the response entirely", name)
		}
	}
	if !response.View.Available {
		t.Fatalf("view = %+v", response.View)
	}
	if response.View.IncompleteReasons == nil {
		t.Fatal("incomplete_reasons must be present, not null, so a client can read it unconditionally")
	}
	if response.View.MissingPublishers == nil || response.View.Publishers == nil {
		t.Fatalf("publisher lists must be present: %+v", response.View)
	}
	if response.View.Refreshes != 1 {
		t.Fatalf("refreshes = %d, want exactly one build for one request", response.View.Refreshes)
	}
}

// The endpoint must not rebuild the merged view per request: it measured ~347 ms
// at the 50 000-session cap.
func TestStreamTelemetryParityReusesTheCachedView(t *testing.T) {
	handler := enabledParityHandler(t)
	for i := 0; i < 4; i++ {
		if response := handler.ReadStreamTelemetryParity(t.Context()); response.View.Refreshes != 1 {
			t.Fatalf("request %d rebuilt the view: refreshes = %d", i, response.View.Refreshes)
		}
	}
}
