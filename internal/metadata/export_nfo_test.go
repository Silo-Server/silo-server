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
	copier   *recordingLocalArtworkCopier
}

// recordingLocalArtworkCopier records the local artwork a refresh hands to
// the discovering process's copier.
type recordingLocalArtworkCopier struct {
	mu     sync.Mutex
	inputs []EnqueueImageCacheJobInput
}

func (c *recordingLocalArtworkCopier) CopyLocalArtwork(_ context.Context, inputs []EnqueueImageCacheJobInput) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inputs = append(c.inputs, inputs...)
	return nil
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

// RecordLocalArtworkCopies wires a copier that records the local artwork
// refreshes hand to it instead of queueing it.
func (x *NFOSeriesHarness) RecordLocalArtworkCopies() {
	x.copier = &recordingLocalArtworkCopier{}
	x.h.service.SetLocalArtworkCopier(x.copier)
}

// LocalArtworkCopies returns every local artwork input handed to the copier.
func (x *NFOSeriesHarness) LocalArtworkCopies() []EnqueueImageCacheJobInput {
	x.copier.mu.Lock()
	defer x.copier.mu.Unlock()
	return append([]EnqueueImageCacheJobInput(nil), x.copier.inputs...)
}

// LandLocalArtworkCopies stores a cached copy for every slot of the series
// whose source is local, the way the image-cache processor's source-matched
// update does once a copy succeeds.
func (x *NFOSeriesHarness) LandLocalArtworkCopies(seriesID string) error {
	ctx := context.Background()
	cached := func(source string) string {
		return fmt.Sprintf("local/series/%s/%08x/copy.webp", seriesID, len(source))
	}
	item, err := x.h.itemRepo.GetByID(ctx, seriesID)
	if err != nil {
		return err
	}
	for _, field := range itemArtworkFields(item) {
		if isLocalImageSourcePath(*field.source) {
			*field.path = cached(*field.source)
		}
	}
	if err := x.h.itemRepo.Upsert(ctx, item); err != nil {
		return err
	}
	x.seasons.mu.Lock()
	for _, season := range x.seasons.seasons {
		if season.SeriesID == seriesID && isLocalImageSourcePath(season.PosterSourcePath) {
			season.PosterPath = cached(season.PosterSourcePath)
		}
	}
	x.seasons.mu.Unlock()
	x.episodes.mu.Lock()
	for _, episode := range x.episodes.episodes {
		if episode.SeriesID == seriesID && isLocalImageSourcePath(episode.StillSourcePath) {
			episode.StillPath = cached(episode.StillSourcePath)
		}
	}
	x.episodes.mu.Unlock()
	return nil
}

// EnqueuedImageJobs returns every image cache job the pipeline enqueued.
func (x *NFOSeriesHarness) EnqueuedImageJobs() []EnqueueImageCacheJobInput {
	x.jobs.mu.Lock()
	defer x.jobs.mu.Unlock()
	out := make([]EnqueueImageCacheJobInput, len(x.jobs.inputs))
	copy(out, x.jobs.inputs)
	return out
}
