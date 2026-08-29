package metadata

import (
	"context"
	"errors"
	"strings"
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
		remaining:     false,
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

// Running out of enqueueable work is not the same as being done. Another node
// holding the rest in flight, a node that died mid-batch, a node on an older
// revision that wrote only the old rungs, and a job parked after exhausting its
// retries all look identical here — nothing left to queue — and in every case
// the artwork still lacks the rung.
func TestRunLadderBackfillIncompleteWhileArtworkStillMissesTheRung(t *testing.T) {
	jobs := &ladderBackfillJobs{
		ladderResults: []int{0},
		remaining:     true,
	}
	processor := newLadderProcessor(jobs)

	_, complete, err := processor.RunLadderBackfill(context.Background(), "ladder-worker", 100, 2, time.Minute, nil)
	if err != nil {
		t.Fatalf("RunLadderBackfill() error = %v", err)
	}
	if complete {
		t.Fatal("reported complete while artwork is still missing the rung")
	}
	if jobs.remainingCalls != 1 {
		t.Fatalf("remainder checks = %d, want exactly 1", jobs.remainingCalls)
	}
}

// The completion question is asked of the artwork, not of the queue, so it must
// be asked even when this run enqueued and cached everything it saw.
func TestRunLadderBackfillAsksArtworkStateAfterWorking(t *testing.T) {
	jobs := &ladderBackfillJobs{
		ladderResults: []int{1, 0},
		remaining:     true,
		loopingImageCacheJobs: loopingImageCacheJobs{
			claimedResults: [][]*models.MetadataImageCacheJob{
				{ladderTestJob(1)},
				{},
			},
		},
	}
	processor := newLadderProcessor(jobs)

	_, complete, err := processor.RunLadderBackfill(context.Background(), "ladder-worker", 100, 2, time.Minute, nil)
	if err != nil {
		t.Fatalf("RunLadderBackfill() error = %v", err)
	}
	if complete {
		t.Fatal("reported complete despite artwork still missing the rung")
	}
	if jobs.remainingCalls != 1 {
		t.Fatalf("remainder checks = %d, want exactly 1", jobs.remainingCalls)
	}
}

func TestRunLadderBackfillIncompleteOnEnqueueError(t *testing.T) {
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

// A remainder check that cannot run is not evidence of completion.
func TestRunLadderBackfillIncompleteOnRemainderError(t *testing.T) {
	jobs := &ladderBackfillJobs{
		ladderResults: []int{0},
		remainingErr:  errors.New("database unavailable"),
	}
	processor := newLadderProcessor(jobs)

	_, complete, err := processor.RunLadderBackfill(context.Background(), "ladder-worker", 100, 2, time.Minute, nil)
	if err == nil {
		t.Fatal("RunLadderBackfill() error = nil, want the remainder error surfaced")
	}
	if complete {
		t.Fatal("an unanswerable remainder check must not report completion")
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

// The rung pattern has to match both cached key forms. Matching only one would
// leave half the catalog permanently "missing" and the sweep would never
// converge.
func TestLadderRungLiteralMatchesBothKeyForms(t *testing.T) {
	tests := []struct {
		imageType string
		want      string
	}{
		{ImageCacheImagePoster, "'%/w780.%'"},
		{ImageCacheImageStill, "'%/w780.%'"},
		{ImageCacheImageLogo, "'%/w1280.%'"},
	}
	for _, tt := range tests {
		if got := ladderRungLiteral(tt.imageType); got != tt.want {
			t.Errorf("ladderRungLiteral(%q) = %q, want %q", tt.imageType, got, tt.want)
		}
	}

	// The SQL is `key LIKE '%/w780.%'`; these are the two real key shapes.
	for _, key := range []string{
		"tmdb/movies/550/poster/w780.abc123.webp", // revisioned
		"tmdb/movies/550/poster/w780.webp",        // legacy, no revision
	} {
		if !sqlLikeMatchesRung(key, "w780") {
			t.Errorf("key %q does not match the w780 rung pattern", key)
		}
	}
	for _, key := range []string{
		"tmdb/movies/550/poster/w500.abc123.webp",
		"tmdb/movies/550/poster/original.abc123.webp",
		"tmdb/movies/550/w780poster/w500.webp",
	} {
		if sqlLikeMatchesRung(key, "w780") {
			t.Errorf("key %q unexpectedly matches the w780 rung pattern", key)
		}
	}
}

// sqlLikeMatchesRung mirrors Postgres `key LIKE '%/<rung>.%'` for the shapes the
// pattern has to discriminate.
func sqlLikeMatchesRung(key, rung string) bool {
	return strings.Contains(key, "/"+rung+".")
}
