package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata/translation"
)

func (h *MetadataAIHandler) TranslateAdminMetadata(ctx context.Context, contentID string, req TranslateMetadataRequest, userID int) (*translation.Job, error) {
	if req.TargetLanguage == "" {
		return nil, fieldError("target_language", "target_language is required")
	}
	if h == nil || h.service == nil {
		return nil, apiError(http.StatusServiceUnavailable, "not_configured", "Metadata AI translation is not configured on this server")
	}
	target, err := h.resolveTranslationTarget(ctx, contentID)
	if err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) {
			return nil, apiError(http.StatusNotFound, "not_found", "Item not found")
		}
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve item")
	}
	includeChildren := target.kind == translation.TargetItem
	if req.IncludeChildren != nil {
		includeChildren = *req.IncludeChildren && target.kind == translation.TargetItem
	}
	var requestedBy *int
	if userID != 0 {
		requestedBy = new(userID)
	}
	job, err := h.service.Enqueue(ctx, translation.JobRequest{TargetKind: target.kind, ContentID: contentID, TargetLanguage: req.TargetLanguage, IncludeChildren: includeChildren, Force: req.Force, RequestedBy: requestedBy})
	if err != nil {
		switch {
		case errors.Is(err, translation.ErrNotConfigured):
			return nil, apiError(http.StatusServiceUnavailable, "not_configured", "Metadata AI translation is not configured on this server")
		case errors.Is(err, translation.ErrInvalidRequest):
			return nil, apiError(http.StatusBadRequest, "bad_request", err.Error())
		default:
			slog.ErrorContext(ctx, "failed to enqueue metadata translation", "component", "api", "content_id", contentID, "error", err)
			return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to start translation")
		}
	}
	return job, nil
}
func (h *MetadataAIHandler) ListAdminMetadataTranslationJobs(ctx context.Context, contentID string) ([]translation.Job, error) {
	if h == nil || h.service == nil {
		return nil, apiError(http.StatusServiceUnavailable, "not_configured", "Metadata AI translation is not configured on this server")
	}
	jobs, err := h.service.ListJobs(ctx, contentID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "list_error", "Failed to list jobs")
	}
	return jobs, nil
}
func (h *MetadataAIHandler) CancelAdminMetadataTranslation(ctx context.Context, contentID string, jobID int64) error {
	if h == nil || h.service == nil {
		return apiError(http.StatusServiceUnavailable, "not_configured", "Metadata AI translation is not configured on this server")
	}
	job, err := h.service.GetJob(ctx, jobID)
	if err != nil {
		if errors.Is(err, translation.ErrJobNotFound) {
			return apiError(http.StatusNotFound, "not_found", "Job not found")
		}
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to load job")
	}
	// Permission authorizes the URL item, never an unrelated job identity.
	if job.ContentID != contentID {
		return apiError(http.StatusNotFound, "not_found", "Job not found")
	}
	if err := h.service.Cancel(ctx, jobID); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to cancel job")
	}
	return nil
}
