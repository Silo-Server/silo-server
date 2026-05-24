package handlers

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

const playbackLazyMarkerTimeout = 10 * time.Minute

type PlaybackIntroEligibilityChecker interface {
	IntroDetectionEligibleForPlayback(ctx context.Context, fileID int) (bool, error)
	IsFileInEnabledLibrary(ctx context.Context, fileID int) (bool, error)
}

type PlaybackMarkerUpdateNotifier interface {
	MarkersUpdated(ctx context.Context, file *models.MediaFile)
}

// PlaybackMarkerUpserter narrows scanner.FileRepository down to just the
// marker write path so tests can supply a fake without dragging in the
// full repository.
type PlaybackMarkerUpserter interface {
	UpsertMarkers(ctx context.Context, fileID int, update scanner.MarkerUpdate) (bool, error)
}

func (h *PlaybackHandler) maybeQueueLazyPlaybackMarkers(
	ctx context.Context,
	session *playback.Session,
	file *models.MediaFile,
) {
	if h == nil || session == nil || file == nil || file.ID <= 0 {
		return
	}
	if hasOnlineSourcedMarkers(file) {
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
		slog.Warn("playback lazy markers: load lazy setting failed",
			"session_id", session.ID,
			"file_id", file.ID,
			"episode_id", file.EpisodeID,
			"error", err)
		return
	}
	if strings.ToLower(strings.TrimSpace(lazy)) != "true" {
		return
	}

	rawMode, err := h.SettingsRepo.Get(ctx, markers.SettingMode)
	if err != nil {
		slog.Warn("playback lazy markers: load marker mode failed",
			"session_id", session.ID,
			"file_id", file.ID,
			"episode_id", file.EpisodeID,
			"error", err)
		return
	}
	mode := markers.NormalizeMode(rawMode)
	if mode == markers.ModeOff {
		slog.Debug("playback lazy markers: skipped; marker mode is off",
			"session_id", session.ID,
			"file_id", file.ID,
			"episode_id", file.EpisodeID)
		return
	}

	hasOnline := h.hasOnlineMarkerProviders()
	shouldRunLocal := markers.ShouldRunLocal(mode)
	shouldRunOnline := (mode == markers.ModeOnline || mode == markers.ModeBoth) && hasOnline

	if shouldRunOnline {
		// Online providers work for any enabled library (movies and series alike).
		ok, err := h.IntroRepository.IsFileInEnabledLibrary(ctx, file.ID)
		if err != nil {
			slog.Warn("playback lazy markers: online eligibility check failed",
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
			slog.Warn("playback lazy markers: local eligibility check failed",
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
		slog.Debug("playback lazy markers: skipped; no eligible detection path",
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
	slog.Info("playback lazy markers: queued",
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
		wrote, err := h.fetchOnlineMarkersForPlayback(ctx, file)
		if err != nil {
			slog.Warn("playback lazy markers: online fetch failed",
				"session_id", sessionID,
				"file_id", file.ID,
				"episode_id", file.EpisodeID,
				"mode", mode,
				"error", err)
		}
		if wrote {
			if refreshed := h.reloadPlaybackMarkerFile(ctx, file.ID); hasAnyMarker(refreshed) {
				h.notifyPlaybackMarkers(ctx, sessionID, refreshed, mode)
				return
			}
		}
	}

	// A concurrent session may have populated markers since we queued; check
	// before falling through to the (expensive) local analyzer.
	if refreshed := h.reloadPlaybackMarkerFile(ctx, file.ID); hasAnyMarker(refreshed) {
		h.notifyPlaybackMarkers(ctx, sessionID, refreshed, mode)
		return
	}

	if runLocal {
		slog.Info("playback lazy markers: local analyzer started",
			"session_id", sessionID,
			"file_id", file.ID,
			"episode_id", file.EpisodeID,
			"mode", mode)
		summary, err := h.IntroAnalyzer.AnalyzeEpisode(ctx, file.EpisodeID)
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
	return h != nil && h.MarkerRegistry != nil && len(h.MarkerRegistry.Providers()) > 0
}

// fetchOnlineMarkersForPlayback resolves external IDs for the given file,
// asks the marker registry for the first hit, and persists the result via
// the scanner upserter. Returns true when at least one segment was
// actually written to storage.
func (h *PlaybackHandler) fetchOnlineMarkersForPlayback(ctx context.Context, file *models.MediaFile) (bool, error) {
	if h == nil || file == nil || !h.hasOnlineMarkerProviders() {
		return false, nil
	}
	if h.MarkerResolver == nil || h.MarkerUpserter == nil {
		slog.Debug("playback lazy markers: online fetch skipped; resolver or upserter missing",
			"file_id", file.ID)
		return false, nil
	}

	ids, err := h.MarkerResolver.ResolveForFile(ctx, file)
	if err != nil {
		return false, err
	}
	if !ids.HasAnyID() {
		slog.Debug("playback lazy markers: online fetch skipped; no external IDs available",
			"file_id", file.ID,
			"episode_id", file.EpisodeID,
			"content_id", file.ContentID)
		return false, nil
	}

	req := markers.Request{
		Kind:          ids.Kind,
		ExternalIDs:   ids.AsRequestMap(),
		SeasonNumber:  ids.SeasonNumber,
		EpisodeNumber: ids.EpisodeNumber,
		Duration:      time.Duration(file.Duration) * time.Second,
	}
	result, ok, err := h.MarkerRegistry.FetchFirstHit(ctx, req)
	if err != nil {
		return false, err
	}
	if !ok || len(result.Markers) == 0 {
		return false, nil
	}

	payload := markers.BuildUpdatePayload(result)
	if !payload.HasAnySegment() {
		return false, nil
	}
	wrote, err := h.MarkerUpserter.UpsertMarkers(ctx, file.ID, markerUpdateFromPayload(payload))
	if err != nil {
		return false, err
	}
	return wrote, nil
}

func (h *PlaybackHandler) reloadPlaybackMarkerFile(ctx context.Context, fileID int) *models.MediaFile {
	if h == nil || h.fileResolver == nil || fileID <= 0 {
		return nil
	}
	refreshed, err := h.fileResolver.GetByID(ctx, fileID)
	if err != nil {
		slog.Warn("playback lazy markers: reload file failed", "file_id", fileID, "error", err)
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
	slog.Info("playback lazy markers: emitted marker update",
		"session_id", sessionID,
		"file_id", file.ID,
		"episode_id", file.EpisodeID,
		"mode", mode)
}

// hasOnlineSourcedMarkers reports whether the file already has at least one
// marker written by a non-local source. We short-circuit lazy online fetch
// in that case to avoid refetching every playback start — TheIntroDB often
// returns only intro+credits and never recap/preview for a given episode, so
// requiring all four kinds before skipping would loop forever on partial data.
// Markers from the scanner/s3 path remain refetchable since online sources
// outrank them.
func hasOnlineSourcedMarkers(file *models.MediaFile) bool {
	if file == nil {
		return false
	}
	isOnlineSource := func(source *string) bool {
		if source == nil {
			return false
		}
		switch *source {
		case models.MarkerSourceOnline, models.MarkerSourcePlugin, models.MarkerSourceManual:
			return true
		}
		return false
	}
	return isOnlineSource(file.IntroMarkersSource) ||
		isOnlineSource(file.CreditsMarkersSource) ||
		isOnlineSource(file.RecapMarkersSource) ||
		isOnlineSource(file.PreviewMarkersSource)
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

// markerUpdateFromPayload adapts the generic markers.MarkerUpdatePayload to
// the scanner.MarkerUpdate shape consumed by FileRepository.UpsertMarkers.
// Kept narrow on purpose — the packages it bridges should not need to import
// each other.
func markerUpdateFromPayload(p markers.MarkerUpdatePayload) scanner.MarkerUpdate {
	return scanner.MarkerUpdate{
		IntroStart:        p.IntroStart,
		IntroEnd:          p.IntroEnd,
		CreditsStart:      p.CreditsStart,
		CreditsEnd:        p.CreditsEnd,
		RecapStart:        p.RecapStart,
		RecapEnd:          p.RecapEnd,
		PreviewStart:      p.PreviewStart,
		PreviewEnd:        p.PreviewEnd,
		MarkersSource:     p.Source,
		MarkersProvider:   p.Provider,
		MarkersConfidence: p.Confidence,
		MarkersAlgorithm:  p.Algorithm,
	}
}
