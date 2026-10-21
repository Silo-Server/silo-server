package apiv2

import (
	"testing"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/ratingsources"
)

// An episode detail's poster is the episode's still. When its URL doesn't
// resolve, the still's ThumbHash is still served and must stay protected.
func TestV2EpisodeDetailPosterStaysAStillWithoutURL(t *testing.T) {
	detail := &catalogpkg.ItemDetail{ContentID: "episode:1", Type: "episode", Title: "Pilot", PosterThumbhash: "still-hash"}
	out := catalogItemDetailOf(detail, ratingsources.Selection{})
	if out.PosterIsEpisodeStill == nil || !*out.PosterIsEpisodeStill {
		t.Fatalf("poster_is_episode_still = %v with the still's ThumbHash served", out.PosterIsEpisodeStill)
	}
	if out.BackdropIsEpisodeStill == nil || *out.BackdropIsEpisodeStill {
		t.Fatalf("backdrop_is_episode_still = %v for the series backdrop", out.BackdropIsEpisodeStill)
	}
}
