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

// Movie and series detail carries the extended artwork; the card members and
// the frozen v1 detail document stay as they were.
func TestV2ItemDetailCarriesExtendedArtwork(t *testing.T) {
	detail := &catalogpkg.ItemDetail{
		ContentID: "movie-1", Type: "movie", Title: "Film",
		PosterURL: "https://cdn.example/p", BackdropURL: "https://cdn.example/b0",
		BannerURL: "https://cdn.example/banner", BannerThumbhash: "bh",
		ThumbURL: "https://cdn.example/thumb", ThumbThumbhash: "th",
		ClearArtURL: "https://cdn.example/clearart", DiscURL: "https://cdn.example/disc",
		BackdropURLs: []string{"https://cdn.example/b0", "https://cdn.example/b1"},
	}
	wire, err := json.Marshal(catalogItemDetailOf(detail, ratingsources.Selection{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"banner_url":"https://cdn.example/banner"`, `"banner_thumbhash":"bh"`,
		`"thumb_url":"https://cdn.example/thumb"`, `"thumb_thumbhash":"th"`,
		`"clearart_url":"https://cdn.example/clearart"`, `"disc_url":"https://cdn.example/disc"`,
		`"backdrop_urls":["https://cdn.example/b0","https://cdn.example/b1"]`,
		`"backdrop_url":"https://cdn.example/b0"`, `"poster_url":"https://cdn.example/p"`,
	} {
		if !strings.Contains(string(wire), want) {
			t.Fatalf("missing %s in %s", want, wire)
		}
	}
	card, err := json.Marshal(catalogItemCardOf(detail))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"banner", "thumb_url", "clearart", "disc_url", "backdrop_urls", "Banner", "BackdropURLs"} {
		if strings.Contains(string(card), leaked) || strings.Contains(string(legacy), leaked) {
			t.Fatalf("%s leaked into a card or the v1 detail: card=%s v1=%s", leaked, card, legacy)
		}
	}

	// Without extended artwork the members are absent, not empty.
	bare, err := json.Marshal(catalogItemDetailOf(&catalogpkg.ItemDetail{ContentID: "movie-2", Type: "movie"}, ratingsources.Selection{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"banner_url", "thumb_url", "clearart_url", "disc_url", "backdrop_urls"} {
		if strings.Contains(string(bare), absent) {
			t.Fatalf("%s present without artwork: %s", absent, bare)
		}
	}
}
