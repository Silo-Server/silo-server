package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// DownloadStoredSubtitle authorizes the file and parent before contacting a provider.
// Attribution comes from the authenticated access filter, never the request body.
func (h *SubtitleSearchHandler) DownloadStoredSubtitle(ctx context.Context, access catalog.AccessFilter, req subtitles.DownloadRequest) (*subtitles.DownloadedSubtitle, error) {
	if err := h.authorizeSubtitleRead(ctx, access, req.MediaFileID); err != nil {
		return nil, err
	}
	if h.manager == nil {
		return nil, apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Subtitle download is not configured")
	}
	req.UserID = new(access.UserID)
	return h.downloadAuthorizedSubtitle(ctx, req)
}

func (h *SubtitleSearchHandler) downloadAuthorizedSubtitle(ctx context.Context, req subtitles.DownloadRequest) (*subtitles.DownloadedSubtitle, error) {
	sub, err := h.manager.Download(ctx, req)
	if err != nil {
		if errors.Is(err, subtitles.ErrUnknownProvider) {
			// Keep the cause so callers (and the v1 bridge) can still classify it.
			return nil, &APIError{Status: http.StatusNotFound, Code: "provider_not_found", Message: "Subtitle provider not found", cause: err}
		}
		slog.ErrorContext(ctx, "subtitle download failed", "component", "api", "provider", req.ProviderName, "subtitle_id", req.SubtitleID, "error", err)
		return nil, apiError(http.StatusInternalServerError, "download_error", "Failed to download subtitle")
	}
	return sub, nil
}
