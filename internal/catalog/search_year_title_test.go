package catalog

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestSearchBlockHasExactTitleAcceptsOwnYearSuffix(t *testing.T) {
	for _, tc := range []struct {
		name string
		item models.MediaItem
		want bool
	}{
		{"bare title", models.MediaItem{Type: "movie", Title: "Castle", Year: 2009}, true},
		{"own year", models.MediaItem{Type: "series", Title: "Castle (2009)", Year: 2009}, true},
		{"other year", models.MediaItem{Type: "series", Title: "Castle (2010)", Year: 2009}, false},
		{"no year", models.MediaItem{Type: "series", Title: "Castle 0", Year: 0}, false},
		{"episode", models.MediaItem{Type: "episode", Title: "Castle (2009)", Year: 2009}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := searchBlockHasExactTitle([]*models.MediaItem{&tc.item}, "castle"); got != tc.want {
				t.Fatalf("searchBlockHasExactTitle = %v, want %v", got, tc.want)
			}
		})
	}
}
