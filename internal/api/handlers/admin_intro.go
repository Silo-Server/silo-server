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
	logger               *slog.Logger

	// runsMu guards runs, the marker work running for each item ID.
	runsMu sync.Mutex
	runs   map[string]*itemMarkerRun
}

// itemMarkerRun is the marker work running for one item: the kinds it
// analyzes, and the kinds requested while it runs that it does not cover,
// which run once it finishes.
type itemMarkerRun struct {
	running, pending intromarkers.EpisodeMarkerKinds
}

// claimItemRun reserves itemID for work on kinds and reports whether the
// caller must start that work. While other work on the item runs, it returns
// the status to report instead: already_running when the running and queued
// work covers kinds, or, when queue is set, queued after adding the kinds not
// yet running to the work that runs next. Without queue, any running work
// reports already_running.
func (h *AdminIntroHandler) claimItemRun(itemID string, kinds intromarkers.EpisodeMarkerKinds, queue bool) (bool, string) {
	h.runsMu.Lock()
	defer h.runsMu.Unlock()
	run := h.runs[itemID]
	if run == nil {
		if h.runs == nil {
			h.runs = map[string]*itemMarkerRun{}
		}
		h.runs[itemID] = &itemMarkerRun{running: kinds}
		return true, markerRefreshQueued
	}
	if !queue || !kinds.Without(run.running.Or(run.pending)).Any() {
		return false, markerRefreshAlreadyRunning
	}
	run.pending = run.pending.Or(kinds.Without(run.running))
	return false, markerRefreshQueued
}

// nextItemRun ends the running work on itemID. It returns the kinds queued
// behind that work, which the caller runs next, or reports false after
// releasing the item.
func (h *AdminIntroHandler) nextItemRun(itemID string) (intromarkers.EpisodeMarkerKinds, bool) {
	h.runsMu.Lock()
	defer h.runsMu.Unlock()
	run := h.runs[itemID]
	if run == nil || !run.pending.Any() {
		delete(h.runs, itemID)
		return intromarkers.EpisodeMarkerKinds{}, false
	}
	run.running, run.pending = run.pending, intromarkers.EpisodeMarkerKinds{}
	return run.running, true
}

// itemRunning reports whether marker work on itemID is running.
func (h *AdminIntroHandler) itemRunning(itemID string) bool {
	h.runsMu.Lock()
	defer h.runsMu.Unlock()
	return h.runs[itemID] != nil
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
// episode intros only, keep their original messages, and report
// already_running while any analysis of the episode runs.
func (h *AdminIntroHandler) handleEpisodeMarkers(w http.ResponseWriter, r *http.Request, action string) {
	status, err := h.refreshItemMarkers(r.Context(), chi.URLParam(r, "id"), action, introMarkerKinds, localRefreshOptions{})
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
	return h.refreshItemMarkers(ctx, itemID, action, introMarkerKinds, localRefreshOptions{queue: true})
}

// The marker kinds RedetectItemMarkers accepts.
const (
	RedetectMarkersIntro   = "intro"
	RedetectMarkersCredits = "credits"
	RedetectMarkersAll     = "all"
)

// RedetectItemMarkers serves the /api/v2 redetect-markers operation. It
// queues local re-detection of the marker kinds kind selects: an episode's
// intro, credits, or both ("all", the default), or a movie's credits, which
// "credits" and "all" mean for a movie. A movie has no local intro, so
// "intro" takes episodes only and rejects a movie as not an episode.
func (h *AdminIntroHandler) RedetectItemMarkers(ctx context.Context, itemID, kind string) (string, error) {
	var kinds intromarkers.EpisodeMarkerKinds
	switch kind {
	case RedetectMarkersIntro:
		kinds = introMarkerKinds
	case RedetectMarkersCredits:
		kinds = intromarkers.EpisodeMarkerKinds{Credits: true}
	case RedetectMarkersAll, "":
		kinds = allMarkerKinds
	default:
		return "", fieldError("kind", "Kind must be intro, credits, or all")
	}
	return h.refreshItemMarkers(ctx, itemID, "redetect-markers", kinds, localRefreshOptions{followSettings: true, queue: true})
}

// localRefreshOptions sets how refreshItemMarkers treats a request.
type localRefreshOptions struct {
	// followSettings narrows the requested kinds to the kinds
	// markers.detect_intros and markers.detect_credits leave on, and rejects
	// the request when none remain. The endpoints that predate those
	// settings leave it unset.
	followSettings bool
	// queue runs the requested kinds that the item's running analysis does
	// not cover once it finishes, instead of reporting already_running. The
	// frozen /api/v1 routes leave it unset.
	queue bool
}

// refreshItemMarkers queues local marker analysis of an item for kinds. A
// movie gets credits only, so a request without credits takes episodes
// only, rejects a movie like any other item that is not an episode, and
// keeps the messages of the endpoints that predate movies. One analysis of
// an item runs at a time; opts decides how a request for kinds it does not
// cover is treated, and whether the detection settings narrow kinds.
func (h *AdminIntroHandler) refreshItemMarkers(ctx context.Context, itemID, action string, kinds intromarkers.EpisodeMarkerKinds, opts localRefreshOptions) (string, error) {
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
	if opts.followSettings {
		if kinds, err = h.enabledItemMarkerKinds(ctx, itemID, eligibility.Kind, kinds); err != nil {
			return "", err
		}
	}

	start, status := h.claimItemRun(itemID, kinds, opts.queue)
	if !start {
		return status, nil
	}

	kind := eligibility.Kind
	go func() {
		for {
			h.runLocalItemAnalysis(ctx, itemID, kind, action, kinds)
			var more bool
			if kinds, more = h.nextItemRun(itemID); !more {
				return
			}
		}
	}()

	return markerRefreshQueued, nil
}

// runLocalItemAnalysis runs local analysis of an item of kind for kinds, logs
// the result, and tells active playback the markers it stored.
func (h *AdminIntroHandler) runLocalItemAnalysis(ctx context.Context, itemID, kind, action string, kinds intromarkers.EpisodeMarkerKinds) {
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
	h.notifyItemMarkerUpdates(h.baseContext, itemID, kind, action, nil)
}

// enabledItemMarkerKinds narrows requested, the kinds asked of an item of
// kind, to the kinds the detection settings leave on. It returns a conflict
// naming the requested kinds when none remain. A movie is only analyzed for
// credits.
func (h *AdminIntroHandler) enabledItemMarkerKinds(ctx context.Context, itemID, kind string, requested intromarkers.EpisodeMarkerKinds) (intromarkers.EpisodeMarkerKinds, error) {
	enabled, err := intromarkers.EnabledMarkerKinds(ctx, h.Settings)
	if err != nil {
		h.logger.ErrorContext(ctx, "admin markers: load detection kinds failed", "item_id", itemID, "error", err)
		return intromarkers.EpisodeMarkerKinds{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load marker settings")
	}
	if kind == intromarkers.MarkerItemMovie {
		requested = intromarkers.EpisodeMarkerKinds{Credits: true}
	}
	if kinds := requested.And(enabled); kinds.Any() {
		return kinds, nil
	}
	return intromarkers.EpisodeMarkerKinds{}, apiError(http.StatusConflict, "conflict", markerKindsOffMessage(requested))
}

// The conflicts of a request whose kinds are all turned off.
const (
	markerKindsOffBoth    = "Intro and credits detection are turned off in marker settings"
	markerKindsOffIntro   = "Intro detection is turned off in marker settings"
	markerKindsOffCredits = "Credits detection is turned off in marker settings"
)

// markerKindsOffMessage names the requested kinds that are turned off.
func markerKindsOffMessage(requested intromarkers.EpisodeMarkerKinds) string {
	switch {
	case requested.Intro && requested.Credits:
		return markerKindsOffBoth
	case requested.Intro:
		return markerKindsOffIntro
	default:
		return markerKindsOffCredits
	}
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

// notifyItemMarkerUpdates tells active playback the markers of an item's
// files as stored, with each file's on-demand online overlay from overlays,
// keyed by file ID, laid over its stored row.
func (h *AdminIntroHandler) notifyItemMarkerUpdates(ctx context.Context, itemID, kind, action string, overlays map[int]*models.MediaFile) {
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
		if file != nil {
			file = markers.OverlayOnline(file, overlays[file.ID])
		}
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
