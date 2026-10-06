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

// PlaybackEpisodeAnalyzer runs local marker analysis for a played episode,
// or for the credits of a played movie.
type PlaybackEpisodeAnalyzer interface {
	AnalyzeEpisodeForPlayback(ctx context.Context, episodeID string, kinds intromarkers.EpisodeMarkerKinds) (intromarkers.RunSummary, error)
	AnalyzeMovieFile(ctx context.Context, fileID int) (intromarkers.RunSummary, error)
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
		if !onlineMarkersOnDemand(ctx, h.SettingsRepo) || (mode != markers.ModeOnline && mode != markers.ModeBoth) {
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

	// localKinds is what local analysis may look for: an episode's intros
	// and credits and a movie's credits, less the kinds turned off
	// server-wide.
	var localKinds intromarkers.EpisodeMarkerKinds
	if shouldRunLocal {
		enabled, err := intromarkers.EnabledMarkerKinds(ctx, h.SettingsRepo)
		if err != nil {
			slog.WarnContext(ctx, "playback lazy markers: load detection kinds failed", "component", "api",
				"session_id", session.ID,
				"file_id", file.ID,
				"episode_id", file.EpisodeID,
				"error", err)
		}
		localKinds = intromarkers.EpisodeMarkerKinds{Intro: isEpisode, Credits: true}.And(enabled)
		shouldRunLocal = err == nil && localKinds.Any()
	}

	if shouldRunLocal {
		// Local analysis runs only in libraries that opted in to it, and
		// needs an analyzer.
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
		if !ok || h.IntroAnalyzer == nil {
			shouldRunLocal = false
		}
	}

	if !shouldRunLocal {
		localKinds = intromarkers.EpisodeMarkerKinds{}
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
		"run_local", shouldRunLocal,
		"local_intro", localKinds.Intro,
		"local_credits", localKinds.Credits)
	go h.runLazyPlaybackMarkers(sessionID, &fileSnapshot, mode, shouldRunOnline, localKinds)
}

// runLazyPlaybackMarkers looks the file's markers up online when runOnline is
// set, then runs local analysis for the kinds of localKinds the file still
// lacks. An empty localKinds runs no local analysis.
func (h *PlaybackHandler) runLazyPlaybackMarkers(
	sessionID string,
	file *models.MediaFile,
	mode markers.Mode,
	runOnline bool,
	localKinds intromarkers.EpisodeMarkerKinds,
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
	isEpisode := strings.TrimSpace(file.EpisodeID) != ""
	localMissing := func(file *models.MediaFile) intromarkers.EpisodeMarkerKinds {
		return missingLocalMarkers(file, isEpisode).And(localKinds)
	}

	slog.Info("playback lazy markers: started",
		"session_id", sessionID,
		"file_id", file.ID,
		"episode_id", file.EpisodeID,
		"mode", mode)

	// overlay is the on-demand online lookup players were sent. Its provider
	// markers are never saved, so each reload of the stored row below gets
	// them laid back over; otherwise the next update would clear a marker the
	// player already shows, and local analysis would be asked for it.
	var overlay *models.MediaFile
	if runOnline {
		onDemand := onlineMarkersOnDemand(ctx, h.SettingsRepo)
		effective, overlaid, err := h.MarkerPopulation.Populate(ctx, file)
		if err != nil {
			slog.WarnContext(ctx, "playback marker lookup failed", "file_id", file.ID, "error", err)
		}
		if effective != nil {
			file = effective
			// In on-demand mode Populate reports an overlay it applied; in
			// stored mode it saved its result, which the reloads read back.
			if onDemand && overlaid {
				overlay = effective
			}
			if hasAnyMarker(file) {
				h.notifyPlaybackMarkers(ctx, sessionID, file, mode)
				if !localMissing(file).Any() {
					return
				}
			}
		}
	}

	// A concurrent session may have populated markers since we queued; check
	// before falling through to the (expensive) local analyzer.
	if refreshed := markers.OverlayOnline(h.reloadPlaybackMarkerFile(ctx, file.ID), overlay); refreshed != nil {
		file = refreshed
		if hasAnyMarker(refreshed) {
			h.notifyPlaybackMarkers(ctx, sessionID, refreshed, mode)
			if !localMissing(refreshed).Any() {
				return
			}
		}
	}

	if kinds := localMissing(file); kinds.Any() {
		slog.Info("playback lazy markers: local analyzer started",
			"session_id", sessionID,
			"file_id", file.ID,
			"episode_id", file.EpisodeID,
			"mode", mode,
			"intro", kinds.Intro,
			"credits", kinds.Credits)
		// A viewer is waiting: take the ffmpeg slot reserved for playback.
		var summary intromarkers.RunSummary
		var err error
		if isEpisode {
			summary, err = h.IntroAnalyzer.AnalyzeEpisodeForPlayback(intromarkers.WithPlaybackPriority(ctx), file.EpisodeID, kinds)
		} else {
			summary, err = h.IntroAnalyzer.AnalyzeMovieFile(intromarkers.WithPlaybackPriority(ctx), file.ID)
		}
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
			"credits_chapter_markers_written", summary.CreditsChapterMarkersWritten,
			"credits_audio_markers_written", summary.CreditsAudioMarkersWritten,
			"credits_audio_video_markers_written", summary.CreditsAudioVideoMarkersWritten,
			"credits_video_markers_written", summary.CreditsVideoMarkersWritten,
			"movie_credits_markers_written", summary.MovieCreditsMarkersWritten,
			"credits_fingerprints_computed", summary.CreditsFingerprintsComputed,
			"credits_tail_scans_computed", summary.CreditsTailScansComputed,
			"errors", len(summary.Errors))

		if refreshed := markers.OverlayOnline(h.reloadPlaybackMarkerFile(ctx, file.ID), overlay); hasAnyMarker(refreshed) {
			h.notifyPlaybackMarkers(ctx, sessionID, refreshed, mode)
		}
	}
}

func (h *PlaybackHandler) hasOnlineMarkerProviders() bool {
	return h != nil && h.MarkerPopulation != nil && h.MarkerRegistry != nil && len(h.MarkerRegistry.Providers()) > 0
}

// onlineMarkersOnDemand reports whether online markers are looked up for each
// playback or refresh and never saved. A setting that cannot be read counts as
// stored, which leaves the stored row as the whole answer.
func onlineMarkersOnDemand(ctx context.Context, settings MarkerSettingsReader) bool {
	if settings == nil {
		return false
	}
	raw, err := settings.Get(ctx, markers.SettingOnlineStorage)
	if err != nil {
		return false
	}
	storage, err := markers.ParseOnlineStorage(raw)
	return err == nil && storage == markers.OnlineStorageOnDemand
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

// missingLocalMarkers returns the marker kinds local analysis could still
// fill for the file. Local analysis finds intros and credits in episodes and
// only credits in movies, so an episode with an intro from any source still
// needs it for credits, and only for credits.
func missingLocalMarkers(file *models.MediaFile, isEpisode bool) intromarkers.EpisodeMarkerKinds {
	if file == nil {
		return intromarkers.EpisodeMarkerKinds{Intro: isEpisode, Credits: true}
	}
	return intromarkers.EpisodeMarkerKinds{
		Intro:   isEpisode && (file.IntroStart == nil || file.IntroEnd == nil),
		Credits: file.CreditsStart == nil || file.CreditsEnd == nil,
	}
}
