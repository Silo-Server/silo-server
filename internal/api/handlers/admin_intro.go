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

type IntroEpisodeAnalyzer interface {
	AnalyzeEpisode(ctx context.Context, episodeID string) (intromarkers.RunSummary, error)
}

type IntroEpisodeEligibilityChecker interface {
	EpisodeIntroEligibility(ctx context.Context, episodeID string) (*intromarkers.EpisodeIntroEligibility, error)
}

type MarkerSettingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}

type AdminIntroFileResolver interface {
	GetByEpisodeID(ctx context.Context, episodeID string) ([]*models.MediaFile, error)
}

type AdminIntroHandler struct {
	analyzer             IntroEpisodeAnalyzer
	eligibility          IntroEpisodeEligibilityChecker
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
	eligibility IntroEpisodeEligibilityChecker,
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

func (h *AdminIntroHandler) handleEpisodeMarkers(w http.ResponseWriter, r *http.Request, action string) {
	status, err := h.RefreshEpisodeMarkers(r.Context(), chi.URLParam(r, "id"), action)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, redetectIntroResponse{Status: status})
}

func (h *AdminIntroHandler) RefreshEpisodeMarkers(ctx context.Context, episodeID, action string) (string, error) {
	if action == "refresh-v2" {
		return h.refreshEpisodeMarkersV2(ctx, episodeID)
	}
	if h == nil || h.analyzer == nil || h.eligibility == nil {
		return "", apiError(http.StatusServiceUnavailable, "unavailable", "Intro detection is not configured")
	}

	if episodeID == "" {
		return "", apiError(http.StatusBadRequest, "bad_request", "Item ID is required")
	}

	eligibility, err := h.eligibility.EpisodeIntroEligibility(ctx, episodeID)
	if err != nil {
		if errors.Is(err, intromarkers.ErrEpisodeNotFound) {
			return "", apiError(http.StatusBadRequest, "bad_request", "Item must be an episode")
		}
		h.logger.ErrorContext(ctx, "admin intro: resolve episode failed", "episode_id", episodeID, "error", err)
		return "", apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve episode")
	}
	if !eligibility.HasMediaFiles {
		return "", apiError(http.StatusConflict, "conflict", "Episode has no media files to analyze")
	}
	if !eligibility.IntroDetectionEnabled {
		return "", apiError(http.StatusConflict, "conflict", "Intro detection is disabled for this episode's library")
	}
	if h.Settings == nil {
		return "", apiError(http.StatusServiceUnavailable, "unavailable", "Marker settings are not configured")
	}
	raw, err := h.Settings.Get(ctx, markers.SettingMode)
	if err != nil {
		h.logger.ErrorContext(ctx, "admin markers: load mode failed", "episode_id", episodeID, "error", err)
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

	if _, loaded := h.inFlight.LoadOrStore(episodeID, struct{}{}); loaded {
		return markerRefreshAlreadyRunning, nil
	}

	go func() {
		defer h.inFlight.Delete(episodeID)
		start := time.Now()
		h.logger.InfoContext(ctx, "admin markers: episode refresh started", "episode_id", episodeID, "action", action)
		summary, err := h.analyzer.AnalyzeEpisode(h.baseContext, episodeID)
		if err != nil {
			h.logger.ErrorContext(ctx, "admin markers: episode refresh failed",
				"episode_id", episodeID,
				"action", action,
				"duration", time.Since(start),
				"error", err)
			return
		}
		h.logger.InfoContext(ctx, "admin markers: episode refresh finished",
			"episode_id", episodeID,
			"action", action,
			"duration", time.Since(start),
			"files_considered", summary.FilesConsidered,
			"season_groups_considered", summary.SeasonGroupsConsidered,
			"chapter_markers_written", summary.ChapterMarkersWritten,
			"chromaprint_markers_written", summary.ChromaprintMarkersWritten,
			"fingerprint_cache_hits", summary.FingerprintCacheHits,
			"fingerprints_computed", summary.FingerprintsComputed,
			"errors", len(summary.Errors))
		h.notifyEpisodeMarkerUpdates(h.baseContext, episodeID, action)
	}()

	return markerRefreshQueued, nil
}

func (h *AdminIntroHandler) notifyEpisodeMarkerUpdates(ctx context.Context, episodeID, action string) {
	if h == nil || h.FileResolver == nil || h.MarkerUpdateNotifier == nil {
		return
	}
	files, err := h.FileResolver.GetByEpisodeID(ctx, episodeID)
	if err != nil {
		h.logger.WarnContext(ctx, "admin markers: reload episode files for marker update failed",
			"episode_id", episodeID,
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
			"episode_id", episodeID,
			"action", action,
			"file_id", file.ID)
	}
}

func hasAnyPlaybackMarker(file *models.MediaFile) bool {
	return file != nil &&
		((file.IntroStart != nil && file.IntroEnd != nil) ||
			(file.CreditsStart != nil && file.CreditsEnd != nil))
}
