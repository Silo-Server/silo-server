package metadata_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/metadata/nfo"
)

// A series an admin identified keeps its chosen identity when its folder's
// tvshow.nfo names another show, and none of that folder's NFOs (series,
// season or episode) are applied over the chosen show.
func TestManualRefresh_IdentifiedSeriesSkipsConflictingNFOsAtEveryDepth(t *testing.T) {
	root := filepath.Join(t.TempDir(), "TV", "Real Show")
	writeFixtureFile(t, filepath.Join(root, "tvshow.nfo"),
		`<tvshow><title>Wrong Show</title><uniqueid type="tvdb">999</uniqueid></tvshow>`)
	s1 := filepath.Join(root, "Season 01")
	writeFixtureFile(t, filepath.Join(s1, "season.nfo"), `<season><title>Wrong Season</title></season>`)
	e1 := filepath.Join(s1, "Real Show S01E01.mkv")
	writeFixtureFile(t, e1, "video")
	writeFixtureFile(t, filepath.Join(s1, "Real Show S01E01.nfo"),
		`<episodedetails><title>Wrong Episode</title></episodedetails>`)

	x := metadata.NewNFOSeriesHarness()
	ctx := context.Background()
	const seriesID = "series:tvdb:81189"
	if err := x.SeedSeriesSkeleton(seriesID, "Real Show"); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}
	x.SeedSeriesFile(100, 10, e1, root, seriesID)
	x.ChooseIdentity(seriesID, map[string]string{"tvdb": "81189"})

	remote := &remoteEpisodeStubProvider{
		slug:     "tvdb",
		metadata: &metadata.MetadataResult{HasMetadata: true, Title: "Real Show", ProviderIDs: map[string]string{"tvdb": "81189"}},
		seasons:  []metadata.SeasonResult{{SeasonNumber: 1, Title: "Season 1"}},
		episodes: map[int][]metadata.EpisodeResult{1: {{SeasonNumber: 1, EpisodeNumber: 1, Title: "Remote Episode"}}},
	}
	if _, err := x.Service().ProcessWithProviders(ctx, metadata.ProcessRequest{
		ContentID: seriesID,
		Language:  "en",
		Mode:      metadata.ModeManualRefresh,
	}, []metadata.Provider{nfo.NewProvider(), remote}); err != nil {
		t.Fatalf("manual refresh: %v", err)
	}

	item, err := x.Item(seriesID)
	if err != nil {
		t.Fatalf("load series: %v", err)
	}
	if item.TvdbID != "81189" || item.Title != "Real Show" {
		t.Errorf("series = tvdb %q title %q, want the chosen Real Show (81189)", item.TvdbID, item.Title)
	}
	if seasons := x.Seasons(seriesID); len(seasons) != 1 || seasons[0].Title != "Season 1" {
		t.Errorf("seasons = %#v, want the remote season name", seasons)
	}
	if episodes := x.Episodes(seriesID); len(episodes) != 1 || episodes[0].Title != "Remote Episode" {
		t.Errorf("episodes = %#v, want the remote episode title", episodes)
	}
}
