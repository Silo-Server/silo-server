package handlers

import (
	"context"
	"net/http"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// GetViewerSubtitleForDeletion retains file access AND account ownership. A
// primary household profile is not itself an administrator.
func (h *SubtitleSearchHandler) GetViewerSubtitleForDeletion(ctx context.Context, access catalog.AccessFilter, id int) (*subtitles.DownloadedSubtitle, error) {
	if h == nil || h.repo == nil || h.manager == nil {
		return nil, apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Subtitle deletion is unavailable")
	}
	row, err := h.repo.GetDownloadedSubtitle(ctx, id)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Unable to read stored subtitle")
	}
	if row == nil {
		return nil, subtitles.ErrSubtitleNotFound
	}
	if err := h.authorizeSubtitleRead(ctx, access, row.MediaFileID); err != nil {
		return nil, err
	}
	claims := apimw.GetClaims(ctx)
	if claims == nil || claims.UserID != access.UserID || (claims.Role != roleAdmin && (row.DownloadedBy == nil || *row.DownloadedBy != claims.UserID)) {
		return nil, apiError(http.StatusForbidden, "forbidden", "Not authorized to delete this subtitle")
	}
	return row, nil
}

func (h *SubtitleSearchHandler) DeleteViewerSubtitle(ctx context.Context, access catalog.AccessFilter, id int, revision int64) error {
	row, err := h.GetViewerSubtitleForDeletion(ctx, access, id)
	if err != nil {
		return err
	}
	if row.Revision != revision {
		return &subtitles.SubtitleRevisionConflict{Current: row}
	}
	// Even wildcard admission retains the authorized row's revision. Never
	// widen a viewer's permission to a concurrently replaced representation.
	return h.manager.DeleteSubtitleWithRevision(ctx, id, &revision)
}
