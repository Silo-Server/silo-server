package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// MarkerFileResolver loads a media file by id for the manual-marker API.
type MarkerFileResolver interface {
	GetByID(ctx context.Context, id int) (*models.MediaFile, error)
	GetByContentID(ctx context.Context, contentID string) ([]*models.MediaFile, error)
	GetByEpisodeID(ctx context.Context, episodeID string) ([]*models.MediaFile, error)
}

// ManualMarkerWriter persists/clears manual markers.
type ManualMarkerWriter interface {
	UpsertMarkers(ctx context.Context, fileID int, update scanner.MarkerUpdate) (bool, error)
	ClearMarkers(ctx context.Context, fileID int, segments []string) (bool, error)
	UpsertAndClearMarkers(ctx context.Context, fileID int, update scanner.MarkerUpdate, clearSegments []string) (bool, error)
}

// MarkerContributor submits a file's eligible markers to enabled providers.
type MarkerContributor interface {
	ContributeFile(ctx context.Context, file *models.MediaFile, opts markers.ContributeOptions) ([]markers.ContributionOutcome, error)
}

// MarkerContributionLister reads contribution history for a file.
type MarkerContributionLister interface {
	ListByFile(ctx context.Context, fileID int) ([]markers.ContributionRow, error)
}

type MarkerAuditLister interface {
	ListMarkerEditAudit(ctx context.Context, fileIDs []int, limit int) ([]scanner.MarkerEditAuditRow, error)
	ListAllMarkerEditAudit(ctx context.Context, limit int) ([]scanner.MarkerEditAuditRow, error)
}

// MarkersHandler serves the manual-marker + contribution API. The marker
// read/write/clear routes are mounted for any authenticated viewer (users fix
// and create markers from the player); the contribution + history routes stay
// admin-only. A successful manual write fires a background contribution run
// (see maybeContribute) so corrected markers reach enabled providers.
type MarkersHandler struct {
	Files         MarkerFileResolver
	Writer        ManualMarkerWriter
	Contributor   MarkerContributor
	Contributions MarkerContributionLister
	AuditHistory  MarkerAuditLister
	Notifier      PlaybackMarkerUpdateNotifier
	// Authorizer enforces per-item access on file lookups so a viewer can only
	// edit markers for content they can actually watch. When nil (tests) the
	// handler falls back to an unchecked lookup.
	Authorizer *MediaFileAuthorizer
	// BaseContext is the lifetime context for detached background work (set from
	// the app context) so contribution goroutines cancel on shutdown.
	BaseContext context.Context
	logger      *slog.Logger
}

// NewMarkersHandler constructs the handler.
func NewMarkersHandler(files MarkerFileResolver, writer ManualMarkerWriter, contributor MarkerContributor, contributions MarkerContributionLister, notifier PlaybackMarkerUpdateNotifier, logger *slog.Logger) *MarkersHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &MarkersHandler{Files: files, Writer: writer, Contributor: contributor, Contributions: contributions, Notifier: notifier, logger: logger}
}

var manualMarkerConfidence = 1.0

const manualMarkerAlgorithm = "manual:v1"

// markerContributeTimeout bounds the detached contribution run kicked off after
// a manual save so a slow or rate-limited provider can't leak goroutines.
const markerContributeTimeout = 30 * time.Second

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

type contributionOutcomeResponse struct {
	Provider          string `json:"provider"`
	Segment           string `json:"segment"`
	Status            string `json:"status"`
	SubmissionID      string `json:"submission_id,omitempty"`
	Reason            string `json:"reason,omitempty"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
}

type contributionRowResponse struct {
	ID               string     `json:"id"`
	MediaFileID      int        `json:"media_file_id"`
	Provider         string     `json:"provider"`
	Segment          string     `json:"segment"`
	Source           string     `json:"source"`
	SubmittedStartMS *int64     `json:"submitted_start_ms,omitempty"`
	SubmittedEndMS   *int64     `json:"submitted_end_ms,omitempty"`
	VideoDurationMS  *int64     `json:"video_duration_ms,omitempty"`
	ContentHash      string     `json:"content_hash"`
	SubmissionID     *string    `json:"submission_id,omitempty"`
	Status           string     `json:"status"`
	HTTPStatus       *int       `json:"http_status,omitempty"`
	Error            *string    `json:"error,omitempty"`
	SubmittedAt      *time.Time `json:"submitted_at,omitempty"`
	UpdatedAt        *time.Time `json:"updated_at,omitempty"`
}

type markerEditAuditResponse struct {
	ID                   int64          `json:"id"`
	MediaFileID          int            `json:"media_file_id"`
	ItemID               *string        `json:"item_id,omitempty"`
	ItemType             *string        `json:"item_type,omitempty"`
	MediaTitle           *string        `json:"media_title,omitempty"`
	FilePath             *string        `json:"file_path,omitempty"`
	Segment              string         `json:"segment"`
	Action               string         `json:"action"`
	Before               *segmentMarker `json:"before"`
	After                *segmentMarker `json:"after"`
	UserID               *int           `json:"user_id,omitempty"`
	Username             *string        `json:"username,omitempty"`
	ImpersonatorUserID   *int           `json:"impersonator_user_id,omitempty"`
	ImpersonatorUsername *string        `json:"impersonator_username,omitempty"`
	APIKeyID             *int64         `json:"api_key_id,omitempty"`
	RequestID            *string        `json:"request_id,omitempty"`
	ClientIP             *string        `json:"client_ip,omitempty"`
	UserAgent            *string        `json:"user_agent,omitempty"`
	CreatedAt            time.Time      `json:"created_at"`
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

func (h *MarkersHandler) loadFile(w http.ResponseWriter, r *http.Request) (*models.MediaFile, bool) {
	if h == nil || h.Files == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Marker editing is not configured")
		return nil, false
	}
	fileID, err := strconv.Atoi(chi.URLParam(r, "fileId"))
	if err != nil || fileID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "A valid file id is required")
		return nil, false
	}
	return h.authorizeFile(w, r, fileID)
}

// authorizeFile loads a file, enforcing per-item access when an authorizer is
// configured. The 404-on-denial mirrors the playback/subtitle paths so marker
// editing can't be used to probe content outside a profile's library scope.
func (h *MarkersHandler) authorizeFile(w http.ResponseWriter, r *http.Request, fileID int) (*models.MediaFile, bool) {
	if h.Authorizer != nil {
		file, err := h.Authorizer.Authorize(r, fileID)
		if err != nil {
			switch {
			case errors.Is(err, catalog.ErrItemNotFound), errors.Is(err, catalog.ErrEpisodeNotFound):
				writeError(w, http.StatusNotFound, "not_found", "Media file not found")
			default:
				h.logger.ErrorContext(r.Context(), "markers: authorize failed", "file_id", fileID, "error", err)
				writeError(w, http.StatusInternalServerError, "internal_error", "Failed to authorize media file")
			}
			return nil, false
		}
		return file, true
	}
	file, err := h.Files.GetByID(r.Context(), fileID)
	if err != nil || file == nil {
		writeError(w, http.StatusNotFound, "not_found", "Media file not found")
		return nil, false
	}
	return file, true
}

func (h *MarkersHandler) loadItemPrimaryFile(w http.ResponseWriter, r *http.Request) (*models.MediaFile, bool) {
	if h == nil || h.Files == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Marker editing is not configured")
		return nil, false
	}
	itemID := strings.TrimSpace(chi.URLParam(r, "id"))
	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "A valid item id is required")
		return nil, false
	}

	files, err := h.Files.GetByEpisodeID(r.Context(), itemID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "markers: episode file lookup failed", "item_id", itemID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load item files")
		return nil, false
	}
	if len(files) == 0 {
		files, err = h.Files.GetByContentID(r.Context(), itemID)
		if err != nil {
			h.logger.ErrorContext(r.Context(), "markers: content file lookup failed", "item_id", itemID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load item files")
			return nil, false
		}
	}
	if len(files) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Media file not found for item")
		return nil, false
	}
	// Re-validate the resolved primary file through the authorizer so item-id
	// edits are access-checked the same way file-id edits are.
	if h.Authorizer != nil {
		for _, file := range files {
			if file == nil {
				continue
			}
			authorized, err := h.Authorizer.Authorize(r, file.ID)
			if err == nil {
				return authorized, true
			}
			if errors.Is(err, catalog.ErrItemNotFound) || errors.Is(err, catalog.ErrEpisodeNotFound) {
				continue
			}
			h.logger.ErrorContext(r.Context(), "markers: authorize item file failed", "item_id", itemID, "file_id", file.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to authorize media file")
			return nil, false
		}
		writeError(w, http.StatusNotFound, "not_found", "Media file not found for item")
		return nil, false
	}
	for _, file := range files {
		if file != nil {
			return file, true
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "Media file not found for item")
	return nil, false
}

func (h *MarkersHandler) auditContext(r *http.Request) context.Context {
	claims := apimw.GetClaims(r.Context())
	if claims == nil {
		return r.Context()
	}
	audit := scanner.MarkerAuditContext{
		UserID:             &claims.UserID,
		ImpersonatorUserID: claims.ImpersonatorUserID,
		RequestID:          chimw.GetReqID(r.Context()),
		ClientIP:           clientip.FromContext(r.Context()),
		UserAgent:          r.UserAgent(),
	}
	if claims.APIKeyID > 0 {
		apiKeyID := claims.APIKeyID
		audit.APIKeyID = &apiKeyID
	}
	return scanner.WithMarkerAuditContext(r.Context(), audit)
}

// HandleGetFileMarkers returns the current markers + provenance for a file.
func (h *MarkersHandler) HandleGetFileMarkers(w http.ResponseWriter, r *http.Request) {
	file, ok := h.loadFile(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, fileMarkers(file))
}

// HandleGetItemMarkers returns markers for the item's primary file.
func (h *MarkersHandler) HandleGetItemMarkers(w http.ResponseWriter, r *http.Request) {
	file, ok := h.loadItemPrimaryFile(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, fileMarkers(file))
}

// HandleSetFileMarkers upserts the manual marker layer. Each segment key may be
// an object {start, end} to set, or null to clear; absent keys are unchanged.
func (h *MarkersHandler) HandleSetFileMarkers(w http.ResponseWriter, r *http.Request) {
	file, ok := h.loadFile(w, r)
	if !ok {
		return
	}
	h.setMarkersForFile(w, r, file)
}

// HandleSetItemMarkers upserts manual markers on the item's primary file.
func (h *MarkersHandler) HandleSetItemMarkers(w http.ResponseWriter, r *http.Request) {
	file, ok := h.loadItemPrimaryFile(w, r)
	if !ok {
		return
	}
	h.setMarkersForFile(w, r, file)
}

func (h *MarkersHandler) setMarkersForFile(w http.ResponseWriter, r *http.Request, file *models.MediaFile) {
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
	var setSegs []string

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
		setSegs = append(setSegs, seg)
	}

	if _, err := h.Writer.UpsertAndClearMarkers(h.auditContext(r), file.ID, update, clears); err != nil {
		h.logger.ErrorContext(r.Context(), "markers: save failed", "file_id", file.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save markers")
		return
	}

	refreshed, err := h.reloadAndNotify(r.Context(), file.ID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "markers: reload after save failed", "file_id", file.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Markers saved but failed to reload")
		return
	}
	h.maybeContribute(refreshed, setSegs)
	writeJSON(w, http.StatusOK, fileMarkers(refreshed))
}

// HandleClearFileSegment clears a single segment.
func (h *MarkersHandler) HandleClearFileSegment(w http.ResponseWriter, r *http.Request) {
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
	if _, err := h.Writer.ClearMarkers(h.auditContext(r), file.ID, []string{segment}); err != nil {
		h.logger.ErrorContext(r.Context(), "markers: clear segment failed", "file_id", file.ID, "segment", segment, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to clear marker")
		return
	}
	refreshed, err := h.reloadAndNotify(r.Context(), file.ID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "markers: reload after clear failed", "file_id", file.ID, "segment", segment, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Marker cleared but failed to reload")
		return
	}
	writeJSON(w, http.StatusOK, fileMarkers(refreshed))
}

// HandleContributeFile submits the file's eligible markers to enabled providers.
func (h *MarkersHandler) HandleContributeFile(w http.ResponseWriter, r *http.Request) {
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
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
		h.logger.ErrorContext(r.Context(), "markers: contribute failed", "file_id", file.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Contribution failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"outcomes": contributionOutcomeResponses(outcomes)})
}

// HandleListFileContributions returns the contribution history for a file.
func (h *MarkersHandler) HandleListFileContributions(w http.ResponseWriter, r *http.Request) {
	file, ok := h.loadFile(w, r)
	if !ok {
		return
	}
	if h.Contributions == nil {
		writeJSON(w, http.StatusOK, map[string]any{"contributions": []contributionRowResponse{}})
		return
	}
	rows, err := h.Contributions.ListByFile(r.Context(), file.ID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "markers: list contributions failed", "file_id", file.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load contributions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contributions": contributionRowResponses(rows)})
}

// HandleListFileMarkerHistory returns recent semantic marker edit audit rows for one file.
func (h *MarkersHandler) HandleListFileMarkerHistory(w http.ResponseWriter, r *http.Request) {
	file, ok := h.loadFile(w, r)
	if !ok {
		return
	}
	h.listMarkerHistory(w, r, []int{file.ID})
}

// HandleListItemMarkerHistory returns recent marker edit audit rows for every file version on an item.
func (h *MarkersHandler) HandleListItemMarkerHistory(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Files == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Marker history is not configured")
		return
	}
	itemID := strings.TrimSpace(chi.URLParam(r, "id"))
	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "A valid item id is required")
		return
	}

	files, err := h.Files.GetByEpisodeID(r.Context(), itemID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "markers: episode history file lookup failed", "item_id", itemID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load item files")
		return
	}
	if len(files) == 0 {
		files, err = h.Files.GetByContentID(r.Context(), itemID)
		if err != nil {
			h.logger.ErrorContext(r.Context(), "markers: content history file lookup failed", "item_id", itemID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load item files")
			return
		}
	}

	fileIDs := make([]int, 0, len(files))
	for _, file := range files {
		if file != nil {
			fileIDs = append(fileIDs, file.ID)
		}
	}
	if len(fileIDs) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Media file not found for item")
		return
	}
	h.listMarkerHistory(w, r, fileIDs)
}

// HandleListMarkerHistory returns recent semantic marker edit audit rows across all files.
func (h *MarkersHandler) HandleListMarkerHistory(w http.ResponseWriter, r *http.Request) {
	if h.AuditHistory == nil {
		writeJSON(w, http.StatusOK, map[string]any{"history": []markerEditAuditResponse{}})
		return
	}
	limit, ok := markerHistoryLimit(w, r)
	if !ok {
		return
	}
	rows, err := h.AuditHistory.ListAllMarkerEditAudit(r.Context(), limit)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "markers: list all history failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load marker history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": markerEditAuditResponses(rows)})
}

func (h *MarkersHandler) listMarkerHistory(w http.ResponseWriter, r *http.Request, fileIDs []int) {
	if h.AuditHistory == nil {
		writeJSON(w, http.StatusOK, map[string]any{"history": []markerEditAuditResponse{}})
		return
	}
	limit, ok := markerHistoryLimit(w, r)
	if !ok {
		return
	}
	rows, err := h.AuditHistory.ListMarkerEditAudit(r.Context(), fileIDs, limit)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "markers: list history failed", "file_ids", fileIDs, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load marker history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": markerEditAuditResponses(rows)})
}

func markerHistoryLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	limit := 25
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "A valid limit is required")
			return 0, false
		}
		limit = parsed
	}
	return limit, true
}

func (h *MarkersHandler) reloadAndNotify(ctx context.Context, fileID int) (*models.MediaFile, error) {
	refreshed, err := h.Files.GetByID(ctx, fileID)
	if err != nil || refreshed == nil {
		if err == nil {
			err = scanner.ErrFileNotFound
		}
		return nil, err
	}
	if h.Notifier != nil {
		h.Notifier.MarkersUpdated(ctx, refreshed)
	}
	return refreshed, nil
}

// maybeContribute fires a best-effort, detached contribution run for the
// segments just set by a manual save. ContributeFile self-gates on each
// provider's contribute_enabled flag, only submits scanner/manual-sourced
// segments, and is idempotent — so this is a no-op when contribution is off or
// the marker was already submitted. It runs in the background so the save
// responds immediately and is never blocked by provider rate limits.
func (h *MarkersHandler) maybeContribute(file *models.MediaFile, segments []string) {
	if h == nil || h.Contributor == nil || file == nil || len(segments) == 0 {
		return
	}
	kinds := make([]markers.MarkerKind, 0, len(segments))
	for _, seg := range segments {
		if kind, ok := markerKindForName(seg); ok {
			kinds = append(kinds, kind)
		}
	}
	if len(kinds) == 0 {
		return
	}
	base := h.BaseContext
	if base == nil {
		base = context.Background()
	}
	go func() {
		ctx, cancel := context.WithTimeout(base, markerContributeTimeout)
		defer cancel()
		if _, err := h.Contributor.ContributeFile(ctx, file, markers.ContributeOptions{Segments: kinds}); err != nil {
			h.logger.Warn("markers: background contribution failed", "file_id", file.ID, "error", err)
		}
	}()
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

func markerNameForKind(kind markers.MarkerKind) string {
	switch kind {
	case markers.MarkerKindIntro:
		return "intro"
	case markers.MarkerKindCredits:
		return "credits"
	case markers.MarkerKindRecap:
		return "recap"
	case markers.MarkerKindPreview:
		return "preview"
	default:
		return ""
	}
}

func contributionOutcomeResponses(outcomes []markers.ContributionOutcome) []contributionOutcomeResponse {
	resp := make([]contributionOutcomeResponse, 0, len(outcomes))
	for _, o := range outcomes {
		item := contributionOutcomeResponse{
			Provider:     o.Provider,
			Segment:      markerNameForKind(o.Segment),
			Status:       o.Status,
			SubmissionID: o.SubmissionID,
			Reason:       o.Reason,
		}
		if o.RetryAfter > 0 {
			item.RetryAfterSeconds = int(o.RetryAfter.Seconds())
		}
		resp = append(resp, item)
	}
	return resp
}

func contributionRowResponses(rows []markers.ContributionRow) []contributionRowResponse {
	resp := make([]contributionRowResponse, 0, len(rows))
	for _, row := range rows {
		item := contributionRowResponse{
			ID:               row.ID,
			MediaFileID:      row.MediaFileID,
			Provider:         row.Provider,
			Segment:          row.SegmentKind,
			Source:           row.Source,
			SubmittedStartMS: row.SubmittedStartMs,
			SubmittedEndMS:   row.SubmittedEndMs,
			VideoDurationMS:  row.VideoDurationMs,
			ContentHash:      row.ContentHash,
			SubmissionID:     row.SubmissionID,
			Status:           row.Status,
			HTTPStatus:       row.HTTPStatus,
			Error:            row.Error,
		}
		if !row.SubmittedAt.IsZero() {
			submittedAt := row.SubmittedAt
			item.SubmittedAt = &submittedAt
		}
		if !row.UpdatedAt.IsZero() {
			updatedAt := row.UpdatedAt
			item.UpdatedAt = &updatedAt
		}
		resp = append(resp, item)
	}
	return resp
}

func markerEditAuditResponses(rows []scanner.MarkerEditAuditRow) []markerEditAuditResponse {
	resp := make([]markerEditAuditResponse, 0, len(rows))
	for _, row := range rows {
		resp = append(resp, markerEditAuditResponse{
			ID:                   row.ID,
			MediaFileID:          row.MediaFileID,
			ItemID:               row.ItemID,
			ItemType:             row.ItemType,
			MediaTitle:           row.MediaTitle,
			FilePath:             row.FilePath,
			Segment:              row.SegmentKind,
			Action:               row.Action,
			Before:               markerAuditSegmentResponse(row.Before),
			After:                markerAuditSegmentResponse(row.After),
			UserID:               row.UserID,
			Username:             row.Username,
			ImpersonatorUserID:   row.ImpersonatorUserID,
			ImpersonatorUsername: row.ImpersonatorUsername,
			APIKeyID:             row.APIKeyID,
			RequestID:            row.RequestID,
			ClientIP:             row.ClientIP,
			UserAgent:            row.UserAgent,
			CreatedAt:            row.CreatedAt,
		})
	}
	return resp
}

func markerAuditSegmentResponse(segment *scanner.MarkerAuditSegment) *segmentMarker {
	if segment == nil {
		return nil
	}
	return &segmentMarker{
		Start:      segment.Start,
		End:        segment.End,
		Source:     segment.Source,
		Provider:   segment.Provider,
		Confidence: segment.Confidence,
		Algorithm:  segment.Algorithm,
		DetectedAt: segment.DetectedAt,
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
