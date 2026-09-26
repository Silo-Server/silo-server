package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles/ai"
)

func (h *SubtitleAIHandler) authorizeSubtitleAIFile(ctx context.Context, filter catalog.AccessFilter, fileID int) error {
	if h == nil || h.service == nil || h.FileAuthorizer == nil {
		return apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Subtitle AI is not configured")
	}
	_, err := h.FileAuthorizer.AuthorizeContext(ctx, fileID, filter)
	if errors.Is(err, catalog.ErrItemNotFound) || errors.Is(err, catalog.ErrEpisodeNotFound) {
		return apiError(http.StatusNotFound, "not_found", "Media file not found")
	}
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to authorize media file")
	}
	return nil
}

func (h *SubtitleAIHandler) ListSubtitleAIJobs(ctx context.Context, filter catalog.AccessFilter, fileID int) ([]ai.Job, error) {
	if err := h.authorizeSubtitleAIFile(ctx, filter, fileID); err != nil {
		return nil, err
	}
	return h.service.ListJobs(ctx, fileID)
}

func (h *SubtitleAIHandler) GetSubtitleAIJob(ctx context.Context, filter catalog.AccessFilter, id int64) (*ai.Job, error) {
	if h == nil || h.service == nil {
		return nil, apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Subtitle AI is not configured")
	}
	job, err := h.service.GetJob(ctx, id)
	if errors.Is(err, ai.ErrJobNotFound) {
		return nil, apiError(http.StatusNotFound, "not_found", "Job not found")
	}
	if err != nil {
		return nil, err
	}
	if err := h.authorizeSubtitleAIFile(ctx, filter, job.MediaFileID); err != nil {
		return nil, err
	}
	return job, nil
}

func (h *SubtitleAIHandler) SubtitleAIQuota(ctx context.Context, userID int, profileID string, isAdmin bool) (ai.QuotaStatus, error) {
	if h == nil || h.service == nil {
		return ai.QuotaStatus{}, apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Subtitle AI is not configured")
	}
	return h.service.TranscribeQuota(ctx, userID, h.subtitleQuotaExempt(ctx, userID, profileID, isAdmin))
}

func (h *SubtitleAIHandler) subtitleQuotaExempt(ctx context.Context, userID int, profileID string, isAdmin bool) bool {
	if !isAdmin {
		return false
	}
	if profileID == "" || h.StoreProvider == nil {
		return true
	}
	store, err := h.StoreProvider.ForUser(ctx, userID)
	if err != nil {
		return false
	}
	profile, err := store.GetProfile(ctx, profileID)
	return err == nil && profile != nil && profile.IsPrimary
}
