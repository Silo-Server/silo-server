package handlers

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type MarkerEditAuditView = markerEditAuditResponse

// AdminMarkerHistory retains the three legacy selections: one authorized file,
// all versions of an item, or the server's recent edits when target is empty.
func (h *MarkersHandler) AdminMarkerHistory(ctx context.Context, filter catalog.AccessFilter, target MarkerTarget, limit int) ([]MarkerEditAuditView, error) {
	if h == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Marker history is not configured")
	}
	var fileIDs []int
	if target.FileID > 0 {
		file, err := h.resolveMarkerTarget(ctx, filter, target)
		if err != nil {
			return nil, err
		}
		fileIDs = []int{file.ID}
	} else if target.ItemID != "" {
		if h.Files == nil {
			return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Marker history is not configured")
		}
		files, err := h.Files.GetByEpisodeID(ctx, target.ItemID)
		if err != nil {
			return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load item files")
		}
		if len(files) == 0 {
			files, err = h.Files.GetByContentID(ctx, target.ItemID)
			if err != nil {
				return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load item files")
			}
		}
		for _, file := range files {
			if file != nil {
				fileIDs = append(fileIDs, file.ID)
			}
		}
		if len(fileIDs) == 0 {
			return nil, apiError(http.StatusNotFound, "not_found", "Media file not found for item")
		}
	}
	if h.AuditHistory == nil {
		return []MarkerEditAuditView{}, nil
	}
	var rows []scanner.MarkerEditAuditRow
	var err error
	if len(fileIDs) == 0 {
		rows, err = h.AuditHistory.ListAllMarkerEditAudit(ctx, limit)
	} else {
		rows, err = h.AuditHistory.ListMarkerEditAudit(ctx, fileIDs, limit)
	}
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load marker history")
	}
	return markerEditAuditResponses(rows), nil
}
