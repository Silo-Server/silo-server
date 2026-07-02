package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// decodeCatalogResponse runs writeCatalogResponse against an httptest recorder
// and returns the decoded JSON body as a generic map so individual keys can be
// asserted for presence/absence (search_diagnostics is omitempty).
func decodeCatalogResponse(t *testing.T, result *catalog.CatalogResult, grouped bool) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	// writeCatalogResponse uses no handler state, so a zero-value handler is fine.
	(&CatalogHandler{}).writeCatalogResponse(rec, result, []itemListResponse{}, grouped)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding catalog response: %v\nbody: %s", err, rec.Body.String())
	}
	return body
}

func TestWriteCatalogResponse_DiagnosticsKeywordFallback(t *testing.T) {
	body := decodeCatalogResponse(t, &catalog.CatalogResult{
		Total:          3,
		TotalExact:     true,
		HasMore:        false,
		Provider:       catalog.SearchProviderMeilisearch,
		Mode:           "keyword",
		SemanticUsed:   false,
		FallbackReason: `semantic_not_ready: type "movie" coverage 40% below threshold`,
	}, false)

	// Existing keys must stay present and unchanged (byte-stable contract).
	for _, key := range []string{"total", "total_exact", "has_more", "items"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("response missing existing key %q: %v", key, body)
		}
	}
	if body["total"].(float64) != 3 {
		t.Fatalf("total = %v, want 3", body["total"])
	}
	if body["total_exact"].(bool) != true {
		t.Fatalf("total_exact = %v, want true", body["total_exact"])
	}

	diagRaw, ok := body["search_diagnostics"]
	if !ok {
		t.Fatalf("expected search_diagnostics in response: %v", body)
	}
	diag := diagRaw.(map[string]any)
	if diag["provider"] != catalog.SearchProviderMeilisearch {
		t.Fatalf("provider = %v, want %q", diag["provider"], catalog.SearchProviderMeilisearch)
	}
	if diag["mode"] != "keyword" {
		t.Fatalf("mode = %v, want keyword", diag["mode"])
	}
	if diag["semantic_used"].(bool) != false {
		t.Fatalf("semantic_used = %v, want false", diag["semantic_used"])
	}
	if diag["fallback_reason"] != `semantic_not_ready: type "movie" coverage 40% below threshold` {
		t.Fatalf("fallback_reason = %v", diag["fallback_reason"])
	}
}

func TestWriteCatalogResponse_DiagnosticsHybridOmitsFallbackReason(t *testing.T) {
	body := decodeCatalogResponse(t, &catalog.CatalogResult{
		Provider:           catalog.SearchProviderMeilisearch,
		Mode:               "hybrid",
		SemanticUsed:       true,
		IndexPendingEvents: 7,
	}, false)

	diagRaw, ok := body["search_diagnostics"]
	if !ok {
		t.Fatalf("expected search_diagnostics in response: %v", body)
	}
	diag := diagRaw.(map[string]any)
	if diag["semantic_used"].(bool) != true {
		t.Fatalf("semantic_used = %v, want true", diag["semantic_used"])
	}
	if diag["mode"] != "hybrid" {
		t.Fatalf("mode = %v, want hybrid", diag["mode"])
	}
	if _, ok := diag["fallback_reason"]; ok {
		t.Fatalf("fallback_reason should be omitted when empty: %v", diag)
	}
	if diag["index_pending_updates"].(float64) != 7 {
		t.Fatalf("index_pending_updates = %v, want 7", diag["index_pending_updates"])
	}
}

func TestWriteCatalogResponse_NoProviderOmitsDiagnostics(t *testing.T) {
	// Browse / preview / non-relevance q= paths never set Provider.
	body := decodeCatalogResponse(t, &catalog.CatalogResult{
		Total:      5,
		TotalExact: true,
	}, false)

	if _, ok := body["search_diagnostics"]; ok {
		t.Fatalf("search_diagnostics should be omitted when no provider search ran: %v", body)
	}
}

func TestWriteCatalogResponse_GroupedByWorkOmitsDiagnostics(t *testing.T) {
	// group=work builds a fresh CatalogResult with an empty Provider.
	body := decodeCatalogResponse(t, &catalog.CatalogResult{
		Total:      2,
		TotalExact: true,
	}, true)

	if _, ok := body["search_diagnostics"]; ok {
		t.Fatalf("grouped response should omit search_diagnostics: %v", body)
	}
	// Grouped responses force total_exact false regardless of result.TotalExact.
	if body["total_exact"].(bool) != false {
		t.Fatalf("grouped total_exact = %v, want false", body["total_exact"])
	}
}
