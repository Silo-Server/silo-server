package sections

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestMarkEpisodeStillProvenanceFollowsSelectedArtwork(t *testing.T) {
	for _, tc := range []struct {
		name, poster, backdrop, still string
		posterStill, backdropStill    bool
	}{
		{"still and season poster", "season/poster", "episode/still", "episode/still", false, true},
		{"series fallback", "series/poster", "series/backdrop", "", false, false},
		{"still in poster fallback", "episode/still", "series/backdrop", "episode/still", true, false},
		{"external provider still", "series/poster", "https://example.invalid/image", "https://example.invalid/image", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := &models.MediaItem{PosterPath: tc.poster, BackdropPath: tc.backdrop}
			markEpisodeStillProvenance(item, tc.still)
			if *item.PosterIsEpisodeStill != tc.posterStill || *item.BackdropIsEpisodeStill != tc.backdropStill {
				t.Fatalf("provenance = %v / %v", *item.PosterIsEpisodeStill, *item.BackdropIsEpisodeStill)
			}
		})
	}
}
