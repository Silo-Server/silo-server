package catalog

import "testing"

func TestCatalogQueryValidationKeepsRelevanceScoped(t *testing.T) {
	for _, tc := range []struct {
		name      string
		source    CatalogSource
		q         string
		sort      QuerySort
		wantError bool
	}{
		{"search", CatalogSourceQuery, "heat", QuerySort{Field: "relevance", Order: "desc"}, false},
		{"empty text", CatalogSourceQuery, " ", QuerySort{Field: "relevance"}, true},
		{"personal source", CatalogSourceFavorites, "heat", QuerySort{Field: "relevance"}, true},
		{"invalid order", CatalogSourceQuery, "heat", QuerySort{Field: "relevance", Order: "sideways"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := CatalogRequest{Source: tc.source, SearchQuery: tc.q, Query: QueryDefinition{Sort: tc.sort}}
			err := req.ValidateQueryDefinition()
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, wantError=%v", err, tc.wantError)
			}
			if req.Query.Sort != tc.sort {
				t.Fatalf("validation changed sort: %+v", req.Query.Sort)
			}
		})
	}
	// Persisted collection rules must still reject a search-only ordering.
	if err := (QueryDefinition{Sort: QuerySort{Field: "relevance"}}).Validate(); err == nil {
		t.Fatal("saved collection accepted relevance")
	}
}
