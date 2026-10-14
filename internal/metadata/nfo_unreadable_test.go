package metadata_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/metadata/nfo"
)

// One unreadable episode NFO costs only that episode's NFO data in a
// fill-empty pass, and stops a manual refresh of the series before it
// replaces the stored values.
func TestSeriesWithUnreadableEpisodeNFO(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: file permissions do not block reads")
	}
	root, episodeFiles := buildFitnessFixture(t)
	locked := filepath.Join(root, "Season 02", "P90X S02E01 - Core.nfo")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	x := metadata.NewNFOSeriesHarness()
	ctx := context.Background()
	const seriesID = "local-p90x"
	if err := x.SeedSeriesSkeleton(seriesID, "P90X"); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}
	hints := &metadata.MatchHints{
		Title:                     "P90X",
		Type:                      "series",
		FilePath:                  episodeFiles[0],
		RepresentativeFilePath:    episodeFiles[0],
		AllGroupFilePaths:         episodeFiles,
		PrimarySidecarSearchPaths: []string{root},
	}
	result, err := x.Service().ProcessWithProviders(ctx, metadata.ProcessRequest{
		ContentID: seriesID, Hints: hints, Language: "en", Mode: metadata.ModeInitialMatch,
	}, []metadata.Provider{nfo.NewProvider()})
	if err != nil || result == nil || !result.Updated {
		t.Fatalf("initial match = %#v, %v; want the series matched", result, err)
	}
	episodes := x.Episodes(seriesID)
	if len(episodes) == 0 || episodes[0].Title != "Chest and Back" {
		t.Fatalf("episodes = %#v, want S1E1 titled from its readable NFO", episodes)
	}
	for _, episode := range episodes {
		if episode.Title == "Core" {
			t.Errorf("S2E1 = %#v, want no title from its unreadable NFO", episode)
		}
	}

	_, err = x.Service().ProcessWithProviders(ctx, metadata.ProcessRequest{
		ContentID: seriesID, Hints: hints, Language: "en", Mode: metadata.ModeManualRefresh,
	}, []metadata.Provider{nfo.NewProvider()})
	if !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
		t.Fatalf("manual refresh error = %v, want ErrLocalMetadataUnavailable", err)
	}
}

// A season folder that no longer holds its cataloged episode (an unmounted
// share's mountpoint, here with an unrelated video in it) stops a manual
// refresh at the seasons, before season.nfo values are replaced.
func TestSeriesSeasonFolderWithoutItsEpisodes(t *testing.T) {
	root, episodeFiles := buildFitnessFixture(t)

	x := metadata.NewNFOSeriesHarness()
	ctx := context.Background()
	const seriesID = "local-p90x"
	if err := x.SeedSeriesSkeleton(seriesID, "P90X"); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}
	hints := &metadata.MatchHints{
		Title:                     "P90X",
		Type:                      "series",
		FilePath:                  episodeFiles[0],
		RepresentativeFilePath:    episodeFiles[0],
		AllGroupFilePaths:         episodeFiles,
		PrimarySidecarSearchPaths: []string{root},
	}
	if _, err := x.Service().ProcessWithProviders(ctx, metadata.ProcessRequest{
		ContentID: seriesID, Hints: hints, Language: "en", Mode: metadata.ModeInitialMatch,
	}, []metadata.Provider{nfo.NewProvider()}); err != nil {
		t.Fatalf("initial match: %v", err)
	}

	if err := os.Remove(episodeFiles[1]); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "Season 02", "Unrelated.mkv"), []byte("video"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := x.Service().ProcessWithProviders(ctx, metadata.ProcessRequest{
		ContentID: seriesID, Hints: hints, Language: "en", Mode: metadata.ModeManualRefresh,
	}, []metadata.Provider{nfo.NewProvider()})
	if !errors.Is(err, metadata.ErrLocalMetadataUnavailable) || !strings.Contains(err.Error(), "seasons:") {
		t.Fatalf("manual refresh error = %v, want ErrLocalMetadataUnavailable from the seasons", err)
	}
}
