package metadata

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestMergeAndPersistKeepsLockedItemArtwork(t *testing.T) {
	for _, mode := range []RefreshMode{ModeScheduledRefresh, ModeManualRefresh} {
		h := newTestHarness()
		ctx := context.Background()
		const contentID = "series:tmdb:1"
		if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
			ContentID:        contentID,
			Type:             "series",
			Title:            "Example Show",
			Status:           "matched",
			TmdbID:           "1",
			PosterPath:       "tmdb/series/1/poster/manual/original.webp",
			PosterSourcePath: "tmdb://manual-poster.jpg",
			PosterThumbhash:  "manual-thumb",
			LockedFields:     []int{int(FieldImages)},
			Studios:          []string{},
			Networks:         []string{},
			Countries:        []string{},
			Genres:           []string{},
		}); err != nil {
			t.Fatalf("upsert existing item: %v", err)
		}
		providerRepo := newFakeProviderIDRepo()
		providerRepo.set(contentID, &models.MediaItemProviderID{
			ContentID:  contentID,
			ItemType:   "series",
			Provider:   "tmdb",
			ProviderID: "1",
		})
		h.service.providerIDRepo = providerRepo

		_, err := h.service.mergeAndPersist(ctx, ProcessRequest{
			ContentID: contentID,
			Mode:      mode,
			Language:  "en",
		}, &MetadataResult{
			HasMetadata: true,
			Title:       "Example Show",
			ProviderIDs: map[string]string{"tmdb": "1"},
		}, []RemoteImage{
			{Type: ImagePoster, URL: "tmdb://provider-poster.jpg", ProviderID: "tmdb", Rating: 5.5, Language: "en"},
			{Type: ImageBackdrop, URL: "tmdb://provider-backdrop.jpg", ProviderID: "tmdb", Rating: 5.5},
		}, nil, nil, "series")
		if err != nil {
			t.Fatalf("mode %d: mergeAndPersist: %v", mode, err)
		}

		got, err := h.itemRepo.GetByID(ctx, contentID)
		if err != nil {
			t.Fatalf("mode %d: load item: %v", mode, err)
		}
		// A changed source path would let the queued cache job replace the
		// manual poster once it finishes.
		if got.PosterPath != "tmdb/series/1/poster/manual/original.webp" ||
			got.PosterSourcePath != "tmdb://manual-poster.jpg" ||
			got.PosterThumbhash != "manual-thumb" {
			t.Fatalf("mode %d: locked poster changed: path=%q source=%q thumb=%q",
				mode, got.PosterPath, got.PosterSourcePath, got.PosterThumbhash)
		}
		if got.BackdropSourcePath != "tmdb://provider-backdrop.jpg" {
			t.Fatalf("mode %d: empty backdrop slot was not filled: source=%q", mode, got.BackdropSourcePath)
		}
	}
}

func TestPersistSeasonsAndEpisodesKeepsLockedSeriesArtwork(t *testing.T) {
	const seriesID = "series-tvdb-123"
	service, _, seasonRepo, episodeRepo := newSeasonEpisodeServiceForTest(seriesID)
	enqueuer := &recordingImageCacheJobEnqueuer{}
	service.SetAutoCacheImages(true)
	service.SetImageCacheJobEnqueuer(enqueuer)

	seasonRepo.seasons[seasonKey(seriesID, 1)] = &models.Season{
		ContentID:        "season-1",
		SeriesID:         seriesID,
		SeasonNumber:     1,
		Title:            "Season 1",
		PosterPath:       "tvdb/series/123/seasons/1/poster/manual/original.webp",
		PosterSourcePath: "tvdb://manual-season.jpg",
		PosterThumbhash:  "manual-season-thumb",
	}
	episodeRepo.episodes[episodeKey(seriesID, 1, 1)] = &models.Episode{
		ContentID:       "episode-1",
		SeriesID:        seriesID,
		SeasonID:        "season-1",
		SeasonNumber:    1,
		EpisodeNumber:   1,
		Title:           "Pilot",
		StillPath:       "tvdb/series/123/seasons/1/episodes/1/still/manual/original.webp",
		StillSourcePath: "tvdb://manual-still.jpg",
		StillThumbhash:  "manual-still-thumb",
	}

	series := &models.MediaItem{
		ContentID:    seriesID,
		Type:         "series",
		TvdbID:       "123",
		LockedFields: []int{int(FieldImages)},
	}
	service.persistSeasonsAndEpisodes(
		context.Background(),
		series,
		map[string]string{"tvdb": "123"},
		"en",
		"en",
		[]SeasonResult{
			{SeasonNumber: 1, Title: "Season 1", PosterPath: "tvdb://provider-season.jpg"},
			{SeasonNumber: 2, Title: "Season 2", PosterPath: "tvdb://provider-season-2.jpg"},
		},
		[]EpisodeResult{{
			ProviderIDs:   map[string]string{"tvdb": "ep-1"},
			SeasonNumber:  1,
			EpisodeNumber: 1,
			Title:         "Pilot",
			StillPath:     "tvdb://provider-still.jpg",
		}},
		MergeReplaceUnlocked,
	)

	season := seasonRepo.seasons[seasonKey(seriesID, 1)]
	if season.PosterPath != "tvdb/series/123/seasons/1/poster/manual/original.webp" ||
		season.PosterSourcePath != "tvdb://manual-season.jpg" ||
		season.PosterThumbhash != "manual-season-thumb" {
		t.Fatalf("locked season poster changed: path=%q source=%q thumb=%q",
			season.PosterPath, season.PosterSourcePath, season.PosterThumbhash)
	}
	episode := episodeRepo.episodes[episodeKey(seriesID, 1, 1)]
	if episode.StillPath != "tvdb/series/123/seasons/1/episodes/1/still/manual/original.webp" ||
		episode.StillSourcePath != "tvdb://manual-still.jpg" ||
		episode.StillThumbhash != "manual-still-thumb" {
		t.Fatalf("locked episode still changed: path=%q source=%q thumb=%q",
			episode.StillPath, episode.StillSourcePath, episode.StillThumbhash)
	}
	newSeason := seasonRepo.seasons[seasonKey(seriesID, 2)]
	if newSeason == nil || newSeason.PosterSourcePath != "tvdb://provider-season-2.jpg" {
		t.Fatalf("new season did not receive provider artwork: %+v", newSeason)
	}
	for _, job := range enqueuer.inputs {
		switch job.SourcePath {
		case "tvdb://provider-season.jpg", "tvdb://provider-still.jpg":
			t.Fatalf("queued provider artwork over a locked selection: %+v", job)
		}
	}
}
