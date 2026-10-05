package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// With recommendations disabled (no engine), Discover still answers the
// reader's rows, so the default rows show, and each links to its live
// section page.
func TestDiscoverServesDefaultRowsWithRecommendationsDisabled(t *testing.T) {
	t.Parallel()

	handler := NewRecommendationsHandler(nil, stubRecommendationsReader{
		discoverRows: []recommendations.ForYouRow{
			{Type: recommendations.RecTypeTopRated, Label: "Highly Rated in Your Library", Items: []recommendations.ScoredItem{{MediaItemID: "movie-1"}}},
			{Type: recommendations.RecTypeRecentlyAdded, Label: "Recently Added", Items: []recommendations.ScoredItem{{MediaItemID: "movie-1"}}},
		},
	}, nil, nil, nil, false)
	handler.Fetcher = stubDiscoverFetcher{items: []*models.MediaItem{{ContentID: "movie-1", Type: "movie", Title: "Movie One", Status: "matched"}}}

	view, err := handler.Discover(t.Context(), 7, "profile-1", catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Rows) != 2 {
		t.Fatalf("rows = %+v, want both default rows", view.Rows)
	}
	for i, want := range []string{recommendations.SectionKindTopRated, recommendations.SectionKindRecentlyAdded} {
		if view.Rows[i].SectionKind != want || len(view.Rows[i].Items) != 1 {
			t.Fatalf("row %d = %+v, want kind %q with its card", i, view.Rows[i], want)
		}
	}
}
