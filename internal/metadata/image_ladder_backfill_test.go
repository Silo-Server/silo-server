package metadata

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ladderBackfillJobs adds the optional ladder sweep to the looping fake.
type ladderBackfillJobs struct {
	loopingImageCacheJobs
	ladderResults []int
	ladderCalls   int
	ladderLimits  []int
	ladderCutoffs []time.Time
	ladderErr     error
}

func (f *ladderBackfillJobs) EnqueueLadderBackfill(_ context.Context, limit int, completedBefore time.Time) (int, error) {
	f.ladderLimits = append(f.ladderLimits, limit)
	f.ladderCutoffs = append(f.ladderCutoffs, completedBefore)
	if f.ladderErr != nil {
		f.ladderCalls++
		return 0, f.ladderErr
	}
	result := 0
	if f.ladderCalls < len(f.ladderResults) {
		result = f.ladderResults[f.ladderCalls]
	}
	f.ladderCalls++
	return result, nil
}

func ladderTestJob(id int64) *models.MetadataImageCacheJob {
	return &models.MetadataImageCacheJob{
		ID:                id,
		TargetType:        ImageCacheTargetEpisode,
		TargetContentID:   "episode-tvdb-1-1-1",
		SourcePath:        "tvdb://banners/episode-1.jpg",
		ProviderID:        "tvdb",
		ProviderContentID: "1",
		ContentType:       "series",
		ImageType:         ImageCacheImageStill,
		SeasonNumber:      intPointer(1),
		EpisodeNumber:     intPointer(1),
	}
}

func newLadderProcessor(jobs ImageCacheJobClaimer) *ImageCacheProcessor {
	cacher := &fakeImageCacher{result: &CacheImageResult{
		BasePath: "tvdb/series/1/seasons/1/episodes/1/still",
		Ext:      ".webp",
	}}
	resolver := &fakeImageResolver{url: "https://artworks.thetvdb.com/banners/episode.jpg"}
	return NewImageCacheProcessor(jobs, cacher, resolver, nil, &fakeEpisodeStillUpdater{updated: true})
}

func TestRunLadderBackfillDrainsEachBatchAndCompletes(t *testing.T) {
	jobs := &ladderBackfillJobs{
		ladderResults: []int{2, 0},
		loopingImageCacheJobs: loopingImageCacheJobs{
			claimedResults: [][]*models.MetadataImageCacheJob{
				{ladderTestJob(1), ladderTestJob(2)},
				{},
			},
			backlog: ImageCacheBacklog{Known: true, Queued: 2},
		},
	}
	processor := newLadderProcessor(jobs)

	stats, complete, err := processor.RunLadderBackfill(context.Background(), "ladder-worker", 100, 2, time.Minute, nil)
	if err != nil {
		t.Fatalf("RunLadderBackfill() error = %v", err)
	}
	if !complete {
		t.Fatal("RunLadderBackfill reported incomplete, want complete when the sweep finds nothing left")
	}
	if stats.EnqueuedExisting != 2 || stats.Succeeded != 2 {
		t.Fatalf("stats = %+v, want two enqueued and two cached", stats)
	}
	if jobs.ladderCalls != 2 {
		t.Fatalf("ladder sweep calls = %d, want a second call to confirm the queue is exhausted", jobs.ladderCalls)
	}
	// The full-catalog discovery sweep must not run: this pass regenerates
	// already-cached artwork and nothing else.
	if jobs.enqueueCalls != 0 {
		t.Fatalf("discovery calls = %d, want none", jobs.enqueueCalls)
	}
	for _, limit := range jobs.ladderLimits {
		if limit != imageCacheLadderBackfillBatchSize {
			t.Fatalf("batch limit = %d, want %d", limit, imageCacheLadderBackfillBatchSize)
		}
	}
}

// The cutoff that keeps the sweep terminating is captured once, before the first
// batch — not refreshed per batch, which would re-admit work the pass just did.
func TestRunLadderBackfillUsesOneStableCutoff(t *testing.T) {
	jobs := &ladderBackfillJobs{
		ladderResults: []int{1, 1, 0},
		loopingImageCacheJobs: loopingImageCacheJobs{
			claimedResults: [][]*models.MetadataImageCacheJob{
				{ladderTestJob(1)},
				{},
				{ladderTestJob(2)},
				{},
			},
		},
	}
	processor := newLadderProcessor(jobs)

	if _, _, err := processor.RunLadderBackfill(context.Background(), "ladder-worker", 100, 2, time.Minute, nil); err != nil {
		t.Fatalf("RunLadderBackfill() error = %v", err)
	}
	if len(jobs.ladderCutoffs) < 2 {
		t.Fatalf("ladder sweeps = %d, want at least 2", len(jobs.ladderCutoffs))
	}
	for _, cutoff := range jobs.ladderCutoffs[1:] {
		if !cutoff.Equal(jobs.ladderCutoffs[0]) {
			t.Fatalf("cutoff moved from %v to %v between batches", jobs.ladderCutoffs[0], cutoff)
		}
	}
}

func TestRunLadderBackfillIncompleteOnError(t *testing.T) {
	jobs := &ladderBackfillJobs{ladderErr: errors.New("database unavailable")}
	processor := newLadderProcessor(jobs)

	_, complete, err := processor.RunLadderBackfill(context.Background(), "ladder-worker", 100, 2, time.Minute, nil)
	if err == nil {
		t.Fatal("RunLadderBackfill() error = nil, want the sweep error surfaced")
	}
	if complete {
		t.Fatal("a failed pass must not report completion")
	}
}

// Caching being switched off is not completion: recording the version would skip
// the backfill permanently.
func TestRunLadderBackfillIncompleteWhenCachingDisabled(t *testing.T) {
	jobs := &ladderBackfillJobs{ladderResults: []int{5}}
	processor := newLadderProcessor(jobs)
	processor.SetEnabled(false)

	_, complete, err := processor.RunLadderBackfill(context.Background(), "ladder-worker", 100, 2, time.Minute, nil)
	if err != nil {
		t.Fatalf("RunLadderBackfill() error = %v", err)
	}
	if complete {
		t.Fatal("a disabled processor must not report completion")
	}
	if jobs.ladderCalls != 0 {
		t.Fatalf("ladder sweep calls = %d, want none while caching is disabled", jobs.ladderCalls)
	}
}

// A store with no ladder sweep just has no backfill; it must not be recorded as
// finished.
func TestRunLadderBackfillNoopWithoutSweepSupport(t *testing.T) {
	processor := newLadderProcessor(&loopingImageCacheJobs{})

	stats, complete, err := processor.RunLadderBackfill(context.Background(), "ladder-worker", 100, 2, time.Minute, nil)
	if err != nil {
		t.Fatalf("RunLadderBackfill() error = %v", err)
	}
	if complete {
		t.Fatal("a store without ladder support must not report completion")
	}
	if stats.EnqueuedExisting != 0 {
		t.Fatalf("stats = %+v, want nothing enqueued", stats)
	}
}
