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
