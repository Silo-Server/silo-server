package catalog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/embeddingvectors"
)

// TestEvaluateEmbedderSettingsCapability exercises the pure capability
// evaluation across its four outcomes, asserting a distinct reason (or OK) for
// each. This is the seam that lets capability validation be tested without
// faking the concrete *meilisearchClient.
func TestEvaluateEmbedderSettingsCapability(t *testing.T) {
	const embedder = "silo_recommendations"
	wantDims := embeddingvectors.CanonicalDimensions

	t.Run("missing embedder", func(t *testing.T) {
		settings := meilisearchIndexSettings{Embedders: map[string]meilisearchEmbedderSettings{}}
		got := evaluateEmbedderSettings(settings, embedder, wantDims)
		if got.OK {
			t.Fatalf("expected not OK, got %+v", got)
		}
		if !strings.Contains(got.Reason, "not configured") {
			t.Fatalf("expected 'not configured' reason, got %q", got.Reason)
		}
		if got.Embedder != embedder {
			t.Fatalf("expected embedder %q, got %q", embedder, got.Embedder)
		}
	})

	t.Run("wrong source", func(t *testing.T) {
		settings := meilisearchIndexSettings{Embedders: map[string]meilisearchEmbedderSettings{
			embedder: {Source: "openAi", Dimensions: wantDims},
		}}
		got := evaluateEmbedderSettings(settings, embedder, wantDims)
		if got.OK {
			t.Fatalf("expected not OK, got %+v", got)
		}
		if !strings.Contains(got.Reason, "source") || !strings.Contains(got.Reason, "userProvided") {
			t.Fatalf("expected source/userProvided reason, got %q", got.Reason)
		}
	})

	t.Run("wrong dimensions", func(t *testing.T) {
		settings := meilisearchIndexSettings{Embedders: map[string]meilisearchEmbedderSettings{
			embedder: {Source: "userProvided", Dimensions: wantDims - 1},
		}}
		got := evaluateEmbedderSettings(settings, embedder, wantDims)
		if got.OK {
			t.Fatalf("expected not OK, got %+v", got)
		}
		if !strings.Contains(got.Reason, "dimensions") {
			t.Fatalf("expected dimensions reason, got %q", got.Reason)
		}
		if got.Dimensions != wantDims-1 {
			t.Fatalf("expected reported dimensions %d, got %d", wantDims-1, got.Dimensions)
		}
	})

	t.Run("ok", func(t *testing.T) {
		settings := meilisearchIndexSettings{Embedders: map[string]meilisearchEmbedderSettings{
			embedder: {Source: "userProvided", Dimensions: wantDims},
		}}
		got := evaluateEmbedderSettings(settings, embedder, wantDims)
		if !got.OK {
			t.Fatalf("expected OK, got %+v", got)
		}
		if got.Reason != "" {
			t.Fatalf("expected empty reason, got %q", got.Reason)
		}
		if got.Embedder != embedder || got.Dimensions != wantDims {
			t.Fatalf("expected embedder %q dims %d, got %+v", embedder, wantDims, got)
		}
	})
}

// TestSemanticCapabilityNeverTouchesCircuit verifies that requesting the
// capability via a nil-client provider returns a deterministic unavailable
// result and leaves the circuit-breaker fields untouched. Capability failures
// must never down keyword search.
func TestSemanticCapabilityNeverTouchesCircuit(t *testing.T) {
	p := &MeilisearchSearchProvider{}

	if !p.unhealthyUntil.IsZero() || p.unhealthyReason != "" {
		t.Fatalf("precondition: circuit fields not zero: until=%v reason=%q", p.unhealthyUntil, p.unhealthyReason)
	}

	cap := p.SemanticCapability(context.Background())
	if cap.OK {
		t.Fatalf("expected not OK for nil client, got %+v", cap)
	}
	if !strings.Contains(cap.Reason, "client unavailable") {
		t.Fatalf("expected client-unavailable reason, got %q", cap.Reason)
	}

	if !p.unhealthyUntil.IsZero() {
		t.Fatalf("SemanticCapability tripped the circuit: unhealthyUntil=%v", p.unhealthyUntil)
	}
	if p.unhealthyReason != "" {
		t.Fatalf("SemanticCapability set unhealthyReason=%q", p.unhealthyReason)
	}
	if p.lastFallback != "" {
		t.Fatalf("SemanticCapability set lastFallback=%q", p.lastFallback)
	}
}

// TestBuildSemanticStatusNilSnapshot covers the disabled/no-coverage path: a nil
// snapshot yields not-ready with the supplied reason and no per-type rows.
func TestBuildSemanticStatusNilSnapshot(t *testing.T) {
	st := buildSemanticStatus(nil, false, "coverage not yet computed")
	if st.Ready {
		t.Fatalf("expected not ready, got %+v", st)
	}
	if st.DisabledReason != "coverage not yet computed" {
		t.Fatalf("expected reason carried, got %q", st.DisabledReason)
	}
	if len(st.PerType) != 0 {
		t.Fatalf("expected empty per-type, got %+v", st.PerType)
	}
	if st.CoverageUpdatedAt != nil {
		t.Fatalf("expected nil CoverageUpdatedAt for nil snapshot, got %v", st.CoverageUpdatedAt)
	}
}

// TestBuildSemanticStatusFromSnapshot covers the populated path: overall ratio,
// updated-at, and a deterministically sorted per-type list with ratio/ready
// mapped through. ready/reason are taken from the passed-in gate decision.
func TestBuildSemanticStatusFromSnapshot(t *testing.T) {
	now := time.Now().UTC()
	snap := &semanticCoverageSnapshot{
		Overall:   0.5,
		Model:     "model-x",
		UpdatedAt: now,
		PerType: map[string]catalogTypeCoverage{
			"series": {Type: "series", Eligible: 10, Vectorized: 5, Ratio: 0.5, Ready: false},
			"movie":  {Type: "movie", Eligible: 4, Vectorized: 4, Ratio: 1.0, Ready: true},
		},
	}

	st := buildSemanticStatus(snap, false, `type "series" coverage 50% below threshold`)
	if st.Ready {
		t.Fatalf("expected not ready (gate said so), got %+v", st)
	}
	if st.DisabledReason == "" {
		t.Fatalf("expected disabled reason carried when not ready")
	}
	if st.CoverageRatio != 0.5 {
		t.Fatalf("expected CoverageRatio == Overall 0.5, got %v", st.CoverageRatio)
	}
	if st.CoverageUpdatedAt == nil || !st.CoverageUpdatedAt.Equal(now) {
		t.Fatalf("expected CoverageUpdatedAt == snapshot UpdatedAt, got %v", st.CoverageUpdatedAt)
	}
	if len(st.PerType) != 2 {
		t.Fatalf("expected 2 per-type rows, got %d", len(st.PerType))
	}
	// Deterministic sort by Type: movie before series.
	if st.PerType[0].Type != "movie" || st.PerType[1].Type != "series" {
		t.Fatalf("expected per-type sorted by Type [movie, series], got %q,%q", st.PerType[0].Type, st.PerType[1].Type)
	}
	if !st.PerType[0].Ready || st.PerType[0].CoverageRatio != 1.0 {
		t.Fatalf("movie row mismatch: %+v", st.PerType[0])
	}
	if st.PerType[1].Ready || st.PerType[1].CoverageRatio != 0.5 {
		t.Fatalf("series row mismatch: %+v", st.PerType[1])
	}
	if st.PerType[1].Eligible != 10 || st.PerType[1].Vectorized != 5 {
		t.Fatalf("series counts mismatch: %+v", st.PerType[1])
	}

	// Ready path carries no disabled reason.
	ready := buildSemanticStatus(snap, true, "")
	if !ready.Ready || ready.DisabledReason != "" {
		t.Fatalf("expected ready with no disabled reason, got %+v", ready)
	}
}
