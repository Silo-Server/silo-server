package apiv2

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/ratingsources"
)

func TestV2ImageProvenancePreservesFalse(t *testing.T) {
	card := catalogItemOfSection(handlers.SectionItemView{Type: "episode", PosterIsEpisodeStill: new(false), BackdropIsEpisodeStill: new(true)}, ratingsources.Selection{})
	listing := catalogItemOfListing(handlers.CollectionItemView{Type: "episode", PosterIsEpisodeStill: new(false), BackdropIsEpisodeStill: new(false)}, ratingsources.Selection{})
	rows := episodesOf([]handlers.EpisodeView{{StillIsEpisodeStill: new(false)}})
	for _, tc := range []struct {
		view any
		want []string
	}{
		{card, []string{`"poster_is_episode_still":false`, `"backdrop_is_episode_still":true`}},
		{listing, []string{`"poster_is_episode_still":false`, `"backdrop_is_episode_still":false`}},
		{rows[0], []string{`"still_is_episode_still":false`}},
	} {
		wire, err := json.Marshal(tc.view)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range tc.want {
			if !strings.Contains(string(wire), want) {
				t.Fatalf("missing %s in %s", want, wire)
			}
		}
	}
}

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
