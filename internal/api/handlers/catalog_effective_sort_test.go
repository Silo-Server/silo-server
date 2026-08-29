package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// TestCatalogSortMetricField covers which sort field drives sort_metrics. A
// personal list with a saved preference returns items in that order while the
// request itself carried no sort, so enrichment has to follow EffectiveSort or
// the client renders a metric that does not explain the ordering it sees.
func TestCatalogSortMetricField(t *testing.T) {
	for _, tc := range []struct {
		name   string
		req    catalog.CatalogRequest
		result *catalog.CatalogResult
		want   string
	}{
		{
			name:   "saved preference with no request sort",
			req:    catalog.CatalogRequest{Source: catalog.CatalogSourceFavorites},
			result: &catalog.CatalogResult{EffectiveSort: catalog.QuerySort{Field: "runtime", Order: "desc"}},
			want:   "runtime",
		},
		{
			name: "explicit request sort",
			req: catalog.CatalogRequest{
				Source: catalog.CatalogSourceWatchlist,
				Query:  catalog.QueryDefinition{Sort: catalog.QuerySort{Field: "title", Order: "asc"}},
			},
			result: &catalog.CatalogResult{EffectiveSort: catalog.QuerySort{Field: "title", Order: "asc"}},
			want:   "title",
		},
		{
			name:   "source order falls back to the normalized request sort",
			req:    catalog.CatalogRequest{Source: catalog.CatalogSourceWatchlist},
			result: &catalog.CatalogResult{},
			want:   catalog.NormalizeQuerySort(catalog.QuerySort{}).Field,
		},
		{
			name: "request sort wins when the result reports none",
			req: catalog.CatalogRequest{
				Query: catalog.QueryDefinition{Sort: catalog.QuerySort{Field: "year", Order: "desc"}},
			},
			result: &catalog.CatalogResult{},
			want:   "year",
		},
		{
			name:   "nil result is tolerated",
			req:    catalog.CatalogRequest{Query: catalog.QueryDefinition{Sort: catalog.QuerySort{Field: "year"}}},
			result: nil,
			want:   "year",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := catalogSortMetricField(tc.req, tc.result); got != tc.want {
				t.Fatalf("catalogSortMetricField = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestWriteCatalogResponseKeepsEffectiveSortWhenGrouped guards the group=work
// response: grouping builds a fresh CatalogResult, and effective_sort must
// survive it so a grouped Watchlist/Favorites page can still show the sort that
// actually ordered it.
func TestWriteCatalogResponseKeepsEffectiveSortWhenGrouped(t *testing.T) {
	body := decodeCatalogResponse(t, &catalog.CatalogResult{
		Total:         2,
		EffectiveSort: catalog.QuerySort{Field: "title", Order: "asc"},
	}, true)

	sort, ok := body["effective_sort"].(map[string]any)
	if !ok {
		t.Fatalf("effective_sort missing from grouped response: %v", body)
	}
	if sort["field"] != "title" || sort["order"] != "asc" {
		t.Fatalf("effective_sort = %v, want title/asc", sort)
	}
}
