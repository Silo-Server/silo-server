package metadata

import (
	"context"
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
