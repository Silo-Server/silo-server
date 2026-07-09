package jellycompat

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

type playbackSessionExpirer interface {
	DeleteExpired(ctx context.Context) (int64, error)
}

func cleanupExpiredCompatState(ctx context.Context, repo *SessionRepository, playbackExpirer playbackSessionExpirer, now time.Time) (int, int64, error) {
	var (
		authDeleted     int
		playbackDeleted int64
		authErr         error
		playbackErr     error
	)
	if repo != nil {
		authDeleted, authErr = repo.DeleteExpired(ctx, now)
	}
	if playbackExpirer != nil {
		playbackDeleted, playbackErr = playbackExpirer.DeleteExpired(ctx)
	}
	return authDeleted, playbackDeleted, errors.Join(authErr, playbackErr)
}

// StartSessionCleanup runs a background goroutine that periodically removes
// expired compat sessions from the database. It stops when ctx is cancelled.
func StartSessionCleanup(ctx context.Context, repo *SessionRepository, interval time.Duration) {
	StartSessionCleanupWithPlaybackStore(ctx, repo, nil, interval)
}

// StartSessionCleanupWithPlaybackStore also sweeps expired durable playback
// negotiation rows when the configured playback store supports it.
func StartSessionCleanupWithPlaybackStore(ctx context.Context, repo *SessionRepository, playbackStore CompatPlaybackStore, interval time.Duration) {
	if repo == nil {
		if _, ok := playbackStore.(playbackSessionExpirer); !ok {
			return
		}
	}
	playbackExpirer, _ := playbackStore.(playbackSessionExpirer)
	if repo == nil && playbackExpirer == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				authDeleted, playbackDeleted, err := cleanupExpiredCompatState(ctx, repo, playbackExpirer, time.Now())
				if err != nil {
					slog.WarnContext(ctx, "jellycompat session cleanup failed", "component", "jellycompat", "error", err)
					continue
				}
				if authDeleted > 0 || playbackDeleted > 0 {
					slog.DebugContext(ctx, "jellycompat session cleanup", "component", "jellycompat", "auth_sessions", authDeleted, "playback_sessions", playbackDeleted)
				}
			}
		}
	}()
}
