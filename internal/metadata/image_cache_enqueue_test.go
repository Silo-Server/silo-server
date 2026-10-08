package metadata

import (
	"context"
	"fmt"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

type recordingImageCacheJobEnqueuer struct {
	inputs []EnqueueImageCacheJobInput
}

func (r *recordingImageCacheJobEnqueuer) Enqueue(_ context.Context, in EnqueueImageCacheJobInput) error {
	_, err := r.EnqueueBatch(context.Background(), []EnqueueImageCacheJobInput{in})
	return err
}

func (r *recordingImageCacheJobEnqueuer) EnqueueBatch(_ context.Context, inputs []EnqueueImageCacheJobInput) (int, error) {
	r.inputs = append(r.inputs, inputs...)
	return len(inputs), nil
}

func TestPreserveCachedArtworkKeepsCachedPathWhenSourceMatches(t *testing.T) {
	path, thumb, source := preserveCachedArtwork(
		"tvdb://banners/episodes/1.jpg",
		"",
		"tvdb/series/1/seasons/1/episodes/1/still/original.webp",
		"tvdb://banners/episodes/1.jpg",
		"thumb",
	)
	if path != "tvdb/series/1/seasons/1/episodes/1/still/original.webp" {
		t.Fatalf("path = %q", path)
	}
	if thumb != "thumb" {
		t.Fatalf("thumb = %q", thumb)
	}
	if source != "tvdb://banners/episodes/1.jpg" {
		t.Fatalf("source = %q", source)
	}
}

func TestPersistSeasonsAndEpisodesPersistsSourceBeforeEnqueue(t *testing.T) {
	const seriesID = "series-tvdb-123"
	service, _, seasonRepo, episodeRepo := newSeasonEpisodeServiceForTest(seriesID)
	enqueuer := &recordingImageCacheJobEnqueuer{}
	service.SetAutoCacheImages(true)
	service.SetImageCacheJobEnqueuer(enqueuer)

	series := &models.MediaItem{
		ContentID: seriesID,
		Type:      "series",
		TvdbID:    "123",
	}
	service.persistSeasonsAndEpisodes(
		context.Background(),
		series,
		map[string]string{"tvdb": "123"},
		"en",
		"en",
		[]SeasonResult{{
			SeasonNumber: 1,
			Title:        "Season 1",
			PosterPath:   "tvdb://banners/seasons/1.jpg",
		}},
		[]EpisodeResult{{
			ProviderIDs:    map[string]string{"tvdb": "ep-1"},
			SeasonNumber:   1,
			EpisodeNumber:  1,
			Title:          "Pilot",
			StillPath:      "tvdb://banners/episodes/1.jpg",
			StillThumbhash: "provider-thumb",
		}},
		MergeFillEmpty,
	)

	season := seasonRepo.seasons[seasonKey(seriesID, 1)]
	if season == nil {
		t.Fatal("season was not persisted")
	}
	if season.PosterSourcePath != "tvdb://banners/seasons/1.jpg" {
		t.Fatalf("season source = %q", season.PosterSourcePath)
	}
	episode := episodeRepo.episodes[episodeKey(seriesID, 1, 1)]
	if episode == nil {
		t.Fatal("episode was not persisted")
	}
	if episode.StillSourcePath != "tvdb://banners/episodes/1.jpg" {
		t.Fatalf("episode source = %q", episode.StillSourcePath)
	}
	if len(enqueuer.inputs) != 2 {
		t.Fatalf("queued jobs = %d, want 2", len(enqueuer.inputs))
	}
	if enqueuer.inputs[0].TargetContentID != season.ContentID {
		t.Fatalf("season job target = %q, want %q", enqueuer.inputs[0].TargetContentID, season.ContentID)
	}
	if enqueuer.inputs[1].TargetContentID != episode.ContentID {
		t.Fatalf("episode job target = %q, want %q", enqueuer.inputs[1].TargetContentID, episode.ContentID)
	}
}

func TestPreserveCachedArtworkRecordsNewProviderSource(t *testing.T) {
	path, thumb, source := preserveCachedArtwork(
		"tvdb://banners/episodes/new.jpg",
		"",
		"tvdb/series/1/seasons/1/episodes/1/still/original.webp",
		"tvdb://banners/episodes/old.jpg",
		"old-thumb",
	)
	if path != "tvdb/series/1/seasons/1/episodes/1/still/original.webp" {
		t.Fatalf("cached path should remain visible until worker succeeds, got %q", path)
	}
	if thumb != "old-thumb" {
		t.Fatalf("thumb = %q", thumb)
	}
	if source != "tvdb://banners/episodes/new.jpg" {
		t.Fatalf("source = %q", source)
	}
}

func TestProviderImageSourcePathOnlyRecordsProviderScheme(t *testing.T) {
	if got := providerImageSourcePath("tvdb://banners/episodes/1.jpg"); got != "tvdb://banners/episodes/1.jpg" {
		t.Fatalf("provider source = %q", got)
	}
	if got := providerImageSourcePath("tmdb/series/1/poster/original.webp"); got != "" {
		t.Fatalf("cached path source = %q, want empty", got)
	}
	if got := providerImageSourcePath("file:///media/poster.jpg"); got != "" {
		t.Fatalf("file source = %q, want empty", got)
	}
	if got := providerImageSourcePath("https://image.tmdb.org/t/p/original/a.jpg"); got != "https://image.tmdb.org/t/p/original/a.jpg" {
		t.Fatalf("http source = %q", got)
	}
}

// An item rebuilt under the same content ID comes back with no cached copy
// while its earlier job for the same source still reads succeeded, so that
// job has to run again. Artwork the item already holds a cached copy of must
// not be fetched again.
func TestEnqueueItemImagesRequeuesArtworkWithoutACachedCopy(t *testing.T) {
	service := &MetadataService{}
	enqueuer := &recordingImageCacheJobEnqueuer{}
	service.SetAutoCacheImages(true)
	service.SetImageCacheJobEnqueuer(enqueuer)

	item := &models.MediaItem{
		ContentID:          "local-series-rebuilt",
		Type:               "series",
		PosterPath:         "",
		PosterSourcePath:   "file:///media/Other/Show/poster.jpg",
		BackdropPath:       "local/series/local-series-rebuilt/abc123/backdrop/original.webp",
		BackdropSourcePath: "file:///media/Other/Show/fanart.jpg",
		LogoPath:           "https://image.example/logo.png",
		LogoSourcePath:     "https://image.example/logo.png",
	}
	service.enqueueItemImages(context.Background(), item, nil, nil)

	requeue := map[string]bool{}
	for _, in := range enqueuer.inputs {
		requeue[in.ImageType] = in.RequeueSucceeded
	}
	want := map[string]bool{
		ImageCacheImagePoster:   true,
		ImageCacheImageBackdrop: false,
		ImageCacheImageLogo:     true,
	}
	if len(enqueuer.inputs) != len(want) {
		t.Fatalf("queued %d jobs (%v), want one per image type in %v", len(enqueuer.inputs), requeue, want)
	}
	for imageType, wantRequeue := range want {
		if got, ok := requeue[imageType]; !ok || got != wantRequeue {
			t.Errorf("%s RequeueSucceeded = %v (queued %v), want %v", imageType, got, ok, wantRequeue)
		}
	}
}

// Seasons and episodes of a provider-anchored series keep their content IDs
// when the series is rebuilt, so a recreated row with no cached copy has to
// requeue its earlier succeeded job. A row that keeps its cached copy must
// not.
func TestPersistSeasonsAndEpisodesRequeuesArtworkWithoutACachedCopy(t *testing.T) {
	const seriesID = "series-tvdb-123"
	service, _, seasonRepo, _ := newSeasonEpisodeServiceForTest(seriesID)
	enqueuer := &recordingImageCacheJobEnqueuer{}
	service.SetAutoCacheImages(true)
	service.SetImageCacheJobEnqueuer(enqueuer)
	seasonRepo.seasons[seasonKey(seriesID, 2)] = &models.Season{
		ContentID:        "season-tvdb-123-2",
		SeriesID:         seriesID,
		SeasonNumber:     2,
		Title:            "Season 2",
		PosterPath:       "local/series/series-tvdb-123/abc123/poster/original.webp",
		PosterSourcePath: "file:///media/Show/season02-poster.jpg",
	}

	service.persistSeasonsAndEpisodes(
		context.Background(),
		&models.MediaItem{ContentID: seriesID, Type: "series", TvdbID: "123"},
		map[string]string{"tvdb": "123"},
		"en",
		"en",
		[]SeasonResult{
			{SeasonNumber: 1, Title: "Season 1", PosterPath: "file:///media/Show/season01-poster.jpg"},
			{SeasonNumber: 2, Title: "Season 2", PosterPath: "file:///media/Show/season02-poster.jpg"},
		},
		[]EpisodeResult{{
			ProviderIDs:   map[string]string{"tvdb": "ep-1"},
			SeasonNumber:  1,
			EpisodeNumber: 1,
			Title:         "Pilot",
			StillPath:     "file:///media/Show/Season 1/Show S01E01-thumb.jpg",
		}},
		MergeFillEmpty,
	)

	requeue := map[string]bool{}
	for _, in := range enqueuer.inputs {
		key := fmt.Sprintf("%s s%d", in.TargetType, *in.SeasonNumber)
		if in.EpisodeNumber != nil {
			key += fmt.Sprintf("e%d", *in.EpisodeNumber)
		}
		requeue[key] = in.RequeueSucceeded
	}
	want := map[string]bool{
		ImageCacheTargetSeason + " s1":    true,
		ImageCacheTargetSeason + " s2":    false,
		ImageCacheTargetEpisode + " s1e1": true,
	}
	if len(enqueuer.inputs) != len(want) {
		t.Fatalf("queued %d jobs (%v), want %v", len(enqueuer.inputs), requeue, want)
	}
	for key, wantRequeue := range want {
		if got, ok := requeue[key]; !ok || got != wantRequeue {
			t.Errorf("%s RequeueSucceeded = %v (queued %v), want %v", key, got, ok, wantRequeue)
		}
	}
}

// Localization rows are deleted with their item and recreated by the next
// refresh, so localized artwork with no cached copy has to requeue its earlier
// succeeded job too, and cached localized artwork must not.
func TestEnqueueLocalizedArtworkRequeuesOnlyWithoutACachedCopy(t *testing.T) {
	t.Run("item", func(t *testing.T) {
		service := &MetadataService{}
		enqueuer := &recordingImageCacheJobEnqueuer{}
		service.SetAutoCacheImages(true)
		service.SetImageCacheJobEnqueuer(enqueuer)

		service.enqueueItemLocalizationImages(context.Background(),
			&models.MediaItem{ContentID: "movie-tmdb-1", Type: "movie"},
			&models.MediaItemLocalization{
				ContentID:          "movie-tmdb-1",
				Language:           "fr",
				PosterPath:         "https://image.example/fr-poster.jpg",
				PosterSourcePath:   "https://image.example/fr-poster.jpg",
				BackdropPath:       "movie/tmdb/1/fr/backdrop/original.webp",
				BackdropSourcePath: "https://image.example/fr-backdrop.jpg",
			},
			nil, nil,
		)

		requeue := map[string]bool{}
		for _, in := range enqueuer.inputs {
			requeue[in.ImageType] = in.RequeueSucceeded
		}
		want := map[string]bool{ImageCacheImagePoster: true, ImageCacheImageBackdrop: false}
		if len(enqueuer.inputs) != len(want) {
			t.Fatalf("queued %d jobs (%v), want %v", len(enqueuer.inputs), requeue, want)
		}
		for imageType, wantRequeue := range want {
			if got, ok := requeue[imageType]; !ok || got != wantRequeue {
				t.Errorf("%s RequeueSucceeded = %v (queued %v), want %v", imageType, got, ok, wantRequeue)
			}
		}
	})

	t.Run("season", func(t *testing.T) {
		const seriesID = "series-tvdb-456"
		service, _, seasonRepo, _ := newSeasonEpisodeServiceForTest(seriesID)
		seasonLocalizations := newFakeSeasonLocalizationRepo()
		service.seasonLocalizationRepo = seasonLocalizations
		enqueuer := &recordingImageCacheJobEnqueuer{}
		service.SetAutoCacheImages(true)
		service.SetImageCacheJobEnqueuer(enqueuer)
		seasonRepo.seasons[seasonKey(seriesID, 2)] = &models.Season{
			ContentID:    "season-tvdb-456-2",
			SeriesID:     seriesID,
			SeasonNumber: 2,
			Title:        "Season 2",
		}
		seasonLocalizations.localizations[seasonLocalizationKey("season-tvdb-456-2", "fr")] = &models.SeasonLocalization{
			SeasonContentID:  "season-tvdb-456-2",
			Language:         "fr",
			Title:            "Saison 2",
			PosterPath:       "series/tvdb/456/fr/season-2/poster/original.webp",
			PosterSourcePath: "https://image.example/fr-season-2.jpg",
		}

		service.persistSeasonsAndEpisodes(
			context.Background(),
			&models.MediaItem{ContentID: seriesID, Type: "series", TvdbID: "456"},
			map[string]string{"tvdb": "456"},
			"en",
			"fr",
			[]SeasonResult{
				{SeasonNumber: 1, Title: "Saison 1", PosterPath: "https://image.example/fr-season-1.jpg"},
				{SeasonNumber: 2, Title: "Saison 2", PosterPath: "https://image.example/fr-season-2.jpg"},
			},
			nil,
			MergeFillEmpty,
		)

		requeue := map[int]bool{}
		for _, in := range enqueuer.inputs {
			if in.TargetType == ImageCacheTargetSeasonLocalization {
				requeue[*in.SeasonNumber] = in.RequeueSucceeded
			}
		}
		want := map[int]bool{1: true, 2: false}
		if len(requeue) != len(want) {
			t.Fatalf("queued season localization jobs %v, want %v", requeue, want)
		}
		for season, wantRequeue := range want {
			if got := requeue[season]; got != wantRequeue {
				t.Errorf("season %d RequeueSucceeded = %v, want %v", season, got, wantRequeue)
			}
		}
	})
}
