package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type FileMarkersView = fileMarkersResponse
type MarkerSegmentView = segmentMarker
type MarkerSegmentInput = segmentInput

// MarkerTarget names either one file or an item's first accessible file.
type MarkerTarget struct {
	FileID int
	ItemID string
}

// MarkerChanges contains only supplied segments. A nil segment clears it.
type MarkerChanges map[string]*MarkerSegmentInput

func (h *MarkersHandler) GetMarkers(ctx context.Context, access catalog.AccessFilter, target MarkerTarget) (FileMarkersView, error) {
	file, err := h.resolveMarkerTarget(ctx, access, target)
	if err != nil {
		return FileMarkersView{}, err
	}
	return fileMarkers(populateFileMarkers(ctx, h.MarkerPopulation, file)), nil
}

func (h *MarkersHandler) SetMarkers(ctx context.Context, access catalog.AccessFilter, target MarkerTarget, changes MarkerChanges) (FileMarkersView, error) {
	file, err := h.resolveMarkerTarget(ctx, access, target)
	if err != nil {
		return FileMarkersView{}, err
	}
	return h.applyManualMarkers(ctx, file, changes)
}

func (h *MarkersHandler) ClearMarker(ctx context.Context, access catalog.AccessFilter, fileID int, segment string) (FileMarkersView, error) {
	return h.SetMarkers(ctx, access, MarkerTarget{FileID: fileID}, MarkerChanges{segment: nil})
}

func (h *MarkersHandler) resolveMarkerTarget(ctx context.Context, access catalog.AccessFilter, target MarkerTarget) (*models.MediaFile, error) {
	if h == nil || h.Files == nil || h.Authorizer == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Marker access is not configured")
	}
	if target.FileID > 0 && target.ItemID == "" {
		file, err := h.Authorizer.AuthorizeContext(ctx, target.FileID, access)
		return file, markerServiceError(err)
	}
	if target.FileID != 0 || strings.TrimSpace(target.ItemID) == "" {
		return nil, apiError(http.StatusBadRequest, "bad_request", "A file or item identifier is required")
	}
	files, err := h.Files.GetByEpisodeID(ctx, target.ItemID)
	if err != nil {
		return nil, markerServiceError(err)
	}
	if len(files) == 0 {
		files, err = h.Files.GetByContentID(ctx, target.ItemID)
		if err != nil {
			return nil, markerServiceError(err)
		}
	}
	for _, file := range files {
		if file == nil {
			continue
		}
		authorized, err := h.Authorizer.AuthorizeContext(ctx, file.ID, access)
		if err == nil {
			return authorized, nil
		}
		if errors.Is(err, catalog.ErrItemNotFound) || errors.Is(err, catalog.ErrEpisodeNotFound) {
			continue
		}
		return nil, markerServiceError(err)
	}
	return nil, markerServiceError(catalog.ErrItemNotFound)
}

func markerServiceError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, catalog.ErrItemNotFound) || errors.Is(err, catalog.ErrEpisodeNotFound) || errors.Is(err, scanner.ErrFileNotFound) {
		return apiError(http.StatusNotFound, "not_found", "Media file not found")
	}
	return apiError(http.StatusInternalServerError, "internal_error", "Marker operation failed")
}

// applyManualMarkers is shared by the legacy adapter and the typed service.
// Validate every segment before the one atomic writer call.
func (h *MarkersHandler) applyManualMarkers(ctx context.Context, file *models.MediaFile, changes MarkerChanges) (FileMarkersView, error) {
	if h.Writer == nil {
		return FileMarkersView{}, apiError(http.StatusServiceUnavailable, "unavailable", "Marker writing is not configured")
	}
	for segment := range changes {
		if !isMarkerSegment(segment) {
			return FileMarkersView{}, apiError(http.StatusBadRequest, "bad_request", "Unknown marker segment")
		}
	}
	update := scanner.MarkerUpdate{MarkersSource: models.MarkerSourceManual, MarkersConfidence: &manualMarkerConfidence, MarkersAlgorithm: manualMarkerAlgorithm}
	var clears, sets []string
	for _, segment := range markerSegmentNames {
		value, present := changes[segment]
		if !present {
			continue
		}
		if value == nil {
			clears = append(clears, segment)
			continue
		}
		start, end, err := normalizeManualSegment(segment, *value, float64(file.Duration))
		if err != nil {
			return FileMarkersView{}, apiError(http.StatusBadRequest, "bad_request", err.Error())
		}
		applyManualSegment(&update, segment, start, end)
		sets = append(sets, segment)
	}
	if _, err := h.Writer.UpsertAndClearMarkers(ctx, file.ID, update, clears); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return FileMarkersView{}, err
		}
		h.logger.ErrorContext(ctx, "markers: save failed", "file_id", file.ID, "error", err)
		return FileMarkersView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to save markers")
	}
	refreshed, err := h.reloadAndNotify(ctx, file.ID)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return FileMarkersView{}, err
		}
		h.logger.ErrorContext(ctx, "markers: reload after save failed", "file_id", file.ID, "error", err)
		return FileMarkersView{}, apiError(http.StatusInternalServerError, "internal_error", "Markers saved but failed to reload")
	}
	h.maybeContribute(refreshed, sets)
	return fileMarkers(refreshed), nil
}
