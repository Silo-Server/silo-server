package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A capability lookup overtaken by an invalidation was handing the caller the
// pre-invalidation report, and that caller goes on to pick transformations and
// a tone-map executor from it — possibly hardware the newer report says is
// gone. It re-probes instead.
func TestLookupRemoteCapabilitiesRefetchesWhenOvertakenMidFlight(t *testing.T) {
	handler := NewPlaybackHandler(nil)
	var fetches atomic.Int32

	// Captured after the server exists; the cache is keyed by the URL the lookup
	// was given, which is not the Host header the node sees.
	var nodeURL string
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempt := fetches.Add(1)
		if attempt == 1 {
			// The health sweep notices the node changed while this first read is
			// still in flight. The counter is bumped directly rather than through
			// RefreshNodeCapabilitiesV3, which would also start a background
			// re-probe and make the fetch count say nothing about this lookup.
			handler.v3NodeCapabilitiesMu.Lock()
			if handler.v3NodeCapabilityInvalidations == nil {
				handler.v3NodeCapabilityInvalidations = make(map[string]uint64)
			}
			handler.v3NodeCapabilityInvalidations[nodeURL]++
			handler.v3NodeCapabilitiesMu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resolved":       "qsv",
			"render_devices": []string{"/dev/dri/renderD128"},
			"transformations": []map[string]any{
				{"name": "hdr_to_sdr_tone_map", "executor": "qsv", "recipe_version": "v3"},
			},
		})
	}))
	t.Cleanup(node.Close)
	nodeURL = node.URL

	if _, err := handler.lookupRemoteCapabilitiesV3(context.Background(), nodeURL, false); err != nil {
		t.Fatalf("lookupRemoteCapabilitiesV3: %v", err)
	}
	if got := fetches.Load(); got != 2 {
		t.Fatalf("fetches = %d, want the overtaken read repeated exactly once", got)
	}
}

// The ordinary path must not pay for that: a lookup nothing invalidates reads
// the node exactly once.
func TestLookupRemoteCapabilitiesFetchesOnceWhenNothingInvalidates(t *testing.T) {
	handler := NewPlaybackHandler(nil)
	var fetches atomic.Int32

	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"resolved": "qsv"})
	}))
	t.Cleanup(node.Close)

	if _, err := handler.lookupRemoteCapabilitiesV3(context.Background(), node.URL, false); err != nil {
		t.Fatalf("lookupRemoteCapabilitiesV3: %v", err)
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("fetches = %d, want a single read on the uncontended path", got)
	}
}

// An acceleration change makes a node's inventory wrong and its next capability
// matrix cold — which is exactly when the read is slowest. Dropping the learned
// budget along with the inventory sent the refresh that invalidation triggers
// back to the 120s fallback, short of what a two-device node legitimately asks
// for, so planning lost its inventory precisely after the invalidation.
func TestRefreshNodeCapabilitiesKeepsTheLearnedProbeBudget(t *testing.T) {
	handler := NewPlaybackHandler(nil)

	advertised := 136_000
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resolved":                 "qsv",
			"probe_request_timeout_ms": advertised,
		})
	}))
	t.Cleanup(node.Close)

	if _, err := handler.lookupRemoteCapabilitiesV3(context.Background(), node.URL, false); err != nil {
		t.Fatalf("lookupRemoteCapabilitiesV3: %v", err)
	}
	learned := handler.remoteToneMapProbeTimeoutV3(node.URL)
	if want := 136 * time.Second; learned != want {
		t.Fatalf("learned budget = %v, want the advertised %v", learned, want)
	}

	handler.RefreshNodeCapabilitiesV3(node.URL)

	if got := handler.remoteToneMapProbeTimeoutV3(node.URL); got != learned {
		t.Fatalf("budget after invalidation = %v, want the learned %v — the refresh it triggers is sized from this",
			got, learned)
	}
	// The inventory itself is still discarded; only the budget survives.
	handler.v3NodeCapabilitiesMu.Lock()
	_, cached := handler.v3NodeCapabilities[node.URL]
	handler.v3NodeCapabilitiesMu.Unlock()
	if cached {
		t.Fatal("invalidation left the inventory cached")
	}
}
