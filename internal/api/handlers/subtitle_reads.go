package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

func (h *SubtitleSearchHandler) authorizeSubtitleRead(ctx context.Context, access catalog.AccessFilter, fileID int) error {
	if h == nil || h.repo == nil || h.FileAuthorizer == nil {
		return apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Subtitle storage is not configured")
	}
	if _, err := h.FileAuthorizer.AuthorizeContext(ctx, fileID, access); err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) || errors.Is(err, catalog.ErrEpisodeNotFound) {
			return apiError(http.StatusNotFound, "not_found", "Media file not found")
		}
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to authorize media file")
	}
	return nil
}

// ListStoredSubtitles applies file and parent access before reading stored tracks.
func (h *SubtitleSearchHandler) ListStoredSubtitles(ctx context.Context, access catalog.AccessFilter, fileID int) ([]subtitles.DownloadedSubtitle, error) {
	if err := h.authorizeSubtitleRead(ctx, access, fileID); err != nil {
		return nil, err
	}
	return h.repo.ListDownloadedSubtitles(ctx, fileID)
}

// SearchSubtitles uses the same metadata and scoring as the bridge handler.
func (h *SubtitleSearchHandler) SearchSubtitles(ctx context.Context, access catalog.AccessFilter, fileID int, languages []string) (*subtitles.SearchResponse, error) {
	if err := h.authorizeSubtitleRead(ctx, access, fileID); err != nil {
		return nil, err
	}
	if h.manager == nil || h.mediaResolver == nil {
		return nil, apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Subtitle search is not configured")
	}
	result, err := h.searchAuthorizedSubtitles(ctx, fileID, languages, false)
	if err != nil {
		return nil, err
	}
	return result, nil
}
