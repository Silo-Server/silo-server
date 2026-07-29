package jellycompat

import (
	"context"
	"log/slog"
	"time"
)

const compatTerminalRecoveryBatchSize = 100

// StartTerminalScrobbleRecovery resumes terminal provider events that survived
// a server exit after staging but before delivery. The first scan runs at
// startup; periodic scans also recover expired leases from crashed peers.
func StartTerminalScrobbleRecovery(
	ctx context.Context,
	store CompatPlaybackStore,
	scrobbler PlaybackWatchScrobbler,
	interval time.Duration,
) <-chan struct{} {
	initialScanDone := make(chan struct{})
	if store == nil || scrobbler == nil {
		close(initialScanDone)
		return initialScanDone
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	handler := &PlaybackHandler{playbackStore: store, WatchScrobbler: scrobbler}
	run := func(runCtx context.Context) {
		if err := recoverPendingTerminalScrobbles(runCtx, handler); err != nil {
			slog.WarnContext(runCtx, "recover jellycompat terminal scrobbles failed",
				"component", "jellycompat", "error", err)
		}
	}
	go func() {
		if err := recoverInitialPendingTerminalScrobbles(ctx, handler); err != nil {
			slog.WarnContext(ctx, "initial jellycompat terminal scrobble recovery incomplete",
				"component", "jellycompat", "error", err)
		}
		close(initialScanDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run(ctx)
			}
		}
	}()
	return initialScanDone
}

func recoverPendingTerminalScrobbles(ctx context.Context, handler *PlaybackHandler) error {
	_, _, err := recoverPendingTerminalScrobbleBatch(ctx, handler, nil)
	return err
}

func recoverInitialPendingTerminalScrobbles(ctx context.Context, handler *PlaybackHandler) error {
	seen := make(map[string]struct{})
	for {
		processed, batchSize, err := recoverPendingTerminalScrobbleBatch(ctx, handler, seen)
		if err != nil {
			return err
		}
		// Both stores advance a rotating cursor between calls. A page with no
		// unseen records means the cursor wrapped; a short page means it reached
		// the current tail.
		if processed == 0 || batchSize < compatTerminalRecoveryBatchSize {
			return nil
		}
	}
}

func recoverPendingTerminalScrobbleBatch(
	ctx context.Context,
	handler *PlaybackHandler,
	seen map[string]struct{},
) (int, int, error) {
	pending, err := handler.playbackStore.ListPendingTerminals(ctx, compatTerminalRecoveryBatchSize)
	if err != nil {
		return 0, 0, err
	}
	processed := 0
	for _, session := range pending {
		if err := ctx.Err(); err != nil {
			return processed, len(pending), err
		}
		if seen != nil {
			if _, ok := seen[session.ID]; ok {
				continue
			}
			seen[session.ID] = struct{}{}
		}
		processed++
		if !session.TerminalAuthoritative && session.TerminalScrobbleEvent != nil {
			deliverAfter := session.TerminalScrobbleEvent.OccurredAt.Add(handler.compatTerminalFallbackDelay())
			if delay := time.Until(deliverAfter); delay > 0 {
				handler.scheduleCompatTerminalDelivery(
					session.ID,
					session.CompatToken,
					false,
					session.ExpiresAt,
					delay,
					0,
				)
				continue
			}
		}
		handler.deliverCompatTerminal(
			ctx,
			session.ID,
			session.CompatToken,
			session.TerminalAuthoritative,
			session.ExpiresAt,
			0,
			false,
		)
	}
	return processed, len(pending), nil
}
