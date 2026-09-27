package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/intromarkers"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
)

// IntroEpisodeAnalyzer runs local marker analysis for one item: an
// episode's intros or credits, as selected, or a movie's credits.
type IntroEpisodeAnalyzer interface {
	AnalyzeEpisodeKinds(ctx context.Context, episodeID string, kinds intromarkers.EpisodeMarkerKinds) (intromarkers.RunSummary, error)
	AnalyzeMovie(ctx context.Context, contentID string) (intromarkers.RunSummary, error)
}

var (
	// introMarkerKinds is what the endpoints that predate credits analyze:
	// the frozen /api/v1 routes and /api/v2 redetect-intro.
	introMarkerKinds = intromarkers.EpisodeMarkerKinds{Intro: true}
	// allMarkerKinds selects every kind local analysis finds.
	allMarkerKinds = intromarkers.EpisodeMarkerKinds{Intro: true, Credits: true}
)

// MarkerItemEligibilityChecker reports whether local marker analysis may run
// for an episode or a movie, and which of the two the item is.
type MarkerItemEligibilityChecker interface {
	MarkerItemEligibility(ctx context.Context, itemID string) (*intromarkers.MarkerItemEligibility, error)
}

type MarkerSettingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}

type AdminIntroFileResolver interface {
	GetByEpisodeID(ctx context.Context, episodeID string) ([]*models.MediaFile, error)
	GetByContentID(ctx context.Context, contentID string) ([]*models.MediaFile, error)
}

type AdminIntroHandler struct {
	analyzer             IntroEpisodeAnalyzer
	eligibility          MarkerItemEligibilityChecker
	Settings             MarkerSettingsReader
	FileResolver         AdminIntroFileResolver
	MarkerUpdateNotifier PlaybackMarkerUpdateNotifier
	OnlineMarkers        MarkerRefreshService
	baseContext          context.Context
	inFlight             sync.Map
	logger               *slog.Logger
}

func NewAdminIntroHandler(
	analyzer IntroEpisodeAnalyzer,
	eligibility MarkerItemEligibilityChecker,
	baseContext context.Context,
	logger *slog.Logger,
) *AdminIntroHandler {
	if baseContext == nil {
		baseContext = context.Background()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &AdminIntroHandler{
		analyzer:    analyzer,
		eligibility: eligibility,
		baseContext: baseContext,
		logger:      logger,
	}
}

type redetectIntroResponse struct {
	Status string `json:"status"`
}

func (h *AdminIntroHandler) HandleRefreshEpisodeMarkers(w http.ResponseWriter, r *http.Request) {
	h.handleEpisodeMarkers(w, r, "refresh")
}

func (h *AdminIntroHandler) HandleRedetectEpisodeIntro(w http.ResponseWriter, r *http.Request) {
	h.handleEpisodeMarkers(w, r, "redetect")
}

// handleEpisodeMarkers serves the frozen /api/v1 endpoints, which analyze
// episode intros only and keep their original messages.
func (h *AdminIntroHandler) handleEpisodeMarkers(w http.ResponseWriter, r *http.Request, action string) {
	status, err := h.refreshItemMarkers(r.Context(), chi.URLParam(r, "id"), action, introMarkerKinds)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, redetectIntroResponse{Status: status})
}

// RefreshEpisodeMarkers serves the frozen /api/v1 routes and the /api/v2
// refresh-markers ("refresh-v2") and redetect-intro ("redetect") operations.
// Every action but refresh-v2 predates credits and analyzes episode intros
// only; refresh-v2 also takes movies, for their credits.
func (h *AdminIntroHandler) RefreshEpisodeMarkers(ctx context.Context, itemID, action string) (string, error) {
	if action == "refresh-v2" {
		return h.refreshEpisodeMarkersV2(ctx, itemID)
	}
	return h.refreshItemMarkers(ctx, itemID, action, introMarkerKinds)
}

// refreshItemMarkers queues local marker analysis of an item for kinds. A
// movie gets credits only, so a request without credits takes episodes
// only, rejects a movie like any other item that is not an episode, and
// keeps the messages of the endpoints that predate movies.
func (h *AdminIntroHandler) refreshItemMarkers(ctx context.Context, itemID, action string, kinds intromarkers.EpisodeMarkerKinds) (string, error) {
	episodesOnly := !kinds.Credits
	if h == nil || h.analyzer == nil || h.eligibility == nil {
		return "", apiError(http.StatusServiceUnavailable, "unavailable", "Intro detection is not configured")
	}

	if itemID == "" {
		return "", apiError(http.StatusBadRequest, "bad_request", "Item ID is required")
	}

	messages := itemMarkerMessages
	if episodesOnly {
		messages = episodeMarkerMessages
	}
	eligibility, err := h.eligibility.MarkerItemEligibility(ctx, itemID)
	if err == nil && episodesOnly && eligibility.Kind == intromarkers.MarkerItemMovie {
		err = intromarkers.ErrMarkerItemNotFound
	}
	if err != nil {
		if errors.Is(err, intromarkers.ErrMarkerItemNotFound) {
			return "", apiError(http.StatusBadRequest, "bad_request", messages.wrongKind)
		}
		h.logger.ErrorContext(ctx, "admin intro: resolve item failed", "item_id", itemID, "error", err)
		return "", apiError(http.StatusInternalServerError, "internal_error", messages.resolveFailed)
	}
	if !eligibility.HasMediaFiles {
		return "", apiError(http.StatusConflict, "conflict", messages.noFiles)
	}
	if !eligibility.IntroDetectionEnabled {
		return "", apiError(http.StatusConflict, "conflict", messages.disabled)
	}
	if h.Settings == nil {
		return "", apiError(http.StatusServiceUnavailable, "unavailable", "Marker settings are not configured")
	}
	raw, err := h.Settings.Get(ctx, markers.SettingMode)
	if err != nil {
		h.logger.ErrorContext(ctx, "admin markers: load mode failed", "item_id", itemID, "error", err)
		return "", apiError(http.StatusInternalServerError, "internal_error", "Failed to load marker settings")
	}
	mode := markers.NormalizeMode(raw)
	if !markers.ShouldRunLocal(mode) {
		message := "Local intro detection is disabled"
		switch mode {
		case markers.ModeOff:
			message = "Marker detection is disabled"
		case markers.ModeOnline:
			message = "Online-only marker refresh is not available for this endpoint"
		}
		return "", apiError(http.StatusConflict, "conflict", message)
	}

	if _, loaded := h.inFlight.LoadOrStore(itemID, struct{}{}); loaded {
		return markerRefreshAlreadyRunning, nil
	}

	kind := eligibility.Kind
	go func() {
		defer h.inFlight.Delete(itemID)
		start := time.Now()
		h.logger.InfoContext(ctx, "admin markers: item refresh started",
			"item_id", itemID,
			"kind", kind,
			"action", action,
			"intro", kinds.Intro,
			"credits", kinds.Credits)
		summary, err := h.analyzeItem(h.baseContext, itemID, kind, kinds)
		if err != nil {
			h.logger.ErrorContext(ctx, "admin markers: item refresh failed",
				"item_id", itemID,
				"kind", kind,
				"action", action,
				"duration", time.Since(start),
				"error", err)
			return
		}
		h.logger.InfoContext(ctx, "admin markers: item refresh finished",
			"item_id", itemID,
			"kind", kind,
			"action", action,
			"duration", time.Since(start),
			"files_considered", summary.FilesConsidered,
			"season_groups_considered", summary.SeasonGroupsConsidered,
			"chapter_markers_written", summary.ChapterMarkersWritten,
			"chromaprint_markers_written", summary.ChromaprintMarkersWritten,
			"fingerprint_cache_hits", summary.FingerprintCacheHits,
			"fingerprints_computed", summary.FingerprintsComputed,
			"credits_chapter_markers_written", summary.CreditsChapterMarkersWritten,
			"credits_audio_markers_written", summary.CreditsAudioMarkersWritten,
			"credits_audio_video_markers_written", summary.CreditsAudioVideoMarkersWritten,
			"credits_video_markers_written", summary.CreditsVideoMarkersWritten,
			"movie_credits_markers_written", summary.MovieCreditsMarkersWritten,
			"credits_fingerprints_computed", summary.CreditsFingerprintsComputed,
			"credits_tail_scans_computed", summary.CreditsTailScansComputed,
			"errors", len(summary.Errors))
		h.notifyItemMarkerUpdates(h.baseContext, itemID, kind, action)
	}()

	return markerRefreshQueued, nil
}

// markerRefreshMessages are the messages of a local marker refresh that
// depend on which items the endpoint accepts.
type markerRefreshMessages struct {
	wrongKind, resolveFailed, noFiles, disabled string
}

var (
	// episodeMarkerMessages are the /api/v1 messages, unchanged since v1
	// was frozen.
	episodeMarkerMessages = markerRefreshMessages{
		wrongKind:     "Item must be an episode",
		resolveFailed: "Failed to resolve episode",
		noFiles:       "Episode has no media files to analyze",
		disabled:      "Intro detection is disabled for this episode's library",
	}
	itemMarkerMessages = markerRefreshMessages{
		wrongKind:     "Item must be an episode or a movie",
		resolveFailed: "Failed to resolve item",
		noFiles:       "Item has no media files to analyze",
		disabled:      "Marker detection is disabled for this item's library",
	}
)

// analyzeItem runs local analysis of an item of kind: an episode for kinds,
// or a movie for its credits.
func (h *AdminIntroHandler) analyzeItem(ctx context.Context, itemID, kind string, kinds intromarkers.EpisodeMarkerKinds) (intromarkers.RunSummary, error) {
	if kind == intromarkers.MarkerItemMovie {
		return h.analyzer.AnalyzeMovie(ctx, itemID)
	}
	return h.analyzer.AnalyzeEpisodeKinds(ctx, itemID, kinds)
}

// itemFiles returns the files of an item of kind: an episode's files, or a
// movie's own files without its extras.
func (h *AdminIntroHandler) itemFiles(ctx context.Context, itemID, kind string) ([]*models.MediaFile, error) {
	if kind != intromarkers.MarkerItemMovie {
		return h.FileResolver.GetByEpisodeID(ctx, itemID)
	}
	files, err := h.FileResolver.GetByContentID(ctx, itemID)
	if err != nil {
		return nil, err
	}
	movieFiles := files[:0]
	for _, file := range files {
		if file != nil && file.EpisodeID == "" && file.ExtraID == "" {
			movieFiles = append(movieFiles, file)
		}
	}
	return movieFiles, nil
}

func (h *AdminIntroHandler) notifyItemMarkerUpdates(ctx context.Context, itemID, kind, action string) {
	if h == nil || h.FileResolver == nil || h.MarkerUpdateNotifier == nil {
		return
	}
	files, err := h.itemFiles(ctx, itemID, kind)
	if err != nil {
		h.logger.WarnContext(ctx, "admin markers: reload item files for marker update failed",
			"item_id", itemID,
			"kind", kind,
			"action", action,
			"error", err)
		return
	}
	for _, file := range files {
		if !hasAnyPlaybackMarker(file) {
			continue
		}
		h.MarkerUpdateNotifier.MarkersUpdated(ctx, file)
		h.logger.InfoContext(ctx, "admin markers: emitted marker update",
			"item_id", itemID,
			"action", action,
			"file_id", file.ID)
	}
}

func hasAnyPlaybackMarker(file *models.MediaFile) bool {
	return file != nil &&
		((file.IntroStart != nil && file.IntroEnd != nil) ||
			(file.CreditsStart != nil && file.CreditsEnd != nil))
}
