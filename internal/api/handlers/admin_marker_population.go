package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/intromarkers"
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

// refreshEpisodeMarkersV2 refreshes an episode's or a movie's markers from
// the configured sources. In both mode, local analysis then fills what the
// online sources left missing: an episode's intro or credits, or a movie's
// credits, for the kinds the detection settings leave on.
func (h *AdminIntroHandler) refreshEpisodeMarkersV2(ctx context.Context, itemID string) (string, error) {
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
		return h.refreshItemMarkers(ctx, itemID, "refresh", allMarkerKinds, localRefreshOptions{followSettings: true, queue: true})
	}
	if h.OnlineMarkers == nil {
		return "", apiError(http.StatusServiceUnavailable, "unavailable", "Online markers are not configured")
	}
	// Eligibility settles whether the item is a movie; anything else is
	// looked up as an episode, so an item that is neither has no files.
	var eligibility *intromarkers.MarkerItemEligibility
	if h.eligibility != nil {
		resolved, err := h.eligibility.MarkerItemEligibility(ctx, itemID)
		switch {
		case errors.Is(err, intromarkers.ErrMarkerItemNotFound):
		case err != nil:
			return "", apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve marker eligibility")
		default:
			eligibility = resolved
		}
	}
	kind := intromarkers.MarkerItemEpisode
	if eligibility != nil && eligibility.Kind == intromarkers.MarkerItemMovie {
		kind = intromarkers.MarkerItemMovie
	}
	files, err := h.itemFiles(ctx, itemID, kind)
	if err != nil {
		return "", apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve item files")
	}
	if len(files) == 0 {
		return "", apiError(http.StatusConflict, "conflict", "Item has no media files to refresh")
	}
	// localKinds is what local analysis may fill after the online refresh.
	// The online refresh does not depend on the detection settings, so
	// failing to read them only skips local analysis.
	var localKinds intromarkers.EpisodeMarkerKinds
	if mode == markers.ModeBoth && h.analyzer != nil && eligibility != nil && eligibility.IntroDetectionEnabled {
		enabled, err := intromarkers.EnabledMarkerKinds(ctx, h.Settings)
		if err != nil {
			h.logger.WarnContext(ctx, "admin markers: load detection kinds failed; skipping local analysis", "item_id", itemID, "error", err)
		}
		localKinds = intromarkers.EpisodeMarkerKinds{Intro: kind != intromarkers.MarkerItemMovie, Credits: true}.And(enabled)
	}
	// An online refresh neither queues behind other work on the item nor
	// lets other work queue behind it.
	if start, status := h.claimItemRun(itemID, allMarkerKinds, false); !start {
		return status, nil
	}
	go func() {
		// Local requests are not queued behind an online refresh, which
		// claims every kind, so there is never work to run next.
		defer h.nextItemRun(itemID)
		ctx, cancel := context.WithTimeout(h.baseContext, playbackLazyMarkerTimeout)
		defer cancel()
		needsLocal := false
		// On-demand online markers are never saved, so the reload after
		// local analysis lacks them; keep each file's overlay to lay back
		// over it, as lazy playback does.
		onDemand := onlineMarkersOnDemand(ctx, h.Settings)
		overlays := make(map[int]*models.MediaFile, len(files))
		for _, file := range files {
			if file == nil || ctx.Err() != nil {
				continue
			}
			effective, changed, err := h.OnlineMarkers.Refresh(ctx, file)
			if err != nil {
				h.logger.WarnContext(ctx, "online marker refresh failed", "file_id", file.ID, "error", err)
			}
			if onDemand && changed && effective != nil {
				overlays[effective.ID] = effective
			}
			// Tell active playback of what the online sources changed now,
			// whether or not local analysis follows.
			if changed && effective != nil && h.MarkerUpdateNotifier != nil {
				h.MarkerUpdateNotifier.MarkersUpdated(ctx, effective)
			}
			if missingLocalMarkers(effective, kind != intromarkers.MarkerItemMovie).And(localKinds).Any() {
				needsLocal = true
			}
		}
		if needsLocal && ctx.Err() == nil {
			if _, err := h.analyzeItem(ctx, itemID, kind, localKinds); err != nil {
				h.logger.WarnContext(ctx, "local marker refresh failed", "item_id", itemID, "kind", kind, "error", err)
			}
			h.notifyItemMarkerUpdates(ctx, itemID, kind, "refresh", overlays)
		}
	}()
	return markerRefreshQueued, nil
}
