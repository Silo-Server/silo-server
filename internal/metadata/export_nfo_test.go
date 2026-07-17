package metadata

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/Silo-Server/silo-server/internal/models"
)

// NFOSeriesHarness bridges the in-package fakes to the external NFO
// integration tests (package metadata_test). Those tests must live outside
// the package so they can import internal/metadata/nfo (which itself imports
// this package) without an import cycle.
type NFOSeriesHarness struct {
	h        *testHarness
	seasons  *fakeSeasonRepo
	episodes *fakeEpisodeRepo
	jobs     *capturingImageJobEnqueuer
}

type capturingImageJobEnqueuer struct {
	mu     sync.Mutex
	inputs []EnqueueImageCacheJobInput
}

func (c *capturingImageJobEnqueuer) Enqueue(_ context.Context, in EnqueueImageCacheJobInput) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inputs = append(c.inputs, in)
	return nil
}

func (c *capturingImageJobEnqueuer) EnqueueBatch(_ context.Context, inputs []EnqueueImageCacheJobInput) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inputs = append(c.inputs, inputs...)
	return len(inputs), nil
}

func NewNFOSeriesHarness() *NFOSeriesHarness {
	h := newTestHarness()
	x := &NFOSeriesHarness{
		h:        h,
		seasons:  newFakeSeasonRepo(),
		episodes: newFakeEpisodeRepo(),
		jobs:     &capturingImageJobEnqueuer{},
	}
	h.service.seasonRepo = x.seasons
	h.service.episodeRepo = x.episodes
	h.service.imageCacheJobs = x.jobs
	h.service.autoCacheImages.Store(true)
	return x
}

func (x *NFOSeriesHarness) Service() *MetadataService { return x.h.service }

// SeedSeriesSkeleton seeds a pending series item, mirroring the scanner's
// skeleton for an unmatched series root.
func (x *NFOSeriesHarness) SeedSeriesSkeleton(contentID, title string) error {
	return x.seedSkeleton(contentID, title, "series")
}

// SeedMovieSkeleton seeds a pending movie item, mirroring the scanner's
// skeleton for an unmatched movie group (e.g. the movie lane of a mixed
// library).
func (x *NFOSeriesHarness) SeedMovieSkeleton(contentID, title string) error {
	return x.seedSkeleton(contentID, title, "movie")
}

func (x *NFOSeriesHarness) seedSkeleton(contentID, title, contentType string) error {
	return x.h.itemRepo.Upsert(context.Background(), &models.MediaItem{
		ContentID: contentID,
		Type:      contentType,
		Title:     title,
		Status:    "pending_match",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	})
}

func (x *NFOSeriesHarness) Item(contentID string) (*models.MediaItem, error) {
	return x.h.itemRepo.GetByID(context.Background(), contentID)
}

// SeedSeriesFile registers a persisted media file linked to a content id, so
// refresh flows (which reconstruct local context from the file repo instead
// of scan hints) can find the series' episode files. observedRootPath is the
// series root eligible for directory-level sidecars.
func (x *NFOSeriesHarness) SeedSeriesFile(fileID, folderID int, filePath, observedRootPath, contentID string) {
	x.h.fileRepo.setGroupFiles(folderID, 1, fmt.Sprintf("group-%s-%d", contentID, fileID), &models.MediaFile{
		ID:               fileID,
		MediaFolderID:    folderID,
		FilePath:         filePath,
		ObservedRootPath: observedRootPath,
	})
	x.h.fileRepo.mu.Lock()
	x.h.fileRepo.contentIDs[fileID] = contentID
	x.h.fileRepo.mu.Unlock()
}

// Seasons returns the persisted seasons for a series sorted by season number.
func (x *NFOSeriesHarness) Seasons(seriesID string) []*models.Season {
	x.seasons.mu.Lock()
	defer x.seasons.mu.Unlock()
	var out []*models.Season
	for _, s := range x.seasons.seasons {
		if s.SeriesID == seriesID {
			cp := *s
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SeasonNumber < out[j].SeasonNumber })
	return out
}

// Episodes returns the persisted episodes for a series sorted by (season, episode).
func (x *NFOSeriesHarness) Episodes(seriesID string) []*models.Episode {
	x.episodes.mu.Lock()
	defer x.episodes.mu.Unlock()
	var out []*models.Episode
	for _, e := range x.episodes.episodes {
		if e.SeriesID == seriesID {
			cp := *e
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SeasonNumber != out[j].SeasonNumber {
			return out[i].SeasonNumber < out[j].SeasonNumber
		}
		return out[i].EpisodeNumber < out[j].EpisodeNumber
	})
	return out
}

// EnqueuedImageJobs returns every image cache job the pipeline enqueued.
func (x *NFOSeriesHarness) EnqueuedImageJobs() []EnqueueImageCacheJobInput {
	x.jobs.mu.Lock()
	defer x.jobs.mu.Unlock()
	out := make([]EnqueueImageCacheJobInput, len(x.jobs.inputs))
	copy(out, x.jobs.inputs)
	return out
}
