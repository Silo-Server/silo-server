package historyimport

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const (
	historyImportHeartbeatInterval     = 15 * time.Second
	historyImportStaleRunThreshold     = 1 * time.Minute
	historyImportStaleRunSweepInterval = 30 * time.Second
	staleRunInterruptedMessage         = "history import interrupted by deploy or app restart"
)

func (s *Service) startStaleRunMonitor() {
	go func() {
		if err := s.reconcileStaleRuns(s.bgContext, time.Now()); err != nil {
			slog.Warn("history import: failed to reconcile stale runs", "error", err)
		}

		ticker := time.NewTicker(historyImportStaleRunSweepInterval)
		defer ticker.Stop()

		for {
			select {
			case <-s.bgContext.Done():
				return
			case now := <-ticker.C:
				if err := s.reconcileStaleRuns(s.bgContext, now); err != nil {
					slog.Warn("history import: failed to reconcile stale runs", "error", err)
				}
			}
		}
	}()
}

func (s *Service) reconcileStaleRuns(ctx context.Context, now time.Time) error {
	return sweepStaleRunsOnce(ctx, now, historyImportStaleRunThreshold, func(ctx context.Context, staleBefore time.Time, message string) (int64, error) {
		count, err := s.repo.FailStaleRuns(ctx, staleBefore, message)
		if err == nil && count > 0 {
			slog.WarnContext(ctx, "history import: reconciled stale running runs", "component", "historyimport", "count", count, "stale_before", staleBefore)
		}
		return count, err
	})
}

func (s *Service) startRunHeartbeat(ctx context.Context, runID string) context.CancelFunc {
	hbCtx, cancel := context.WithCancel(ctx)
	go heartbeatLoop(hbCtx, historyImportHeartbeatInterval, func(ctx context.Context) error {
		err := s.repo.TouchRunHeartbeat(ctx, runID)
		if err != nil && !errors.Is(err, ErrRunNotFound) {
			slog.WarnContext(ctx, "history import: failed to persist heartbeat", "component", "historyimport", "run_id", runID, "error", err)
		}
		return nil
	})
	return cancel
}

func heartbeatLoop(ctx context.Context, interval time.Duration, touch func(context.Context) error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = touch(ctx)
		}
	}
}

func sweepStaleRunsOnce(ctx context.Context, now time.Time, threshold time.Duration, failStale func(context.Context, time.Time, string) (int64, error)) error {
	_, err := failStale(ctx, now.Add(-threshold), staleRunInterruptedMessage)
	return err
}
