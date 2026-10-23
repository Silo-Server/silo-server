package markers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCreditsLimitsForFileUsesEpisodeWindowForEpisodes(t *testing.T) {
	// A 40-minute file: the episode window opens at 32:30, the movie one at 30:00.
	for _, tc := range []struct {
		name string
		file *models.MediaFile
		want float64
	}{
		{"linked episode", &models.MediaFile{EpisodeID: "episode"}, 1950},
		// The scanner writes sidecar markers before the episode is linked.
		{"episode not linked yet", &models.MediaFile{SeasonNumber: 1, EpisodeNumber: 3}, 1950},
		{"movie", &models.MediaFile{ContentID: "movie"}, 1800},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CreditsLimitsForFile(tc.file).WindowStart(2400); got != tc.want {
				t.Fatalf("window start = %v, want %v", got, tc.want)
			}
		})
	}
}
