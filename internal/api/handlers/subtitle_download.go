package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/logredact"
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
		if errors.Is(err, subtitles.ErrInvalidSubtitleID) {
			slog.WarnContext(ctx, "subtitle download refused malformed result id", "component", "api", "provider", req.ProviderName)
			return nil, fieldError("subtitle_id", "subtitle_id is not a result ID this provider returns").WithCause(err)
		}
		// A client can resend a SubDL ID issued while result IDs still carried
		// a signed query string, and provider errors can quote a request URL or
		// an upstream body; keep credentials from either out of the log.
		loggedID, _, _ := strings.Cut(req.SubtitleID, "?")
		slog.ErrorContext(ctx, "subtitle download failed", "component", "api", "provider", req.ProviderName, "subtitle_id", loggedID,
			"error", logredact.SanitizeText(logredact.SanitizeURLError(err).Error()))
		return nil, apiError(http.StatusInternalServerError, "download_error", "Failed to download subtitle")
	}
	h.requestAutoSync(ctx, sub)
	return sub, nil
}
