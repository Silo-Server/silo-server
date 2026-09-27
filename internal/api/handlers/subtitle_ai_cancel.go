package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles/ai"
)

// CancelSubtitleAIJob preserves the bridge's media-file access rule. A caller
// who can access the job's file may cancel it; this is not a requester-only job.
// Success acknowledges the guarded state transition, not immediate provider stop.
func (h *SubtitleAIHandler) CancelSubtitleAIJob(ctx context.Context, filter catalog.AccessFilter, id int64) error {
	job, err := h.GetSubtitleAIJob(ctx, filter, id)
	if err != nil {
		return err
	}
	if err := h.service.Cancel(ctx, job.ID); err != nil {
		if errors.Is(err, ai.ErrJobNotFound) {
			return apiError(http.StatusNotFound, "not_found", "Job not found")
		}
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to cancel job")
	}
	return nil
}
