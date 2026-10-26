package metadata

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	// seriesEpisodeSyncTimeout bounds one series' link and debt pass, whether
	// it runs straight after a target refresh or in a batch flush. It matches
	// the per-target timeout of the scheduled refresh task.
	seriesEpisodeSyncTimeout = 2 * time.Minute
	// scheduledRefreshFlushWorkers caps how many series a flush syncs at once.
	// It matches the scheduled refresh task's worker count, which is how many
	// of these passes could run at once before they were batched.
	scheduledRefreshFlushWorkers = 12
)

// scheduledRefreshBatchKey carries the open *scheduledRefreshBatch on the
// contexts of the refreshes that belong to it.
type scheduledRefreshBatchKey struct{}

// failedEpisodeDebtKey carries the episode targets whose refresh failure was
// recorded during the batch into the flush's series debt sweep.
type failedEpisodeDebtKey struct{}

// scheduledRefreshBatch collects the series whose seasons or episodes were
// refreshed during one scheduled refresh batch. The series-wide link and debt
// passes run once per collected series when the batch is flushed, instead of
// once per refreshed season or episode.
type scheduledRefreshBatch struct {
	mu             sync.Mutex
	series         []string
	seen           map[string]struct{}
	failedEpisodes map[string]struct{}
	// targets holds the season and episode targets that refreshed successfully.
	// Their own debt rows stay claimed until the flush has run their series'
	// passes, so a batch that never flushes, or whose pass for a series fails,
	// leaves them for a later claim.
	targets []refreshDebtTarget
}

// refreshDebtTarget is a successful season or episode target whose debt row
// the flush syncs once its series' passes have finished.
type refreshDebtTarget struct {
	targetType string
	contentID  string
	seriesID   string
}

// add records a series for the flush, once however many of its seasons or
// episodes the batch refreshes.
func (b *scheduledRefreshBatch) add(seriesID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.seen[seriesID]; ok {
		return
	}
	b.seen[seriesID] = struct{}{}
	b.series = append(b.series, seriesID)
}

// markEpisodeFailed records an episode target whose refresh failure was
// written to its debt row during the batch.
func (b *scheduledRefreshBatch) markEpisodeFailed(episodeID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failedEpisodes[episodeID] = struct{}{}
}

// addTarget records a successful season or episode target whose debt row
// stays claimed until the flush has run its series' passes.
func (b *scheduledRefreshBatch) addTarget(targetType, contentID, seriesID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.targets = append(b.targets, refreshDebtTarget{targetType: targetType, contentID: contentID, seriesID: seriesID})
}

// take returns everything the batch has recorded and empties it, so a second
// flush has nothing left to sync.
func (b *scheduledRefreshBatch) take() ([]string, map[string]struct{}, []refreshDebtTarget) {
	b.mu.Lock()
	defer b.mu.Unlock()
	series, failed, targets := b.series, b.failedEpisodes, b.targets
	b.series = nil
	b.seen = make(map[string]struct{})
	b.failedEpisodes = make(map[string]struct{})
	b.targets = nil
	return series, failed, targets
}

// BeginScheduledRefreshBatch starts a scheduled refresh batch. Season and
// episode refreshes run with the returned context record their series instead
// of relinking and re-syncing the whole series each time; the returned flush
// runs those series-wide passes once per series and must be called after every
// refresh in the batch has returned. Whole-series refreshes keep their passes
// inline because their own debt sync reads the result.
//
// A season or episode target that succeeds inside the batch syncs its own debt
// row in the flush, after its series' passes, as it did when the passes ran
// inline. Until then the row stays claimed under its lease.
//
// An episode target whose failure was recorded during the batch keeps that
// failure while it still has debt: the flush's series sweep leaves its row
// alone, as the sweep that ran before the failure was recorded did when the
// passes were inline.
//
// A flush whose context is done skips the series it has not started and every
// deferred target debt sync. A target whose series pass failed or timed out is
// skipped too. Those targets' rows are still claimed, so the next claim after
// their lease expires refreshes them and runs the passes again.
func (s *MetadataService) BeginScheduledRefreshBatch(ctx context.Context) (context.Context, func(context.Context)) {
	batch := &scheduledRefreshBatch{seen: make(map[string]struct{}), failedEpisodes: make(map[string]struct{})}
	return context.WithValue(ctx, scheduledRefreshBatchKey{}, batch), func(flushCtx context.Context) {
		s.flushScheduledRefreshBatch(flushCtx, batch)
	}
}

// scheduledRefreshBatchFromContext returns the batch a refresh belongs to, or
// nil when it runs outside a scheduled refresh batch.
func scheduledRefreshBatchFromContext(ctx context.Context) *scheduledRefreshBatch {
	batch, _ := ctx.Value(scheduledRefreshBatchKey{}).(*scheduledRefreshBatch)
	return batch
}

// syncSeriesEpisodeStateOrDefer runs the series-wide link and debt passes now,
// or records the series for the enclosing scheduled refresh batch.
func (s *MetadataService) syncSeriesEpisodeStateOrDefer(ctx context.Context, seriesID string) {
	if batch := scheduledRefreshBatchFromContext(ctx); batch != nil {
		batch.add(seriesID)
		return
	}
	s.syncSeriesEpisodeState(ctx, seriesID)
}

// syncDeferredSeriesEpisodeState runs a batch flush's passes for one series and
// reports whether they finished. Unlike the inline passes, it skips the debt
// sweep when the link pass fails: the sweep would release the claims of the
// batch's targets in the series, and the retry those claims bring runs both
// passes again.
func (s *MetadataService) syncDeferredSeriesEpisodeState(ctx context.Context, seriesID string) bool {
	if err := s.ensureSeriesEpisodeLinks(ctx, seriesID); err != nil {
		slog.WarnContext(ctx, "metadata: failed to ensure series episode links for a scheduled refresh batch", "component", "metadata",
			"series_id", seriesID, "error", err)
		return false
	}
	swept := s.refreshSeriesEpisodeMetadataState(ctx, seriesID, time.Now())
	return swept && ctx.Err() == nil
}

// syncRefreshDebtForTargetOrDefer syncs a successful season or episode
// target's debt row now, or leaves it claimed for the enclosing scheduled
// refresh batch to sync after the flush has run its series' passes.
func (s *MetadataService) syncRefreshDebtForTargetOrDefer(ctx context.Context, targetType, contentID, seriesID string) error {
	if batch := scheduledRefreshBatchFromContext(ctx); batch != nil {
		batch.addTarget(targetType, contentID, seriesID)
		return nil
	}
	return s.syncRefreshDebtForTarget(ctx, targetType, contentID)
}

// noteScheduledRefreshFailure records an episode target whose refresh failure
// was written to its debt row, so the enclosing batch's series sweep keeps it.
func noteScheduledRefreshFailure(ctx context.Context, targetType, contentID string) {
	batch := scheduledRefreshBatchFromContext(ctx)
	contentID = strings.TrimSpace(contentID)
	if batch == nil || contentID == "" || NormalizeRefreshTargetType(targetType) != RefreshTargetEpisode {
		return
	}
	batch.markEpisodeFailed(contentID)
}

// failedEpisodeDebtFromContext returns the episode targets whose recorded
// failure a series debt sweep must leave in place.
func failedEpisodeDebtFromContext(ctx context.Context) map[string]struct{} {
	failed, _ := ctx.Value(failedEpisodeDebtKey{}).(map[string]struct{})
	return failed
}

// flushScheduledRefreshBatch runs the batch's deferred work: each recorded
// series' link and debt passes, at most scheduledRefreshFlushWorkers at a time
// and each under seriesEpisodeSyncTimeout, then the debt sync of every target
// whose series finished. BeginScheduledRefreshBatch describes what a canceled
// flush or a failed series pass leaves claimed.
func (s *MetadataService) flushScheduledRefreshBatch(ctx context.Context, batch *scheduledRefreshBatch) {
	series, failedEpisodes, targets := batch.take()
	if len(failedEpisodes) > 0 {
		ctx = context.WithValue(ctx, failedEpisodeDebtKey{}, failedEpisodes)
	}
	slots := make(chan struct{}, scheduledRefreshFlushWorkers)
	var wg sync.WaitGroup
	var syncedMu sync.Mutex
	synced := make(map[string]bool, len(series))
	for i, seriesID := range series {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if err := ctx.Err(); err != nil {
			slog.InfoContext(ctx, "metadata: skipped deferred series sync for a canceled refresh batch", "component", "metadata",
				"skipped_series", len(series)-i, "error", err)
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			seriesCtx, cancel := context.WithTimeout(ctx, seriesEpisodeSyncTimeout)
			defer cancel()
			ok := s.syncDeferredSeriesEpisodeState(seriesCtx, seriesID)
			syncedMu.Lock()
			synced[seriesID] = ok
			syncedMu.Unlock()
		}()
	}
	wg.Wait()

	for i, target := range targets {
		if err := ctx.Err(); err != nil {
			slog.InfoContext(ctx, "metadata: left deferred target debt claimed for a canceled refresh batch", "component", "metadata",
				"skipped_targets", len(targets)-i, "error", err)
			return
		}
		if !synced[target.seriesID] {
			slog.InfoContext(ctx, "metadata: left target debt claimed after its series sync failed", "component", "metadata",
				"target_type", target.targetType,
				"content_id", target.contentID,
				"series_id", target.seriesID)
			continue
		}
		targetCtx := withCountedRefreshAttempt(ctx, target.targetType, target.contentID)
		if err := s.syncRefreshDebtForTarget(targetCtx, target.targetType, target.contentID); err != nil {
			slog.WarnContext(ctx, "metadata: failed to sync refresh debt after a scheduled refresh batch", "component", "metadata",
				"target_type", target.targetType,
				"content_id", target.contentID,
				"error", err)
		}
	}
}
