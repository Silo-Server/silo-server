package handlers

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
)

type MarkerRefreshService interface {
	Refresh(context.Context, *models.MediaFile) (*models.MediaFile, bool, error)
}

const (
	markerRefreshQueued         = "queued"
	markerRefreshAlreadyRunning = "already_running"
)

func (h *AdminIntroHandler) refreshEpisodeMarkersV2(ctx context.Context, episodeID string) (string, error) {
	if h == nil || h.Settings == nil || h.FileResolver == nil {
		return "", apiError(http.StatusServiceUnavailable, "unavailable", "Marker refresh is not configured")
	}
	raw, err := h.Settings.Get(ctx, markers.SettingMode)
	if err != nil {
		return "", apiError(http.StatusInternalServerError, "internal_error", "Failed to load marker settings")
	}
	mode := markers.NormalizeMode(raw)
	if mode == markers.ModeOff {
		return "", apiError(http.StatusConflict, "conflict", "Marker detection is disabled")
	}
	if mode == markers.ModeLocal {
		return h.RefreshEpisodeMarkers(ctx, episodeID, "refresh")
	}
	if h.OnlineMarkers == nil {
		return "", apiError(http.StatusServiceUnavailable, "unavailable", "Online markers are not configured")
	}
	files, err := h.FileResolver.GetByEpisodeID(ctx, episodeID)
	if err != nil {
		return "", apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve episode files")
	}
	if len(files) == 0 {
		return "", apiError(http.StatusConflict, "conflict", "Episode has no media files to refresh")
	}
	local := false
	if mode == markers.ModeBoth && h.analyzer != nil && h.eligibility != nil {
		eligibility, err := h.eligibility.EpisodeIntroEligibility(ctx, episodeID)
		if err != nil {
			return "", apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve marker eligibility")
		}
		local = eligibility.IntroDetectionEnabled
	}
	if _, loaded := h.inFlight.LoadOrStore(episodeID, struct{}{}); loaded {
		return markerRefreshAlreadyRunning, nil
	}
	go func() {
		defer h.inFlight.Delete(episodeID)
		ctx, cancel := context.WithTimeout(h.baseContext, playbackLazyMarkerTimeout)
		defer cancel()
		needsLocal := false
		for _, file := range files {
			if file == nil || ctx.Err() != nil {
				continue
			}
			effective, _, err := h.OnlineMarkers.Refresh(ctx, file)
			if err != nil {
				h.logger.WarnContext(ctx, "online marker refresh failed", "file_id", file.ID, "error", err)
			}
			if effective == nil || effective.IntroEnd == nil || effective.CreditsStart == nil {
				needsLocal = true
			}
		}
		if local && needsLocal && ctx.Err() == nil {
			if _, err := h.analyzer.AnalyzeEpisode(ctx, episodeID); err != nil {
				h.logger.WarnContext(ctx, "local marker refresh failed", "episode_id", episodeID, "error", err)
			}
			h.notifyEpisodeMarkerUpdates(ctx, episodeID, "refresh")
		}
	}()
	return markerRefreshQueued, nil
}
