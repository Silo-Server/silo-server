package sections

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestBecauseWatchedTitleUsesSelectedAccessibleAnchor(t *testing.T) {
	for _, tc := range []struct {
		name, fallback, source string
		items                  []*models.MediaItem
		want                   string
	}{
		{"selected anchor", "Because You Watched", "older", []*models.MediaItem{{ContentID: "latest", Title: "Latest Watch"}, {ContentID: "older", Title: "Actual Anchor"}}, "Because You Watched Actual Anchor"},
		{"restricted or deleted anchor", "Because You Watched", "hidden", []*models.MediaItem{{ContentID: "other", Title: "Other"}}, "Because You Watched"},
		{"empty title", "Because You Watched", "empty", []*models.MediaItem{{ContentID: "empty", Title: "  "}}, "Because You Watched"},
		{"empty section title", "", "older", []*models.MediaItem{{ContentID: "older", Title: "Actual Anchor"}}, "Because You Watched Actual Anchor"},
		{"empty section title without anchor", "", "hidden", nil, "Because You Watched"},
		{"custom section title", "More Like Your Last Watch", "older", []*models.MediaItem{{ContentID: "older", Title: "Actual Anchor"}}, "More Like Your Last Watch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := becauseWatchedTitle(tc.fallback, tc.source, tc.items); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
