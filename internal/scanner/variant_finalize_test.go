package scanner

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestVariantPartTotals(t *testing.T) {
	tests := []struct {
		name       string
		folder     *models.MediaFolder
		files      []*models.MediaFile
		wantTotals []int
	}{
		{
			name:   "lone part number in a movie title",
			folder: &models.MediaFolder{Type: "movies", Paths: []string{"/movies"}},
			files: []*models.MediaFile{
				{ID: 1, ContentID: "movie:1", FilePath: "/movies/Mockingjay Part 2 (2015)/The.Hunger.Games.Mockingjay.Part.2.2015.BluRay.1080p.mkv"},
			},
			wantTotals: []int{0},
		},
		{
			name:   "versions of one episode with a part number in its title",
			folder: &models.MediaFolder{Type: "series", Paths: []string{"/tv"}},
			files: []*models.MediaFile{
				{ID: 1, EpisodeID: "episode:1", FilePath: "/tv/True Detective/Season 04/True.Detective.S04E04.Part.4.2160p.MAX.WEB-DL.mkv"},
				{ID: 2, EpisodeID: "episode:1", FilePath: "/tv/True Detective/Season 04/True.Detective.S04E04.Part.4.1080p.HMAX.WEB-DL.mkv"},
			},
			wantTotals: []int{0, 0},
		},
		{
			name:   "movie split across discs",
			folder: &models.MediaFolder{Type: "movies", Paths: []string{"/movies"}},
			files: []*models.MediaFile{
				{ID: 1, ContentID: "movie:1", FilePath: "/movies/Movie (2010)/Movie.2010.CD1.mkv"},
				{ID: 2, ContentID: "movie:1", FilePath: "/movies/Movie (2010)/Movie.2010.CD2.mkv"},
			},
			wantTotals: []int{2, 2},
		},
		{
			name:   "episode split into parts",
			folder: &models.MediaFolder{Type: "series", Paths: []string{"/tv"}},
			files: []*models.MediaFile{
				{ID: 1, EpisodeID: "episode:1", FilePath: "/tv/Show/Season 01/Show.S01E01.Part.1.mkv"},
				{ID: 2, EpisodeID: "episode:1", FilePath: "/tv/Show/Season 01/Show.S01E01.Part.2.mkv"},
				{ID: 3, EpisodeID: "episode:1", FilePath: "/tv/Show/Season 01/Show.S01E01.Part.3.mkv"},
			},
			wantTotals: []int{3, 3, 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			totals := variantPartTotals(tt.files, tt.folder)
			for i, file := range tt.files {
				got := 0
				if groupKey, ok := variantPartGroupKey(stableOwnerKey(file), variantHintsForFile(file, tt.folder)); ok {
					got = totals[groupKey]
				}
				if got != tt.wantTotals[i] {
					t.Errorf("%s part total = %d, want %d", file.FilePath, got, tt.wantTotals[i])
				}
			}
		})
	}
}
