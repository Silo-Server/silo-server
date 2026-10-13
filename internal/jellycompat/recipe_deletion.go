package jellycompat

import (
	"context"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func (h *PlaybackHandler) captureNodeRecipeDeletion(ctx context.Context, work, nodeURL string) func() {
	if h == nil || h.RecipeNodeStore == nil || work == "" {
		return func() {}
	}
	captureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	deleteRecipe, err := playback.CaptureNodeRecipeDeletion(captureCtx, h.RecipeNodeStore, work, nodeURL)
	cancel()
	if err != nil {
		slog.WarnContext(ctx, "capture node recipe cleanup failed", "component", "jellycompat", "playback_session_id", work, "error", err)
		return func() {}
	}
	return func() {
		deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if err := deleteRecipe(deleteCtx); err != nil {
			slog.WarnContext(ctx, "delete node transcode recipe failed", "component", "jellycompat", "playback_session_id", work, "error", err)
		}
	}
}
