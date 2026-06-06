package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// MarkerFileResolver loads a media file by id for the manual-marker API.
type MarkerFileResolver interface {
	GetByID(ctx context.Context, id int) (*models.MediaFile, error)
}

// ManualMarkerWriter persists/clears manual markers.
type ManualMarkerWriter interface {
	UpsertMarkers(ctx context.Context, fileID int, update scanner.MarkerUpdate) (bool, error)
	ClearMarkers(ctx context.Context, fileID int, segments []string) (bool, error)
}

// MarkerContributor submits a file's eligible markers to enabled providers.
type MarkerContributor interface {
	ContributeFile(ctx context.Context, file *models.MediaFile, opts markers.ContributeOptions) ([]markers.ContributionOutcome, error)
}

// MarkerContributionLister reads contribution history for a file.
type MarkerContributionLister interface {
	ListByFile(ctx context.Context, fileID int) ([]markers.ContributionRow, error)
}

// AdminMarkersHandler serves the admin manual-marker + contribution API. All
// routes are mounted under the RequireAdmin group.
type AdminMarkersHandler struct {
	Files         MarkerFileResolver
	Writer        ManualMarkerWriter
	Contributor   MarkerContributor
	Contributions MarkerContributionLister
	Notifier      PlaybackMarkerUpdateNotifier
	logger        *slog.Logger
}

// NewAdminMarkersHandler constructs the handler.
func NewAdminMarkersHandler(files MarkerFileResolver, writer ManualMarkerWriter, contributor MarkerContributor, contributions MarkerContributionLister, notifier PlaybackMarkerUpdateNotifier, logger *slog.Logger) *AdminMarkersHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AdminMarkersHandler{Files: files, Writer: writer, Contributor: contributor, Contributions: contributions, Notifier: notifier, logger: logger}
}

var manualMarkerConfidence = 1.0

const manualMarkerAlgorithm = "manual:v1"

var markerSegmentNames = []string{"intro", "credits", "recap", "preview"}

type segmentInput struct {
	Start *float64 `json:"start"`
	End   *float64 `json:"end"`
}

type segmentMarker struct {
	Start      *float64   `json:"start"`
	End        *float64   `json:"end"`
	Source     *string    `json:"source"`
	Provider   *string    `json:"provider"`
	Confidence *float64   `json:"confidence"`
	Algorithm  *string    `json:"algorithm"`
	DetectedAt *time.Time `json:"detected_at"`
}

type fileMarkersResponse struct {
	FileID  int           `json:"file_id"`
	Intro   segmentMarker `json:"intro"`
	Credits segmentMarker `json:"credits"`
	Recap   segmentMarker `json:"recap"`
	Preview segmentMarker `json:"preview"`
}

func fileMarkers(file *models.MediaFile) fileMarkersResponse {
	return fileMarkersResponse{
		FileID: file.ID,
		Intro: segmentMarker{file.IntroStart, file.IntroEnd, file.IntroMarkersSource, file.IntroMarkersProvider,
			file.IntroMarkersConfidence, file.IntroMarkersAlgorithm, file.IntroMarkersDetectedAt},
		Credits: segmentMarker{file.CreditsStart, file.CreditsEnd, file.CreditsMarkersSource, file.CreditsMarkersProvider,
			file.CreditsMarkersConfidence, file.CreditsMarkersAlgorithm, file.CreditsMarkersDetectedAt},
		Recap: segmentMarker{file.RecapStart, file.RecapEnd, file.RecapMarkersSource, file.RecapMarkersProvider,
			file.RecapMarkersConfidence, file.RecapMarkersAlgorithm, file.RecapMarkersDetectedAt},
		Preview: segmentMarker{file.PreviewStart, file.PreviewEnd, file.PreviewMarkersSource, file.PreviewMarkersProvider,
			file.PreviewMarkersConfidence, file.PreviewMarkersAlgorithm, file.PreviewMarkersDetectedAt},
	}
}

func (h *AdminMarkersHandler) loadFile(w http.ResponseWriter, r *http.Request) (*models.MediaFile, bool) {
	if h == nil || h.Files == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Marker editing is not configured")
		return nil, false
	}
	fileID, err := strconv.Atoi(chi.URLParam(r, "fileId"))
	if err != nil || fileID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "A valid file id is required")
		return nil, false
	}
	file, err := h.Files.GetByID(r.Context(), fileID)
	if err != nil || file == nil {
		writeError(w, http.StatusNotFound, "not_found", "Media file not found")
		return nil, false
	}
	return file, true
}

// HandleGetFileMarkers returns the current markers + provenance for a file.
func (h *AdminMarkersHandler) HandleGetFileMarkers(w http.ResponseWriter, r *http.Request) {
	file, ok := h.loadFile(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, fileMarkers(file))
}

// HandleSetFileMarkers upserts the manual marker layer. Each segment key may be
// an object {start, end} to set, or null to clear; absent keys are unchanged.
func (h *AdminMarkersHandler) HandleSetFileMarkers(w http.ResponseWriter, r *http.Request) {
	file, ok := h.loadFile(w, r)
	if !ok {
		return
	}
	if h.Writer == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Marker writing is not configured")
		return
	}

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	duration := float64(file.Duration)
	update := scanner.MarkerUpdate{
		MarkersSource:     models.MarkerSourceManual,
		MarkersConfidence: &manualMarkerConfidence,
		MarkersAlgorithm:  manualMarkerAlgorithm,
	}
	var clears []string
	hasSet := false

	for _, seg := range markerSegmentNames {
		val, present := raw[seg]
		if !present {
			continue
		}
		if isJSONNull(val) {
			clears = append(clears, seg)
			continue
		}
		var in segmentInput
		if err := json.Unmarshal(val, &in); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid "+seg+" marker")
			return
		}
		start, end, err := normalizeManualSegment(seg, in, duration)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		applyManualSegment(&update, seg, start, end)
		hasSet = true
	}

	if hasSet {
		if _, err := h.Writer.UpsertMarkers(r.Context(), file.ID, update); err != nil {
			h.logger.Error("admin markers: upsert failed", "file_id", file.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save markers")
			return
		}
	}
	if len(clears) > 0 {
		if _, err := h.Writer.ClearMarkers(r.Context(), file.ID, clears); err != nil {
			h.logger.Error("admin markers: clear failed", "file_id", file.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to clear markers")
			return
		}
	}

	refreshed := h.reloadAndNotify(r.Context(), file.ID)
	writeJSON(w, http.StatusOK, fileMarkers(refreshed))
}

// HandleClearFileSegment clears a single segment.
func (h *AdminMarkersHandler) HandleClearFileSegment(w http.ResponseWriter, r *http.Request) {
	file, ok := h.loadFile(w, r)
	if !ok {
		return
	}
	if h.Writer == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Marker writing is not configured")
		return
	}
	segment := chi.URLParam(r, "segment")
	if !isMarkerSegment(segment) {
		writeError(w, http.StatusBadRequest, "bad_request", "Unknown marker segment")
		return
	}
	if _, err := h.Writer.ClearMarkers(r.Context(), file.ID, []string{segment}); err != nil {
		h.logger.Error("admin markers: clear segment failed", "file_id", file.ID, "segment", segment, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to clear marker")
		return
	}
	refreshed := h.reloadAndNotify(r.Context(), file.ID)
	writeJSON(w, http.StatusOK, fileMarkers(refreshed))
}

// HandleContributeFile submits the file's eligible markers to enabled providers.
func (h *AdminMarkersHandler) HandleContributeFile(w http.ResponseWriter, r *http.Request) {
	file, ok := h.loadFile(w, r)
	if !ok {
		return
	}
	if h.Contributor == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Contribution is not configured")
		return
	}
	var body struct {
		Provider string   `json:"provider"`
		Segments []string `json:"segments"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
			return
		}
	}
	var kinds []markers.MarkerKind
	for _, name := range body.Segments {
		kind, ok := markerKindForName(name)
		if !ok {
			writeError(w, http.StatusBadRequest, "bad_request", "Unknown segment "+name)
			return
		}
		kinds = append(kinds, kind)
	}
	outcomes, err := h.Contributor.ContributeFile(r.Context(), file, markers.ContributeOptions{Provider: body.Provider, Segments: kinds})
	if err != nil {
		h.logger.Error("admin markers: contribute failed", "file_id", file.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Contribution failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"outcomes": outcomes})
}

// HandleListFileContributions returns the contribution history for a file.
func (h *AdminMarkersHandler) HandleListFileContributions(w http.ResponseWriter, r *http.Request) {
	file, ok := h.loadFile(w, r)
	if !ok {
		return
	}
	if h.Contributions == nil {
		writeJSON(w, http.StatusOK, map[string]any{"contributions": []any{}})
		return
	}
	rows, err := h.Contributions.ListByFile(r.Context(), file.ID)
	if err != nil {
		h.logger.Error("admin markers: list contributions failed", "file_id", file.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load contributions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contributions": rows})
}

func (h *AdminMarkersHandler) reloadAndNotify(ctx context.Context, fileID int) *models.MediaFile {
	refreshed, err := h.Files.GetByID(ctx, fileID)
	if err != nil || refreshed == nil {
		return &models.MediaFile{ID: fileID}
	}
	if h.Notifier != nil {
		h.Notifier.MarkersUpdated(ctx, refreshed)
	}
	return refreshed
}

func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

func isMarkerSegment(seg string) bool {
	_, ok := markerKindForName(seg)
	return ok
}

func markerKindForName(name string) (markers.MarkerKind, bool) {
	switch name {
	case "intro":
		return markers.MarkerKindIntro, true
	case "credits":
		return markers.MarkerKindCredits, true
	case "recap":
		return markers.MarkerKindRecap, true
	case "preview":
		return markers.MarkerKindPreview, true
	default:
		return 0, false
	}
}

// normalizeManualSegment applies the start/end defaults and validation, mirroring
// the contribution rules: intro/recap may omit start (=0); credits/preview may
// omit end (=duration); end must exceed start and stay within the file.
func normalizeManualSegment(seg string, in segmentInput, duration float64) (start, end float64, err error) {
	switch seg {
	case "intro", "recap":
		if in.Start != nil {
			start = *in.Start
		}
		if in.End == nil {
			return 0, 0, errSegment(seg, "end is required")
		}
		end = *in.End
	default: // credits, preview
		if in.Start == nil {
			return 0, 0, errSegment(seg, "start is required")
		}
		start = *in.Start
		if in.End != nil {
			end = *in.End
		} else if duration > 0 {
			end = duration
		} else {
			return 0, 0, errSegment(seg, "end is required when duration is unknown")
		}
	}
	if start < 0 || end <= start {
		return 0, 0, errSegment(seg, "end must be greater than start")
	}
	if duration > 0 && end > duration+1 {
		return 0, 0, errSegment(seg, "end exceeds the file duration")
	}
	return start, end, nil
}

func applyManualSegment(update *scanner.MarkerUpdate, seg string, start, end float64) {
	s, e := start, end
	switch seg {
	case "intro":
		update.IntroStart, update.IntroEnd = &s, &e
	case "credits":
		update.CreditsStart, update.CreditsEnd = &s, &e
	case "recap":
		update.RecapStart, update.RecapEnd = &s, &e
	case "preview":
		update.PreviewStart, update.PreviewEnd = &s, &e
	}
}

func errSegment(seg, msg string) error {
	return &markerValidationError{seg: seg, msg: msg}
}

type markerValidationError struct {
	seg string
	msg string
}

func (e *markerValidationError) Error() string { return e.seg + " marker: " + e.msg }
