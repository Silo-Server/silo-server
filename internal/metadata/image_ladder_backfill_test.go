package metadata

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ladderBackfillJobs adds the optional ladder sweep to the looping fake.
//
// enqueueResults is what each enqueue call returns; remaining answers the
// artwork-state question the completion check asks, which is deliberately
// independent of the enqueue.
type ladderBackfillJobs struct {
	loopingImageCacheJobs
	ladderResults  []int
	ladderCalls    int
	ladderLimits   []int
	ladderErr      error
	remaining      bool
	remainingCalls int
	remainingErr   error
}

func (f *ladderBackfillJobs) EnqueueLadderBackfill(_ context.Context, limit int) (int, error) {
	f.ladderLimits = append(f.ladderLimits, limit)
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

func (f *ladderBackfillJobs) HasLadderBackfillRemaining(context.Context) (bool, error) {
	f.remainingCalls++
	if f.remainingErr != nil {
		return false, f.remainingErr
	}
	return f.remaining, nil
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
		remaining:     false,
		loopingImageCacheJobs: loopingImageCacheJobs{
			claimedResults: [][]*models.MetadataImageCacheJob{
				{testEpisodeStillJob(1, 1, "tvdb://banners/episode-1.jpg", true), testEpisodeStillJob(2, 1, "tvdb://banners/episode-1.jpg", true)},
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
		t.Fatal("reported incomplete, want complete when no artwork is missing the rung")
	}
	if stats.EnqueuedExisting != 2 || stats.Succeeded != 2 {
		t.Fatalf("stats = %+v, want two enqueued and two cached", stats)
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

// A pass that cannot prove every artwork has the rung is not complete.
func TestRunLadderBackfillIncomplete(t *testing.T) {
	for _, tc := range []struct {
		name     string
		jobs     *ladderBackfillJobs
		disabled bool
		wantErr  bool
		check    func(t *testing.T, jobs *ladderBackfillJobs)
	}{
		{
			// Running out of enqueueable work is not the same as being done.
			// Another node holding the rest in flight, a node that died
			// mid-batch, a node on an older revision that wrote only the old
			// rungs, and a job parked after exhausting its retries all look
			// identical here — nothing left to queue — and in every case the
			// artwork still lacks the rung.
			name: "artwork still misses the rung",
			jobs: &ladderBackfillJobs{ladderResults: []int{0}, remaining: true},
			check: func(t *testing.T, jobs *ladderBackfillJobs) {
				if jobs.remainingCalls != 1 {
					t.Fatalf("remainder checks = %d, want exactly 1", jobs.remainingCalls)
				}
			},
		},
		{name: "enqueue error", jobs: &ladderBackfillJobs{ladderErr: errors.New("database unavailable")}, wantErr: true},
		{
			// A remainder check that cannot run is not evidence of completion.
			name:    "remainder error",
			jobs:    &ladderBackfillJobs{ladderResults: []int{0}, remainingErr: errors.New("database unavailable")},
			wantErr: true,
		},
		{
			// Caching being switched off is not completion: recording the
			// version would skip the backfill permanently.
			name: "caching disabled", jobs: &ladderBackfillJobs{ladderResults: []int{5}}, disabled: true,
			check: func(t *testing.T, jobs *ladderBackfillJobs) {
				if jobs.ladderCalls != 0 {
					t.Fatalf("ladder sweep calls = %d, want none while caching is disabled", jobs.ladderCalls)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			processor := newLadderProcessor(tc.jobs)
			if tc.disabled {
				processor.SetEnabled(false)
			}
			_, complete, err := processor.RunLadderBackfill(context.Background(), "ladder-worker", 100, 2, time.Minute, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("RunLadderBackfill() error = %v, want error %v", err, tc.wantErr)
			}
			if complete {
				t.Fatal("reported complete, want incomplete")
			}
			if tc.check != nil {
				tc.check(t, tc.jobs)
			}
		})
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
