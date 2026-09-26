package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// UploadStoredSubtitle applies the same file/parent access as provider download.
func (h *SubtitleSearchHandler) UploadStoredSubtitle(ctx context.Context, access catalog.AccessFilter, req subtitles.UploadRequest) (*subtitles.DownloadedSubtitle, error) {
	if err := h.authorizeSubtitleRead(ctx, access, req.MediaFileID); err != nil {
		return nil, err
	}
	if h.manager == nil {
		return nil, apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Subtitle upload is not configured")
	}
	req.UserID = new(access.UserID)
	return h.uploadAuthorizedSubtitle(ctx, req)
}

func (h *SubtitleSearchHandler) uploadAuthorizedSubtitle(ctx context.Context, req subtitles.UploadRequest) (*subtitles.DownloadedSubtitle, error) {
	sub, err := h.manager.Upload(ctx, req)
	if err == nil {
		return sub, nil
	}
	switch {
	case strings.Contains(err.Error(), "unsupported subtitle format"), strings.Contains(err.Error(), "missing file extension"), strings.Contains(err.Error(), "empty subtitle file"), strings.Contains(err.Error(), "could not detect subtitle language"), strings.Contains(err.Error(), "invalid subtitle language"):
		return nil, apiError(http.StatusBadRequest, "bad_request", err.Error())
	case strings.Contains(err.Error(), "exceeds maximum size"):
		return nil, apiError(http.StatusRequestEntityTooLarge, "too_large", "Subtitle file must be under 5 MB")
	default:
		slog.ErrorContext(ctx, "subtitle upload failed", "component", "api", "media_file_id", req.MediaFileID, "error", err)
		return nil, apiError(http.StatusInternalServerError, "upload_error", "Failed to upload subtitle")
	}
}
