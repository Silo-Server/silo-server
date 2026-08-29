package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

func decodeParity(t *testing.T, recorder *httptest.ResponseRecorder) parityResponse {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response parityResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v; body = %s", err, recorder.Body.String())
	}
	return response
}

func serveParity(t *testing.T, handler *StreamTelemetryParityHandler) parityResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.HandleGetStreamTelemetryParity(recorder, httptest.NewRequest(http.MethodGet, "/admin/stream-telemetry/parity", nil))
	return decodeParity(t, recorder)
}

// With telemetry off the endpoint must say so rather than return an empty
// report, which a reader would take as "the two projections agree".
func TestStreamTelemetryParityReportsDisabled(t *testing.T) {
	t.Run("nil handler fields", func(t *testing.T) {
		response := serveParity(t, &StreamTelemetryParityHandler{})
		if response.Enabled || response.Reason == "" {
			t.Fatalf("response = %+v", response)
		}
		if len(response.Sources) != 0 {
			t.Fatalf("disabled telemetry still produced comparisons: %+v", response.Sources)
		}
	})
	t.Run("disabled registry", func(t *testing.T) {
		cfg := streamtelemetry.DefaultConfig("parity-test")
		registry := streamtelemetry.NewRegistry(cfg, streamtelemetry.NewLocalStore(), nil)
		t.Cleanup(func() { _ = registry.Stop(context.Background()) })
		response := serveParity(t, &StreamTelemetryParityHandler{Registry: registry})
		if response.Enabled {
			t.Fatalf("disabled registry reported enabled: %+v", response)
		}
	})
}

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

// A source that cannot be read must report itself unavailable with a reason.
// Omitting it would read as "there was nothing to compare against".
func TestStreamTelemetryParityReportsUnreadableSources(t *testing.T) {
	response := serveParity(t, enabledParityHandler(t))
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
}

// The completeness flag has to travel with the diff: a degraded view is missing
// sessions by construction, so a parity report built on one is evidence of
// blindness rather than disagreement.
func TestStreamTelemetryParitySurfacesViewCompleteness(t *testing.T) {
	response := serveParity(t, enabledParityHandler(t))
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
		if response := serveParity(t, handler); response.View.Refreshes != 1 {
			t.Fatalf("request %d rebuilt the view: refreshes = %d", i, response.View.Refreshes)
		}
	}
}
