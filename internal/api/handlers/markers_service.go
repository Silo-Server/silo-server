package handlers

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/librarykind"
	"github.com/Silo-Server/silo-server/internal/markers"
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
	return h.fileMarkersView(ctx, populateFileMarkers(ctx, h.MarkerPopulation, file)), nil
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
	if err := h.ensureMarkerEditable(ctx, file); err != nil {
		return FileMarkersView{}, err
	}
	if h.Writer == nil {
		return FileMarkersView{}, apiError(http.StatusServiceUnavailable, "unavailable", "Marker writing is not configured")
	}
	for segment := range changes {
		if !isMarkerSegment(segment) {
			return FileMarkersView{}, apiError(http.StatusBadRequest, "bad_request", "Unknown marker segment")
		}
	}
	update := scanner.MarkerUpdate{MarkersSource: models.MarkerSourceManual, MarkersConfidence: &manualMarkerConfidence, MarkersAlgorithm: markers.ManualAlgorithm}
	var clears, sets []string
	// Intro comes before credits in markerSegmentNames, so by the credits
	// check this is the intro the change leaves the file with.
	introEnd := file.IntroEnd
	for _, segment := range markerSegmentNames {
		value, present := changes[segment]
		if !present {
			continue
		}
		if value == nil {
			if segment == models.MarkerSegmentIntro {
				introEnd = nil
			}
			clears = append(clears, segment)
			continue
		}
		start, end, err := normalizeManualSegment(segment, *value, float64(file.Duration))
		if err != nil {
			return FileMarkersView{}, apiError(http.StatusBadRequest, "bad_request", err.Error())
		}
		switch segment {
		case models.MarkerSegmentIntro:
			introEnd = &end
		case models.MarkerSegmentCredits:
			if err := markers.CreditsLimitsForFile(file).CheckCreditsPlacement(start, end, float64(file.Duration), introEnd); err != nil {
				return FileMarkersView{}, apiError(http.StatusBadRequest, "bad_request", errSegment(segment, err.Error()).Error())
			}
		}
		applyManualSegment(&update, segment, start, end)
		sets = append(sets, segment)
	}
	if len(sets) > 0 || len(clears) > 0 {
		h.withholdKeptProviderMarkers(ctx, file, &update, clears)
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
	// A clear goes to providers that accept absent submissions.
	h.maybeContribute(refreshed, slices.Concat(sets, clears))
	return h.fileMarkersView(ctx, refreshed), nil
}

// markerStorageReader reports whether a player is shown only a file's stored
// markers (satisfied by *markers.PopulationService).
type markerStorageReader interface {
	ShowsStoredMarkers(ctx context.Context) (bool, error)
}

var _ markerStorageReader = (*markers.PopulationService)(nil)

// withholdKeptProviderMarkers stores markers.ManualWithheldAlgorithm on each
// kind update sets that keeps a provider's marker (markers.KeepsProviderMarker),
// and on each kind in clears that repeats a provider's answer that the file
// has none of it (markers.KeepsProviderAbsence), so that no contribution ever
// sends it. A withheld clear is written as a manual tombstone through update.
// It compares the save with the file's stored markers, which are what a
// player is shown unless online lookups run in on_demand mode. There a player
// is shown provider answers that each replica caches for itself and drops, so
// no lookup at save time can tell which one the user saw, and every kind is
// withheld, except a kind the file already holds as a shared manual marker
// (markers.HoldsSharedManualMarker).
func (h *MarkersHandler) withholdKeptProviderMarkers(ctx context.Context, file *models.MediaFile, update *scanner.MarkerUpdate, clears []string) {
	known := true
	if reader, ok := h.MarkerPopulation.(markerStorageReader); ok {
		stored, err := reader.ShowsStoredMarkers(ctx)
		if known = stored && err == nil; !known {
			h.logger.InfoContext(ctx, "markers: manual save withheld from contribution; the provider markers a player was shown are not stored", "file_id", file.ID, "error", err)
		}
	}
	for _, seg := range []struct {
		kind       markers.MarkerKind
		name       string
		start, end *float64
		provenance **scanner.SegmentProvenance
	}{
		{markers.MarkerKindIntro, models.MarkerSegmentIntro, update.IntroStart, update.IntroEnd, &update.IntroProvenance},
		{markers.MarkerKindCredits, models.MarkerSegmentCredits, update.CreditsStart, update.CreditsEnd, &update.CreditsProvenance},
		{markers.MarkerKindRecap, models.MarkerSegmentRecap, update.RecapStart, update.RecapEnd, &update.RecapProvenance},
		{markers.MarkerKindPreview, models.MarkerSegmentPreview, update.PreviewStart, update.PreviewEnd, &update.PreviewProvenance},
	} {
		cleared := slices.Contains(clears, seg.name)
		var keeps bool
		switch {
		case cleared:
			keeps = markers.KeepsProviderAbsence(file, seg.kind)
		case seg.start != nil && seg.end != nil:
			keeps = markers.KeepsProviderMarker(file, seg.kind, *seg.start, *seg.end)
		default:
			continue
		}
		if keeps || (!known && !markers.HoldsSharedManualMarker(file, seg.kind)) {
			*seg.provenance = &scanner.SegmentProvenance{Source: models.MarkerSourceManual, Confidence: &manualMarkerConfidence,
				Algorithm: markers.ManualWithheldAlgorithm, Absent: cleared}
		}
	}
}

// v2-only projection; the legacy writer retains its frozen response shape.
func (h *MarkersHandler) fileMarkersView(ctx context.Context, file *models.MediaFile) FileMarkersView {
	view := fileMarkers(file)
	if h.ThumbnailQueuer != nil {
		h.ThumbnailQueuer.QueueFileIDs(ctx, []int{file.ID})
	}
	if h.MarkerImageURLs != nil {
		view.MarkerPreviews = catalog.BuildMarkerPreviews(ctx, file, h.MarkerImageURLs.ResolveURLs)
	}
	return view
}

func (h *MarkersHandler) ensureMarkerEditable(ctx context.Context, file *models.MediaFile) error {
	if h.Libraries == nil {
		return apiError(http.StatusServiceUnavailable, "unavailable", "Marker library access is not configured")
	}
	library, err := h.Libraries.GetByID(ctx, file.MediaFolderID)
	if err != nil {
		return markerServiceError(err)
	}
	if library == nil {
		return apiError(http.StatusNotFound, "not_found", "Media library not found")
	}
	if !librarykind.IsMovie(library.Type) && !librarykind.IsTV(library.Type) && !librarykind.IsMixed(library.Type) {
		return apiError(http.StatusUnprocessableEntity, "validation_failed", "Markers can only be edited in Movie and Series libraries")
	}
	return nil
}
