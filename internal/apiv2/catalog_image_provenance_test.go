package apiv2

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
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

// Episode cards in Continue Watching and Next Up rows show the season
// backdrop, then the series backdrop, and the episode still only when neither
// exists. Other rows keep the backdrop the section chose.
func TestV2ContinueWatchingEpisodeCardBackdrop(t *testing.T) {
	still := handlers.SectionItemView{ContentID: "episode:1", Type: "episode", BackdropURL: "/still", BackdropThumbhash: "still-hash", BackdropIsEpisodeStill: new(true)}
	season := handlers.EpisodeParentArtworkView{SeasonBackdropURL: "/season", SeasonBackdropThumbhash: "season-hash"}
	series := handlers.EpisodeParentArtworkView{SeriesBackdropURL: "/series", SeriesBackdropThumbhash: "series-hash"}
	both := season
	both.SeriesBackdropURL, both.SeriesBackdropThumbhash = series.SeriesBackdropURL, series.SeriesBackdropThumbhash
	for _, tc := range []struct {
		name, sectionType string
		parents           handlers.EpisodeParentArtworkView
		wantURL, wantHash string
		wantStill         bool
	}{
		{"continue watching with season art", "continue_watching", both, "/season", "season-hash", false},
		{"next up with series art only", "next_up", series, "/series", "series-hash", false},
		{"continue watching without parent art", "continue_watching", handlers.EpisodeParentArtworkView{}, "/still", "still-hash", true},
		{"recently added", "recently_added", both, "/still", "still-hash", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			card := still
			card.ParentArtwork = tc.parents
			out := sectionOf(handlers.SectionView{SectionType: tc.sectionType, Items: []handlers.SectionItemView{card}}, ratingsources.Selection{}).Items[0]
			if out.BackdropURL != tc.wantURL || out.BackdropThumbhash != tc.wantHash {
				t.Errorf("backdrop = %q/%q, want %q/%q", out.BackdropURL, out.BackdropThumbhash, tc.wantURL, tc.wantHash)
			}
			if out.BackdropIsEpisodeStill == nil || *out.BackdropIsEpisodeStill != tc.wantStill {
				t.Errorf("backdrop_is_episode_still = %v, want %v", out.BackdropIsEpisodeStill, tc.wantStill)
			}
		})
	}
}
