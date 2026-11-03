package catalog

import (
	"context"
	"strings"
	"testing"

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
