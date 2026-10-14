package handlers

import (
	"context"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func (h *PlaybackHandler) captureNodeRecipeDeletionV3(ctx context.Context, work, nodeURL string) func() {
	if h == nil || h.NodeRecipeStore == nil || work == "" {
		return func() {}
	}
	captureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeRecipeWriteTimeoutV3)
	deleteRecipe, err := playback.CaptureNodeRecipeDeletion(captureCtx, h.NodeRecipeStore, work, nodeURL)
	cancel()
	if err != nil {
		slog.WarnContext(ctx, "capture node recipe cleanup failed", "component", "api", "transport", work, "error", err)
		return func() {}
	}
	return func() {
		deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeRecipeWriteTimeoutV3)
		defer cancel()
		if err := deleteRecipe(deleteCtx); err != nil {
			slog.WarnContext(ctx, "failed to drop the node transcode recipe", "component", "api", "transport", work, "error", err)
		}
	}
}

// capturePredecessorRecipeDeletionV3 captures a replaced transport's recipe
// publication. A local predecessor published no node recipe, so local commits
// skip the store round trip while they hold the session lifecycle lock.
func (h *PlaybackHandler) capturePredecessorRecipeDeletionV3(ctx context.Context, work, nodeURL string) func() {
	if nodeURL == "" {
		return func() {}
	}
	return h.captureNodeRecipeDeletionV3(ctx, work, nodeURL)
}
