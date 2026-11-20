package metadata

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// sweepCountingRefreshDebtRepo counts series-wide episode debt sweeps, which
// always start by snapshotting the series' episode debt.
type sweepCountingRefreshDebtRepo struct {
	*fakeRefreshDebtRepo
	sweepMu sync.Mutex
	sweeps  map[string]int
}

func newSweepCountingRefreshDebtRepo() *sweepCountingRefreshDebtRepo {
	return &sweepCountingRefreshDebtRepo{
		fakeRefreshDebtRepo: newFakeRefreshDebtRepo(),
		sweeps:              make(map[string]int),
	}
}

func (r *sweepCountingRefreshDebtRepo) SnapshotEpisodeDebts(ctx context.Context, seriesID string) (map[string]string, error) {
	r.sweepMu.Lock()
	r.sweeps[seriesID]++
	r.sweepMu.Unlock()
	return r.fakeRefreshDebtRepo.SnapshotEpisodeDebts(ctx, seriesID)
}

func (r *sweepCountingRefreshDebtRepo) sweepCount(seriesID string) int {
	r.sweepMu.Lock()
	defer r.sweepMu.Unlock()
	return r.sweeps[seriesID]
}

// seedSeriesSyncCounters wires a target-refresh harness whose provider covers
// S05E03 and S05E04 and counts the series-wide link passes and debt sweeps.
func seedSeriesSyncCounters(t *testing.T) (*testHarness, string, string, *atomic.Int32, *sweepCountingRefreshDebtRepo) {
	t.Helper()
	provider := &targetRefreshProvider{
		seasons: []SeasonResult{{SeasonNumber: 5, Title: "Provider Season 5"}},
		episodesBySeason: map[int][]EpisodeResult{
			5: {
				{SeasonNumber: 5, EpisodeNumber: 3, Title: "Provider Episode 3", Overview: "Episode 3 overview"},
				{SeasonNumber: 5, EpisodeNumber: 4, Title: "Provider Episode 4", Overview: "Episode 4 overview"},
			},
		},
	}
	h, seriesID, seasonID, _ := seedTargetRefreshHarness(provider)
	debts := newSweepCountingRefreshDebtRepo()
	h.service.refreshDebtRepo = debts
	linkPasses := &atomic.Int32{}
	h.service.hooks.ensureSeriesEpisodeLinks = func(context.Context, string) error {
		linkPasses.Add(1)
		return nil
	}
	return h, seriesID, seasonID, linkPasses, debts
}

func TestScheduledRefreshBatchFlushSkipsCanceledContext(t *testing.T) {
	h, seriesID, _, linkPasses, debts := seedSeriesSyncCounters(t)
	ctx, cancel := context.WithCancel(context.Background())

	batchCtx, flush := h.service.BeginScheduledRefreshBatch(ctx)
	if err := h.service.RefreshScheduledTarget(batchCtx, RefreshTargetEpisode, "episode-s05e03"); err != nil {
		t.Fatalf("RefreshScheduledTarget: %v", err)
	}
	cancel()
	flush(ctx)

	if got := linkPasses.Load(); got != 0 {
		t.Fatalf("series link passes after a canceled flush = %d, want 0", got)
	}
	if got := debts.sweepCount(seriesID); got != 0 {
		t.Fatalf("series debt sweeps after a canceled flush = %d, want 0", got)
	}
}

// failingStaleIDRepo fails every stale provider ID lookup after the first,
// which lets a two-language refresh write its first language and then fail.
type failingStaleIDRepo struct {
	*fakeStaleIDRepo
	calls atomic.Int32
}

func (r *failingStaleIDRepo) GetByContentID(ctx context.Context, contentID string) ([]*models.StaleMediaID, error) {
	if r.calls.Add(1) > 1 {
		return nil, errors.New("stale id lookup failed")
	}
	return r.fakeStaleIDRepo.GetByContentID(ctx, contentID)
}

func TestScheduledRefreshBatchSyncsSeriesWhenALaterLanguageFails(t *testing.T) {
	h, seriesID, _, linkPasses, debts := seedSeriesSyncCounters(t)
	ctx := context.Background()
	h.libraryRepo.setMetadataLanguages(seriesID, []string{"en", "fr"}, nil)
	h.service.staleIDRepo = &failingStaleIDRepo{fakeStaleIDRepo: newFakeStaleIDRepo()}

	batchCtx, flush := h.service.BeginScheduledRefreshBatch(ctx)
	if err := h.service.RefreshScheduledTarget(batchCtx, RefreshTargetEpisode, "episode-s05e04"); err == nil {
		t.Fatal("RefreshScheduledTarget succeeded, want the second language's lookup error")
	}
	updated, err := h.service.episodeRepo.GetBySeriesAndNumber(ctx, seriesID, 5, 4)
	if err != nil {
		t.Fatalf("GetBySeriesAndNumber S05E04: %v", err)
	}
	if updated.Overview != "Episode 4 overview" {
		t.Fatalf("S05E04 overview = %q, want the first language's rows written", updated.Overview)
	}

	flush(ctx)

	if got := linkPasses.Load(); got != 1 {
		t.Fatalf("series link passes after the flush = %d, want 1 for the rows the first language wrote", got)
	}
	assertEpisodeFailureDebtKept(t, debts.fakeRefreshDebtRepo, "episode-s05e04")
}

// assertEpisodeFailureDebtKept checks that a batch flush left the failure an
// episode target recorded during the batch in place.
func assertEpisodeFailureDebtKept(t *testing.T, debts *fakeRefreshDebtRepo, episodeID string) {
	t.Helper()
	debt, err := debts.GetTarget(context.Background(), RefreshTargetEpisode, episodeID)
	if err != nil {
		t.Fatalf("debt for failed target %s after the flush: %v", episodeID, err)
	}
	if !hasRefreshDebtReason(debt.ReasonMask, RefreshDebtReasonRefreshFailure) || debt.LastError == "" {
		t.Fatalf("debt for failed target %s after the flush = reason %d, last_error %q; want the recorded failure kept",
			episodeID, debt.ReasonMask, debt.LastError)
	}
}

func TestScheduledRefreshBatchKeepsFailureDebtOfAFailedSibling(t *testing.T) {
	provider := &targetRefreshProvider{
		episodesBySeason: map[int][]EpisodeResult{
			5: {{SeasonNumber: 5, EpisodeNumber: 3, Title: "Provider Episode 3", Overview: "Episode 3 overview"}},
		},
	}
	h, seriesID, _, _ := seedTargetRefreshHarness(provider)
	debts := newSweepCountingRefreshDebtRepo()
	h.service.refreshDebtRepo = debts
	ctx := context.Background()

	batchCtx, flush := h.service.BeginScheduledRefreshBatch(ctx)
	if err := h.service.RefreshScheduledTarget(batchCtx, RefreshTargetEpisode, "episode-s05e03"); err != nil {
		t.Fatalf("RefreshScheduledTarget S05E03: %v", err)
	}
	if err := h.service.RefreshScheduledTarget(batchCtx, RefreshTargetEpisode, "episode-s05e04"); err == nil {
		t.Fatal("RefreshScheduledTarget S05E04 succeeded, want no provider metadata")
	}
	assertEpisodeFailureDebtKept(t, debts.fakeRefreshDebtRepo, "episode-s05e04")

	flush(ctx)

	// The sibling's success queued a sweep of the whole series. That sweep must
	// not turn the failed episode's row back into a plain success.
	if got := debts.sweepCount(seriesID); got != 1 {
		t.Fatalf("series debt sweeps after the flush = %d, want 1", got)
	}
	assertEpisodeFailureDebtKept(t, debts.fakeRefreshDebtRepo, "episode-s05e04")
}

// targetDebtSyncCountingRepo counts the writes that settle one target's debt
// row, which a successful target's own debt sync or a series sweep makes.
type targetDebtSyncCountingRepo struct {
	*sweepCountingRefreshDebtRepo
	contentID string
	settled   atomic.Int32
}

func (r *targetDebtSyncCountingRepo) MarkTargetSuccess(ctx context.Context, targetType, contentID string, priority int, reasonMask int64, nextRefreshAt time.Time) error {
	if contentID == r.contentID {
		r.settled.Add(1)
	}
	return r.sweepCountingRefreshDebtRepo.MarkTargetSuccess(ctx, targetType, contentID, priority, reasonMask, nextRefreshAt)
}

func (r *targetDebtSyncCountingRepo) DeleteTargetDebt(ctx context.Context, targetType, contentID string) error {
	if contentID == r.contentID {
		r.settled.Add(1)
	}
	return r.sweepCountingRefreshDebtRepo.DeleteTargetDebt(ctx, targetType, contentID)
}

func TestScheduledRefreshBatchSyncsTargetDebtAfterTheFlush(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			h, _, _, linkPasses, _ := seedSeriesSyncCounters(t)
			debts := &targetDebtSyncCountingRepo{
				sweepCountingRefreshDebtRepo: newSweepCountingRefreshDebtRepo(),
				contentID:                    "episode-s05e03",
			}
			h.service.refreshDebtRepo = debts
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			batchCtx, flush := h.service.BeginScheduledRefreshBatch(ctx)
			if err := h.service.RefreshScheduledTarget(batchCtx, RefreshTargetEpisode, "episode-s05e03"); err != nil {
				t.Fatalf("RefreshScheduledTarget: %v", err)
			}
			// The target's own debt row must stay claimed until its series has
			// been relinked, so a batch that never flushes leaves retryable work.
			if got := debts.settled.Load(); got != 0 {
				t.Fatalf("target debt writes before the flush = %d, want 0", got)
			}
			if canceled {
				cancel()
			}
			flush(ctx)

			if canceled {
				if got := debts.settled.Load(); got != 0 {
					t.Fatalf("target debt writes after a canceled flush = %d, want 0 so the lease brings it back", got)
				}
				return
			}
			if got := linkPasses.Load(); got != 1 {
				t.Fatalf("series link passes after the flush = %d, want 1", got)
			}
			if got := debts.settled.Load(); got == 0 {
				t.Fatal("target debt was never synced after the flush")
			}
		})
	}
}

func TestSeriesDebtSweepClearsAFailedEpisodeThatIsNowComplete(t *testing.T) {
	h := newTestHarness()
	h.service.episodeRepo = newFakeEpisodeRepo()
	debts := newFakeRefreshDebtRepo()
	h.service.refreshDebtRepo = debts
	ctx := context.Background()
	seriesID := "series-failed-then-complete"
	_ = h.service.episodeRepo.Upsert(ctx, &models.Episode{
		ContentID:      "episode-complete",
		SeriesID:       seriesID,
		SeasonNumber:   1,
		EpisodeNumber:  1,
		Title:          "Pilot",
		TmdbID:         "101",
		MetadataSource: "provider",
	})
	_ = h.service.episodeRepo.Upsert(ctx, &models.Episode{
		ContentID:      "episode-incomplete",
		SeriesID:       seriesID,
		SeasonNumber:   1,
		EpisodeNumber:  2,
		MetadataSource: "provider",
	})
	for _, episodeID := range []string{"episode-complete", "episode-incomplete"} {
		if err := h.service.syncRefreshDebtTargetFailure(ctx, RefreshTargetEpisode, episodeID, errors.New("provider failed"), true); err != nil {
			t.Fatalf("syncRefreshDebtTargetFailure(%s): %v", episodeID, err)
		}
	}

	sweepCtx := context.WithValue(ctx, failedEpisodeDebtKey{}, map[string]struct{}{
		"episode-complete":   {},
		"episode-incomplete": {},
	})
	h.service.refreshSeriesEpisodeMetadataState(sweepCtx, seriesID, time.Now().UTC())

	// A later target in the batch completed this episode, so its failure row
	// has nothing left to retry.
	if _, err := debts.GetTarget(ctx, RefreshTargetEpisode, "episode-complete"); !errors.Is(err, ErrRefreshDebtNotFound) {
		t.Fatalf("debt for the completed failed episode: err = %v, want it cleared", err)
	}
	assertEpisodeFailureDebtKept(t, debts, "episode-incomplete")
}

// cancelingStaleIDRepo cancels the refresh's context on the second stale
// provider ID lookup, so a two-language refresh writes its first language and
// then fails on its own deadline.
type cancelingStaleIDRepo struct {
	*fakeStaleIDRepo
	cancel context.CancelFunc
	calls  atomic.Int32
}

func (r *cancelingStaleIDRepo) GetByContentID(ctx context.Context, contentID string) ([]*models.StaleMediaID, error) {
	if r.calls.Add(1) > 1 {
		r.cancel()
		return nil, context.Canceled
	}
	return r.fakeStaleIDRepo.GetByContentID(ctx, contentID)
}

func TestTargetRefreshSyncsSeriesOnALiveContextAfterItsOwnCancellation(t *testing.T) {
	h, seriesID, _, _, _ := seedSeriesSyncCounters(t)
	h.libraryRepo.setMetadataLanguages(seriesID, []string{"en", "fr"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.service.staleIDRepo = &cancelingStaleIDRepo{fakeStaleIDRepo: newFakeStaleIDRepo(), cancel: cancel}
	var linkCtxErrs []error
	h.service.hooks.ensureSeriesEpisodeLinks = func(linkCtx context.Context, _ string) error {
		linkCtxErrs = append(linkCtxErrs, linkCtx.Err())
		return nil
	}

	if err := h.service.RefreshScheduledTarget(ctx, RefreshTargetEpisode, "episode-s05e04"); err == nil {
		t.Fatal("RefreshScheduledTarget succeeded, want the canceled second language")
	}

	if len(linkCtxErrs) != 1 || linkCtxErrs[0] != nil {
		t.Fatalf("series link passes = %v, want one on a live context for the rows the first language wrote", linkCtxErrs)
	}
}

func TestScheduledRefreshBatchLeavesTargetDebtClaimedWhenItsSeriesSyncFails(t *testing.T) {
	h, _, _, _, _ := seedSeriesSyncCounters(t)
	debts := &targetDebtSyncCountingRepo{
		sweepCountingRefreshDebtRepo: newSweepCountingRefreshDebtRepo(),
		contentID:                    "episode-s05e03",
	}
	h.service.refreshDebtRepo = debts
	h.service.hooks.ensureSeriesEpisodeLinks = func(context.Context, string) error {
		return errors.New("link pass failed")
	}
	ctx := context.Background()

	batchCtx, flush := h.service.BeginScheduledRefreshBatch(ctx)
	if err := h.service.RefreshScheduledTarget(batchCtx, RefreshTargetEpisode, "episode-s05e03"); err != nil {
		t.Fatalf("RefreshScheduledTarget: %v", err)
	}
	flush(ctx)

	// The files the refresh could have linked are still unlinked, so the row
	// must stay claimed for the next claim after its lease to retry.
	if got := debts.settled.Load(); got != 0 {
		t.Fatalf("target debt writes after a failed series sync = %d, want 0", got)
	}
}
