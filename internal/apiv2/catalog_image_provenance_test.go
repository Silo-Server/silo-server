package apiv2

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

func TestV2ImageProvenancePreservesFalse(t *testing.T) {
	card := catalogItemOfSection(handlers.SectionItemView{Type: "episode", PosterIsEpisodeStill: new(false), BackdropIsEpisodeStill: new(true)})
	listing := catalogItemOfListing(handlers.CollectionItemView{Type: "episode", PosterIsEpisodeStill: new(false), BackdropIsEpisodeStill: new(false)})
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
