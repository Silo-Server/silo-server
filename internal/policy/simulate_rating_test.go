package policy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
)

// simulateDownload runs an action-domain simulation of a download that passes
// every gate except, possibly, the maturity ceiling, and returns the decision.
// It goes through Simulate with no store and no custom source, so it exercises
// the same path the admin policy simulator uses.
func simulateDownload(t *testing.T, rating, ceiling string, extra map[string]any) ActionDecision {
	t.Helper()
	input := map[string]any{
		"schema_version":      1,
		"action":              ActionDownload,
		"user_id":             1,
		"downloads_enabled":   true,
		"download_allowed":    true,
		"artifacts_available": true,
		"request_time":        "2026-01-01T00:00:00Z",
		"content_rating":      rating,
		"max_content_rating":  ceiling,
	}
	for key, value := range extra {
		input[key] = value
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Simulate(context.Background(), nil, SimulateRequest{Domain: DomainAction, Input: raw})
	if err != nil {
		t.Fatalf("Simulate(%q under %q): %v", rating, ceiling, err)
	}
	var decision ActionDecision
	if err := json.Unmarshal(result.Decision, &decision); err != nil {
		t.Fatalf("decoding decision %s: %v", result.Decision, err)
	}
	return decision
}

// TestSimulateActionDerivesTheRatingCeiling pins that an action simulation
// answers the way the live request does. action.rego reads a
// content_rating_within_ceiling flag only Go can compute, and treats an absent
// flag as "no ceiling asserted" — so until Simulate derived it, an
// administrator simulating an R download under a PG ceiling was told "allowed"
// while the real download was denied.
func TestSimulateActionDerivesTheRatingCeiling(t *testing.T) {
	if decision := simulateDownload(t, "R", "PG", nil); decision.Allowed {
		t.Errorf("simulating an R download under a PG ceiling allowed it: %+v", decision)
	}
	if decision := simulateDownload(t, "PG", "PG-13", nil); !decision.Allowed {
		t.Errorf("simulating a PG download under a PG-13 ceiling denied it: %+v", decision)
	}
	if decision := simulateDownload(t, "NC-17", "", nil); !decision.Allowed {
		t.Errorf("simulating a download with no ceiling denied it: %+v", decision)
	}

	// The live PDP overwrites the flag, so honoring one supplied by the caller
	// would let the simulator predict a request that cannot occur.
	supplied := map[string]any{"content_rating_within_ceiling": true}
	if decision := simulateDownload(t, "R", "PG", supplied); decision.Allowed {
		t.Errorf("a supplied content_rating_within_ceiling loosened the simulation: %+v", decision)
	}
}

// TestSimulateActionMatchesRatingAllowed is the parity the simulator exists
// for: with every other gate open, the simulated decision tracks
// access.RatingAllowed — the same function PDP.CheckAction calls — across
// systems, unrated ratings and unusable ceilings.
func TestSimulateActionMatchesRatingAllowed(t *testing.T) {
	pairs := []struct{ rating, ceiling string }{
		{"R", "PG"},
		{"PG", "PG-13"},
		{"TV-14", "PG-13"},
		{"FSK 16", "PG-13"},
		{"12A", "TV-14"},
		{"NC-17", ""},
		{"NR", "PG-13"},
		{"PG", "banana"},
		{"PG", " "},
	}
	for _, pair := range pairs {
		want := access.RatingAllowed(pair.rating, pair.ceiling)
		got := simulateDownload(t, pair.rating, pair.ceiling, nil)
		if got.Allowed != want {
			t.Errorf("rating %q under ceiling %q: simulated allowed=%v, access.RatingAllowed=%v (%s)",
				pair.rating, pair.ceiling, got.Allowed, want, got.ReasonCode)
		}
	}
}
