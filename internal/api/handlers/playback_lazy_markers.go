package handlers

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/intromarkers"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const playbackLazyMarkerTimeout = 10 * time.Minute

type PlaybackIntroEligibilityChecker interface {
	IntroDetectionEligibleForPlayback(ctx context.Context, fileID int) (bool, error)
	IsFileInEnabledLibrary(ctx context.Context, fileID int) (bool, error)
}

type PlaybackMarkerUpdateNotifier interface {
	MarkersUpdated(ctx context.Context, file *models.MediaFile)
}

func (h *PlaybackHandler) maybeQueueLazyPlaybackMarkers(
	ctx context.Context,
	session *playback.Session,
	file *models.MediaFile,
) {
	if h == nil || session == nil || file == nil || file.ID <= 0 {
		return
	}
	if file.MediaFolderID <= 0 {
		return
	}
	isEpisode := strings.TrimSpace(file.EpisodeID) != ""
	isMovie := !isEpisode && strings.TrimSpace(file.ContentID) != ""
	if !isEpisode && !isMovie {
		return
	}
	if h.SettingsRepo == nil || h.IntroRepository == nil {
		return
	}

	lazy, err := h.SettingsRepo.Get(ctx, markers.SettingLazyPlayback)
	if err != nil {
		slog.WarnContext(ctx, "playback lazy markers: load lazy setting failed", "component", "api",
			"session_id", session.ID,
			"file_id", file.ID,
			"episode_id", file.EpisodeID,
			"error", err)
		return
	}

	rawMode, err := h.SettingsRepo.Get(ctx, markers.SettingMode)
	if err != nil {
		slog.WarnContext(ctx, "playback lazy markers: load marker mode failed", "component", "api",
			"session_id", session.ID,
			"file_id", file.ID,
			"episode_id", file.EpisodeID,
			"error", err)
		return
	}
	mode := markers.NormalizeMode(rawMode)
	lazyEnabled := strings.EqualFold(strings.TrimSpace(lazy), "true")
	if !lazyEnabled {
		storage, err := h.SettingsRepo.Get(ctx, markers.SettingOnlineStorage)
		if err != nil || storage != "on_demand" || (mode != markers.ModeOnline && mode != markers.ModeBoth) {
			return
		}
	}
	if mode == markers.ModeOff {
		slog.DebugContext(ctx, "playback lazy markers: skipped; marker mode is off", "component", "api",
			"session_id", session.ID,
			"file_id", file.ID,
			"episode_id", file.EpisodeID)
		return
	}

	hasOnline := h.hasOnlineMarkerProviders()
	shouldRunLocal := lazyEnabled && markers.ShouldRunLocal(mode)
	shouldRunOnline := (mode == markers.ModeOnline || mode == markers.ModeBoth) && hasOnline

	if shouldRunOnline {
		// Online providers work for any enabled library (movies and series alike).
		ok, err := h.IntroRepository.IsFileInEnabledLibrary(ctx, file.ID)
		if err != nil {
			slog.WarnContext(ctx, "playback lazy markers: online eligibility check failed", "component", "api",
				"session_id", session.ID,
				"file_id", file.ID,
				"error", err)
			shouldRunOnline = false
		}
		if !ok {
			shouldRunOnline = false
		}
	}

	if shouldRunLocal {
		// Local chromaprint is only meaningful for series libraries that
		// opted in to expensive fingerprinting and requires an analyzer.
		ok, err := h.IntroRepository.IntroDetectionEligibleForPlayback(ctx, file.ID)
		if err != nil {
			slog.WarnContext(ctx, "playback lazy markers: local eligibility check failed", "component", "api",
				"session_id", session.ID,
				"file_id", file.ID,
				"episode_id", file.EpisodeID,
				"mode", mode,
				"error", err)
			shouldRunLocal = false
		}
		if !ok || h.IntroAnalyzer == nil || !isEpisode {
			shouldRunLocal = false
		}
	}

	if !shouldRunOnline && !shouldRunLocal {
		slog.DebugContext(ctx, "playback lazy markers: skipped; no eligible detection path", "component", "api",
			"session_id", session.ID,
			"file_id", file.ID,
			"episode_id", file.EpisodeID,
			"mode", mode)
		return
	}

	if _, loaded := h.MarkerLazyInFlight.LoadOrStore(file.ID, struct{}{}); loaded {
		return
	}

	sessionID := session.ID
	fileSnapshot := *file
	slog.InfoContext(ctx, "playback lazy markers: queued", "component", "api",
		"session_id", sessionID,
		"file_id", file.ID,
		"episode_id", file.EpisodeID,
		"mode", mode,
		"run_online", shouldRunOnline,
		"run_local", shouldRunLocal)
	go h.runLazyPlaybackMarkers(sessionID, &fileSnapshot, mode, shouldRunOnline, shouldRunLocal)
}

func (h *PlaybackHandler) runLazyPlaybackMarkers(
	sessionID string,
	file *models.MediaFile,
	mode markers.Mode,
	runOnline bool,
	runLocal bool,
) {
	if file == nil {
		return
	}
	defer h.MarkerLazyInFlight.Delete(file.ID)

	base := h.MarkerLazyContext
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, playbackLazyMarkerTimeout)
	defer cancel()

	slog.Info("playback lazy markers: started",
		"session_id", sessionID,
		"file_id", file.ID,
		"episode_id", file.EpisodeID,
		"mode", mode)

	if runOnline {
		effective, _, err := h.MarkerPopulation.Populate(ctx, file)
		if err != nil {
			slog.WarnContext(ctx, "playback marker lookup failed", "file_id", file.ID, "error", err)
		}
		if effective != nil {
			file = effective
			if hasAnyMarker(file) {
				h.notifyPlaybackMarkers(ctx, sessionID, file, mode)
				if !runLocal || hasLocalDetectionMarkers(file) {
					return
				}
			}
		}
	}

	// A concurrent session may have populated markers since we queued; check
	// before falling through to the (expensive) local analyzer.
	if refreshed := h.reloadPlaybackMarkerFile(ctx, file.ID); hasAnyMarker(refreshed) {
		h.notifyPlaybackMarkers(ctx, sessionID, refreshed, mode)
		if !runLocal || hasLocalDetectionMarkers(refreshed) {
			return
		}
	}

	if runLocal {
		slog.Info("playback lazy markers: local analyzer started",
			"session_id", sessionID,
			"file_id", file.ID,
			"episode_id", file.EpisodeID,
			"mode", mode)
		// A viewer is waiting: take the ffmpeg slot reserved for playback.
		summary, err := h.IntroAnalyzer.AnalyzeEpisode(intromarkers.WithPlaybackPriority(ctx), file.EpisodeID)
		if err != nil {
			slog.Warn("playback lazy markers: local analyzer failed",
				"session_id", sessionID,
				"file_id", file.ID,
				"episode_id", file.EpisodeID,
				"mode", mode,
				"error", err)
			return
		}
		slog.Info("playback lazy markers: local analyzer finished",
			"session_id", sessionID,
			"file_id", file.ID,
			"episode_id", file.EpisodeID,
			"mode", mode,
			"files_considered", summary.FilesConsidered,
			"season_groups_considered", summary.SeasonGroupsConsidered,
			"chapter_markers_written", summary.ChapterMarkersWritten,
			"chromaprint_markers_written", summary.ChromaprintMarkersWritten,
			"fingerprint_cache_hits", summary.FingerprintCacheHits,
			"fingerprints_computed", summary.FingerprintsComputed,
			"errors", len(summary.Errors))

		if refreshed := h.reloadPlaybackMarkerFile(ctx, file.ID); hasAnyMarker(refreshed) {
			h.notifyPlaybackMarkers(ctx, sessionID, refreshed, mode)
		}
	}
}

func (h *PlaybackHandler) hasOnlineMarkerProviders() bool {
	return h != nil && h.MarkerPopulation != nil && h.MarkerRegistry != nil && len(h.MarkerRegistry.Providers()) > 0
}

func (h *PlaybackHandler) reloadPlaybackMarkerFile(ctx context.Context, fileID int) *models.MediaFile {
	if h == nil || h.fileResolver == nil || fileID <= 0 {
		return nil
	}
	refreshed, err := h.fileResolver.GetByID(ctx, fileID)
	if err != nil {
		slog.WarnContext(ctx, "playback lazy markers: reload file failed", "component", "api", "file_id", fileID, "error", err)
		return nil
	}
	return refreshed
}

func (h *PlaybackHandler) notifyPlaybackMarkers(
	ctx context.Context,
	sessionID string,
	file *models.MediaFile,
	mode markers.Mode,
) {
	if h == nil || h.MarkerUpdateNotifier == nil || file == nil {
		return
	}
	h.MarkerUpdateNotifier.MarkersUpdated(ctx, file)
	slog.InfoContext(ctx, "playback lazy markers: emitted marker update", "component", "api",
		"session_id", sessionID,
		"file_id", file.ID,
		"episode_id", file.EpisodeID,
		"mode", mode)
}

// hasAnyMarker reports whether the file has at least one populated marker
// segment. Used to decide whether to emit a markers_updated event.
func hasAnyMarker(file *models.MediaFile) bool {
	if file == nil {
		return false
	}
	return (file.IntroStart != nil && file.IntroEnd != nil) ||
		(file.CreditsStart != nil && file.CreditsEnd != nil) ||
		(file.RecapStart != nil && file.RecapEnd != nil) ||
		(file.PreviewStart != nil && file.PreviewEnd != nil)
}

func hasLocalDetectionMarkers(file *models.MediaFile) bool {
	if file == nil {
		return false
	}
	return (file.IntroStart != nil && file.IntroEnd != nil) ||
		(file.CreditsStart != nil && file.CreditsEnd != nil)
}
