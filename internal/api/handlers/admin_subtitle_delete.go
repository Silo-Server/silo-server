package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

func (h *AdminSubtitleHandler) DeleteAdminSubtitle(ctx context.Context, id int, revision *int64) error {
	if h == nil || h.manager == nil {
		return subtitles.ErrSubtitleGuardedDeletionUnavailable
	}
	return h.manager.DeleteSubtitleWithRevision(ctx, id, revision)
}
