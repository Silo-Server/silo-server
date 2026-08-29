package sections

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestEpisodeCatalogArtworkIDsPreserveRecentTVBackdropPolicy(t *testing.T) {
	seriesBackdropIDs, episodeStillIDs := episodeCatalogArtworkIDs([]*models.MediaItem{
		nil,
		{ContentID: "movie-1", Type: "movie"},
		{ContentID: "catalog-episode", Type: "episode"},
		{ContentID: "recent-tv-episode", Type: "episode", PlayContentID: "recent-tv-episode"},
	})

	if len(seriesBackdropIDs) != 1 || seriesBackdropIDs[0] != "catalog-episode" {
		t.Fatalf("series backdrop IDs = %v, want [catalog-episode]", seriesBackdropIDs)
	}
	if len(episodeStillIDs) != 1 || episodeStillIDs[0] != "recent-tv-episode" {
		t.Fatalf("episode still IDs = %v, want [recent-tv-episode]", episodeStillIDs)
	}
}

func TestChooseEpisodeArtworkTracksOwningCandidate(t *testing.T) {
	seasonOwner := SectionArtworkOwner{Kind: SectionArtworkOwnerSeason, ContentID: "season-1"}
	seriesOwner := SectionArtworkOwner{Kind: SectionArtworkOwnerSeries, ContentID: "series-1"}
	episodeOwner := SectionArtworkOwner{Kind: SectionArtworkOwnerEpisode, ContentID: "episode-1"}

	tests := []struct {
		name       string
		candidates []episodeArtworkCandidate
		wantPath   string
		wantOwner  SectionArtworkOwner
	}{
		{
			name: "season poster wins",
			candidates: []episodeArtworkCandidate{
				{path: "season.jpg", owner: seasonOwner},
				{path: "series.jpg", owner: seriesOwner},
				{path: "still.jpg", owner: episodeOwner},
			},
			wantPath:  "season.jpg",
			wantOwner: seasonOwner,
		},
		{
			name: "series poster fallback",
			candidates: []episodeArtworkCandidate{
				{owner: seasonOwner},
				{path: "series.jpg", owner: seriesOwner},
				{path: "still.jpg", owner: episodeOwner},
			},
			wantPath:  "series.jpg",
			wantOwner: seriesOwner,
		},
		{
			name: "episode still fallback",
			candidates: []episodeArtworkCandidate{
				{owner: seasonOwner},
				{owner: seriesOwner},
				{path: "still.jpg", owner: episodeOwner},
			},
			wantPath:  "still.jpg",
			wantOwner: episodeOwner,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, _, owner := chooseEpisodeArtwork(tt.candidates...)
			if path != tt.wantPath {
				t.Fatalf("path = %q, want %q", path, tt.wantPath)
			}
			if owner != tt.wantOwner {
				t.Fatalf("owner = %+v, want %+v", owner, tt.wantOwner)
			}
		})
	}
}

func TestChooseEpisodeArtworkPreservesIndependentThumbhashPrecedence(t *testing.T) {
	seasonOwner := SectionArtworkOwner{Kind: SectionArtworkOwnerSeason, ContentID: "season-1"}
	seriesOwner := SectionArtworkOwner{Kind: SectionArtworkOwnerSeries, ContentID: "series-1"}

	path, thumbhash, owner := chooseEpisodeArtwork(
		episodeArtworkCandidate{path: "season.jpg", owner: seasonOwner},
		episodeArtworkCandidate{path: "series.jpg", thumbhash: "series-hash", owner: seriesOwner},
	)
	if path != "season.jpg" || thumbhash != "series-hash" || owner != seasonOwner {
		t.Fatalf("selection = (%q, %q, %+v), want season path/owner with series thumbhash", path, thumbhash, owner)
	}
}
