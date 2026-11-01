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

// A slot keeps serving its cached copy while a new source waits for its cache
// job. A file:// source never lands in the served path. With caching off and
// the artwork unlocked (keepStored false), a provider path replaces a stored
// copy; with nothing offered the slot keeps it.
func TestPreserveCachedArtwork(t *testing.T) {
	const stored = "tvdb/series/1/seasons/1/episodes/1/still/original.webp"
	for _, tc := range []struct {
		name                                               string
		path, thumb, storedPath, storedSource, storedThumb string
		keepStored                                         bool
		wantPath, wantThumb, wantSource                    string
	}{
		{
			name: "cached path kept when the source matches",
			path: "tvdb://banners/episodes/1.jpg", storedPath: stored, storedSource: "tvdb://banners/episodes/1.jpg", storedThumb: "thumb", keepStored: true,
			wantPath: stored, wantThumb: "thumb", wantSource: "tvdb://banners/episodes/1.jpg",
		},
		{
			name: "new provider source recorded, cached path visible until the worker succeeds",
			path: "tvdb://banners/episodes/new.jpg", storedPath: stored, storedSource: "tvdb://banners/episodes/old.jpg", storedThumb: "old-thumb", keepStored: true,
			wantPath: stored, wantThumb: "old-thumb", wantSource: "tvdb://banners/episodes/new.jpg",
		},
		{
			name: "provider path replaces a stored copy not kept",
			path: "tvdb://banners/episodes/2.jpg", thumb: "new", storedPath: stored, storedSource: "tvdb://banners/episodes/1.jpg", storedThumb: "old",
			wantPath: "tvdb://banners/episodes/2.jpg", wantThumb: "new", wantSource: "tvdb://banners/episodes/2.jpg",
		},
		{
			name:       "stored copy not kept stays when nothing is offered",
			storedPath: stored, storedSource: "file:///m/thumb.jpg", storedThumb: "old",
			wantPath: stored, wantThumb: "old", wantSource: "file:///m/thumb.jpg",
		},
		{
			// The pre-cache window keeps the path empty until the cache job completes.
			name: "local source with no prior cached art",
			path: "file:///media/movies/Film/poster.jpg", keepStored: true,
			wantSource: "file:///media/movies/Film/poster.jpg",
		},
		{
			name: "prior cached art keeps serving while a local source waits",
			path: "file:///media/movies/Film/poster.jpg", storedPath: "tmdb/movies/550/poster/original.webp",
			storedSource: "https://image.tmdb.org/a.jpg", storedThumb: "th", keepStored: true,
			wantPath: "tmdb/movies/550/poster/original.webp", wantThumb: "th", wantSource: "file:///media/movies/Film/poster.jpg",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, thumb, source := preserveCachedArtwork(tc.path, tc.thumb, tc.storedPath, tc.storedSource, tc.storedThumb, tc.keepStored)
			if path != tc.wantPath || thumb != tc.wantThumb || source != tc.wantSource {
				t.Fatalf("got %q/%q/%q, want %q/%q/%q", path, thumb, source, tc.wantPath, tc.wantThumb, tc.wantSource)
			}
		})
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

// After an identify the item can be a different title, so its cached artwork
// of another source must not keep standing in for the new title's artwork.
// Ordinary refreshes, re-applying the same match and locked artwork keep the
// cached copy.
func TestPrepareItemImagesForQueueDropsPreviousTitleArtworkOnIdentify(t *testing.T) {
	const (
		oldCached = "tmdb/movies/438631/poster/original.old.webp"
		oldSource = "tmdb://poster/old.jpg"
		newSource = "tmdb://poster/new.jpg"
	)
	existing := func() *models.MediaItem {
		return &models.MediaItem{PosterPath: oldCached, PosterSourcePath: oldSource, PosterThumbhash: "old-thumb"}
	}
	cases := []struct {
		name       string
		chosenPath string
		identify   bool
		wantPath   string
		wantSource string
	}{
		{"identify to another title", newSource, true, newSource, newSource},
		{"refresh with a new provider choice", newSource, false, oldCached, newSource},
		{"identify to the same title", oldSource, true, oldCached, oldSource},
		{"identify with locked artwork", oldCached, true, oldCached, oldSource},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := &models.MediaItem{PosterPath: tc.chosenPath}
			prepareItemImagesForQueue(item, existing(), true, tc.identify)
			if item.PosterPath != tc.wantPath || item.PosterSourcePath != tc.wantSource {
				t.Fatalf("poster path, source = %q, %q; want %q, %q", item.PosterPath, item.PosterSourcePath, tc.wantPath, tc.wantSource)
			}
		})
	}
}

// An identify in a library whose language is not the item's canonical one
// writes only the localization row, so that row must not keep serving the
// previous title's cached artwork either.
func TestBuildItemLocalizationRecordDropsPreviousTitleArtworkOnIdentify(t *testing.T) {
	existing := func() *models.MediaItemLocalization {
		return &models.MediaItemLocalization{
			ContentID:        "movie-1",
			Language:         "de",
			PosterPath:       "tmdb/movies/438631/poster/original.old.webp",
			PosterSourcePath: "tmdb://poster/old.jpg",
			PosterThumbhash:  "old-title-hash",
		}
	}
	images := []RemoteImage{{ProviderID: "tmdb", URL: "tmdb://poster/new.jpg", Type: ImagePoster, Language: "de", Rating: 9}}

	loc := buildItemLocalizationRecord(existing(), "movie-1", "de", "movie", &MetadataResult{}, &MetadataResult{}, images, MergeReplaceUnlocked, "de", "", false, true, true)
	if loc.PosterPath != "tmdb://poster/new.jpg" || loc.PosterSourcePath != "tmdb://poster/new.jpg" {
		t.Fatalf("identify localization poster = %q (source %q), want the new provider image", loc.PosterPath, loc.PosterSourcePath)
	}
	if loc.PosterThumbhash != "" {
		t.Fatalf("identify localization thumbhash = %q, want the previous title's cleared", loc.PosterThumbhash)
	}
	loc = buildItemLocalizationRecord(existing(), "movie-1", "de", "movie", &MetadataResult{}, &MetadataResult{}, images, MergeReplaceUnlocked, "de", "", false, true, false)
	if loc.PosterPath != "tmdb/movies/438631/poster/original.old.webp" {
		t.Fatalf("refresh localization poster = %q, want the cached copy kept until the new one lands", loc.PosterPath)
	}
}
