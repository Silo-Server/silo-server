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
// credits.
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
		return h.refreshItemMarkers(ctx, itemID, "refresh", allMarkerKinds)
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
	local := mode == markers.ModeBoth && h.analyzer != nil && eligibility != nil && eligibility.IntroDetectionEnabled
	if _, loaded := h.inFlight.LoadOrStore(itemID, struct{}{}); loaded {
		return markerRefreshAlreadyRunning, nil
	}
	go func() {
		defer h.inFlight.Delete(itemID)
		ctx, cancel := context.WithTimeout(h.baseContext, playbackLazyMarkerTimeout)
		defer cancel()
		needsLocal := false
		for _, file := range files {
			if file == nil || ctx.Err() != nil {
				continue
			}
			effective, changed, err := h.OnlineMarkers.Refresh(ctx, file)
			if err != nil {
				h.logger.WarnContext(ctx, "online marker refresh failed", "file_id", file.ID, "error", err)
			}
			// Tell active playback of what the online sources changed now,
			// whether or not local analysis follows.
			if changed && effective != nil && h.MarkerUpdateNotifier != nil {
				h.MarkerUpdateNotifier.MarkersUpdated(ctx, effective)
			}
			if localMarkersMissing(effective, kind) {
				needsLocal = true
			}
		}
		if local && needsLocal && ctx.Err() == nil {
			if _, err := h.analyzeItem(ctx, itemID, kind, allMarkerKinds); err != nil {
				h.logger.WarnContext(ctx, "local marker refresh failed", "item_id", itemID, "kind", kind, "error", err)
			}
			h.notifyItemMarkerUpdates(ctx, itemID, kind, "refresh")
		}
	}()
	return markerRefreshQueued, nil
}

// localMarkersMissing reports whether local analysis could fill a marker the
// refreshed file lacks: an episode's intro or credits, or a movie's credits.
func localMarkersMissing(file *models.MediaFile, kind string) bool {
	if file == nil {
		return true
	}
	if kind == intromarkers.MarkerItemMovie {
		return file.CreditsStart == nil
	}
	return file.IntroEnd == nil || file.CreditsStart == nil
}
