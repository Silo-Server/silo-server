package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/config"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// FilePathResolver looks up a media file by its ID.
type FilePathResolver interface {
	GetByID(ctx context.Context, id int) (*models.MediaFile, error)
}

// StreamHandler handles HTTP endpoints for streaming media content.
type StreamHandler struct {
	sessionMgr    SessionManagerInterface
	fileResolver  FilePathResolver
	MissingMarker MissingFileMarker
	EventsHub     *evt.Hub
	AdminStore    PlaybackAdminStore
	SessionSyncer PlaybackSessionSyncer
	// TM is the shared transcode/reconstruct manager (same instance as the
	// PlaybackHandler's). It lets a direct/remux stream rebuild its playback
	// Session from the recipe card after a server restart instead of 404-ing.
	// May be nil (tests / minimal setups) — reconstruct is then simply off.
	TM *playback.TranscodeManager
	// JWTSecret verifies the stream token carried on the serve URL (?st=), which
	// is the reconstruction descriptor for direct/remux after a restart. Empty
	// disables token-based reconstruct (tests / minimal setups).
	JWTSecret string
	// PlaybackConfig returns the current playback config; read it through
	// ffmpegPath(). May be nil (tests).
	PlaybackConfig func() config.PlaybackConfig
	// SubtitleCache stores full-track PGS (.sup) extracts under the transcode
	// dir so repeat selections skip the whole-file ffmpeg demux. May be nil
	// (tests / minimal setups) — extraction then always streams uncached.
	SubtitleCache *playback.SubtitleCache
	SubtitleRepo  subtitles.Repository // optional; enables S3-sourced subtitles
	S3Client      subtitles.S3Client   // optional; needed for fetching S3 subtitles
	S3Bucket      string               // bucket for subtitle storage
}

// ffmpegPath returns the currently configured ffmpeg binary path.
func (h *StreamHandler) ffmpegPath() string {
	if h.PlaybackConfig != nil {
		return h.PlaybackConfig().FFmpegPath
	}
	return ""
}

// NewStreamHandler creates a new StreamHandler backed by the given session
// manager and file resolver.
func NewStreamHandler(sessionMgr SessionManagerInterface, fileResolver FilePathResolver) *StreamHandler {
	return &StreamHandler{
		sessionMgr:   sessionMgr,
		fileResolver: fileResolver,
		// A bare manager (no recipe store) behaves as "no reconstruct" — plain
		// GetSession + ownership — so HandleStream has a single code path. The
		// router overwrites this with the shared manager to enable reconstruct.
		TM: playback.NewTranscodeManager(),
	}
}

// HandleStream serves the video stream for a playback session.
// For direct play: serves the file with HTTP byte-range support.
// For remux: starts an ffmpeg remux and streams the output.
// For transcode: returns 400 (transcode uses manifest/segment endpoints).
func (h *StreamHandler) HandleStream(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	sessionID := chi.URLParam(r, "session_id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Session ID is required")
		return
	}
	setPlaybackSessionLogContext(r, sessionID)

	// Look up the session, reconstructing it from the recipe card on a not-found
	// miss (e.g. after a server restart) so a direct/remux stream resumes instead
	// of 404-ing. The client re-supplies its position (HTTP Range for direct, the
	// ?seek= query for remux), so no runtime beyond the Session needs rebuilding.
	// Without a token (or signing secret) reconstruct is off, collapsing to a
	// plain GetSession + ownership check.
	card := streamCardFromToken(r.URL.Query().Get(streamTokenParam), sessionID, h.JWTSecret)
	session, status := h.TM.LoadOrReconstructSession(r.Context(), h.sessionMgr.GetSession, sessionID, userID, card)
	switch status {
	case playback.SessionMissing:
		writePlaybackSessionNotFound(w)
		return
	case playback.SessionLoadFailed:
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load playback session")
		return
	case playback.SessionForbidden:
		writeError(w, http.StatusForbidden, "forbidden", "Session belongs to another user")
		return
	}

	file, err := h.fileResolver.GetByID(r.Context(), session.MediaFileID)
	if err != nil {
		if isPlaybackFileLookupMissing(err) {
			h.abortPlaybackSession(r.Context(), session)
			writeError(w, http.StatusNotFound, "not_found", "Media file not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load media file")
		return
	}
	if file == nil {
		h.abortPlaybackSession(r.Context(), session)
		writeError(w, http.StatusNotFound, "not_found", "Media file not found")
		return
	}
	if err := preflightPlaybackFile(r.Context(), file, h.MissingMarker, h.EventsHub); err != nil {
		if isPlaybackFileMissing(err) {
			h.abortPlaybackSession(r.Context(), session)
		}
		writePlaybackFilePreflightError(w, err)
		return
	}

	switch session.PlayMethod {
	case playback.PlayDirect:
		if err := h.sessionMgr.BeginTransport(sessionID); err == nil {
			defer func() {
				_ = h.sessionMgr.EndTransport(sessionID)
			}()
		}
		if err := playback.ServeDirectPlay(w, r, file.FilePath); err != nil {
			h.handleTransportStartFailure(r.Context(), session, file, err)
		}

	case playback.PlayRemux:
		if err := h.sessionMgr.BeginTransport(sessionID); err == nil {
			defer func() {
				_ = h.sessionMgr.EndTransport(sessionID)
			}()
		}
		seekSeconds := 0.0
		if seekStr := r.URL.Query().Get("seek"); seekStr != "" {
			if s, err := strconv.ParseFloat(seekStr, 64); err == nil && s >= 0 {
				seekSeconds = s
			}
		}
		if err := playback.ServeRemuxWithDVMode(w, r, file.FilePath, "mp4", seekSeconds, session.TranscodeAudio, session.AudioTrackIndex, file.PrimaryDVProfile(), session.RemuxDVMode, h.ffmpegPath()); err != nil {
			h.handleTransportStartFailure(r.Context(), session, file, err)
		}

	case playback.PlayTranscode:
		writeError(w, http.StatusBadRequest, "bad_request",
			"Transcode streams use manifest/segment endpoints")

	default:
		writeError(w, http.StatusInternalServerError, "internal_error",
			"Unknown play method")
	}
}

// HandleSubtitle extracts a subtitle track from the media file associated with
// a playback session and serves it as WebVTT or raw ASS depending on the
// URL extension (e.g. /subtitles/2.ass or /subtitles/2.vtt).
func (h *StreamHandler) HandleSubtitle(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	sessionID := chi.URLParam(r, "session_id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Session ID is required")
		return
	}
	setPlaybackSessionLogContext(r, sessionID)

	trackParam := chi.URLParam(r, "track")
	trackIndex, requestedFormat, err := playback.ParseSubtitleTrackParam(trackParam)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid subtitle track index")
		return
	}

	session, err := h.sessionMgr.GetSession(sessionID)
	if err != nil {
		writePlaybackSessionNotFound(w)
		return
	}

	if session.UserID != userID {
		writeError(w, http.StatusForbidden, "forbidden", "Session belongs to another user")
		return
	}

	fileID, err := subtitleSourceFileID(r, session)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	file, err := h.fileResolver.GetByID(r.Context(), fileID)
	if err != nil || file == nil {
		writeError(w, http.StatusNotFound, "not_found", "Media file not found")
		return
	}

	externalCount := len(file.ExternalSubtitles)
	if trackIndex < externalCount {
		sub := file.ExternalSubtitles[trackIndex]

		// Serve ASS/SSA external subtitles as raw data for client-side rendering.
		if playback.IsASS(sub.Format) && requestedFormat != "vtt" {
			data, err := playback.LoadExternalSubtitleRaw(sub.Path)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal_error",
					"Failed to load external subtitle")
				return
			}
			playback.ServeSubtitle(w, data, "ass")
			return
		}

		vttData, err := playback.LoadExternalSubtitleAsVTT(r.Context(), sub.Path, sub.Format, h.ffmpegPath())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error",
				"Failed to load external subtitle")
			return
		}
		playback.ServeSubtitle(w, vttData, "vtt")
		return
	}

	embeddedIndex := trackIndex - externalCount

	// Check embedded tracks.
	if embeddedIndex < len(file.SubtitleTracks) {
		track := file.SubtitleTracks[embeddedIndex]
		// PGS is the one bitmap codec we can deliver without burn-in: the
		// track is copied losslessly into a .sup stream and rendered
		// client-side. DVD/DVB bitmap subs still require burn-in.
		if playback.NeedsBurnIn(track.Codec) && !playback.IsPGS(track.Codec) {
			writeError(w, http.StatusBadRequest, "bad_request",
				"Bitmap subtitle tracks cannot be extracted as text")
			return
		}

		// Dedicated streaming extract — ffmpeg seeks to the current
		// playback position and pipes cues to the response as they're
		// demuxed, so the first byte lands within ~1s even on network
		// storage. Works identically for direct-play, remux, and
		// transcode because it doesn't depend on any other ffmpeg.
		h.streamEmbeddedSubtitle(w, r, file, embeddedIndex, session, requestedFormat)
		return
	}

	// Check downloaded subtitles (from S3).
	if h.SubtitleRepo != nil && h.S3Client != nil {
		downloaded, err := h.SubtitleRepo.ListDownloadedSubtitles(r.Context(), file.ID)
		if err != nil {
			// A DB failure here must not masquerade as "track not found":
			// surface it as an internal error (with a server-side signal)
			// so the real failure is diagnosable instead of looking like an
			// intermittent 404 to the client.
			slog.ErrorContext(r.Context(), "list downloaded subtitles failed", "component", "api",
				"file_id", file.ID,
				"track", trackIndex,
				"error", err,
			)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list downloaded subtitles")
			return
		}

		downloadedIndex := embeddedIndex - len(file.SubtitleTracks)
		if downloadedIndex >= 0 && downloadedIndex < len(downloaded) {
			dl := downloaded[downloadedIndex]
			data, err := h.S3Client.GetObject(r.Context(), h.S3Bucket, dl.S3Key)
			if err != nil {
				writeError(w, http.StatusBadGateway, "s3_error", "Failed to load subtitle from storage")
				return
			}

			// Serve ASS/SSA downloaded subtitles as raw data.
			if playback.IsASS(string(dl.Format)) && requestedFormat != "vtt" {
				playback.ServeSubtitle(w, data, "ass")
				return
			}

			// If the subtitle is already VTT, serve directly.
			if dl.Format == subtitles.FormatVTT {
				playback.ServeSubtitle(w, data, "vtt")
				return
			}

			// Convert to VTT using the playback conversion pipeline.
			vttData, err := playback.ConvertToVTTWithFFmpeg(r.Context(), data, string(dl.Format), h.ffmpegPath())
			if err != nil {
				writeError(w, http.StatusInternalServerError, "convert_error", "Failed to convert subtitle")
				return
			}
			playback.ServeSubtitle(w, vttData, "vtt")
			return
		}
	}

	writeError(w, http.StatusNotFound, "not_found", "Subtitle track not found")
}

// subtitleSourceFileID pins a subtitle URL to the file whose track list was
// used to create it. A quality/seek restart may change session.MediaFileID to
// an alternate version; interpreting the old combined track index against the
// alternate file can silently serve a different language. Only the session's
// requested or current effective file may be named by the authenticated URL.
func subtitleSourceFileID(r *http.Request, session *playback.Session) (int, error) {
	if session == nil {
		return 0, errors.New("playback session is required")
	}
	raw := strings.TrimSpace(r.URL.Query().Get("file_id"))
	if raw == "" {
		return session.MediaFileID, nil
	}
	fileID, err := strconv.Atoi(raw)
	if err != nil || fileID <= 0 {
		return 0, errors.New("invalid subtitle source file")
	}
	if fileID != session.MediaFileID && fileID != session.RequestedMediaFileID {
		return 0, errors.New("subtitle source file does not belong to playback session")
	}
	return fileID, nil
}

// HandleSubtitleFonts extracts embedded container font attachments for ASS/SSA
// playback. The web player loads these bytes into JASSUB before creating the
// renderer so libass can resolve script font names deterministically.
func (h *StreamHandler) HandleSubtitleFonts(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	sessionID := chi.URLParam(r, "session_id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Session ID is required")
		return
	}
	setPlaybackSessionLogContext(r, sessionID)

	session, err := h.sessionMgr.GetSession(sessionID)
	if err != nil {
		writePlaybackSessionNotFound(w)
		return
	}
	if session.UserID != userID {
		writeError(w, http.StatusForbidden, "forbidden", "Session belongs to another user")
		return
	}

	fileID, err := subtitleSourceFileID(r, session)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	file, err := h.fileResolver.GetByID(r.Context(), fileID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Media file not found")
		return
	}
	if file == nil {
		writeError(w, http.StatusNotFound, "not_found", "Media file not found")
		return
	}
	if err := preflightPlaybackFile(r.Context(), file, h.MissingMarker, h.EventsHub); err != nil {
		if isPlaybackFileMissing(err) {
			h.abortPlaybackSession(r.Context(), session)
		}
		writePlaybackFilePreflightError(w, err)
		return
	}

	trackParam := chi.URLParam(r, "track")
	trackIndex, _, err := playback.ParseSubtitleTrackParam(trackParam)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid subtitle track index")
		return
	}

	embeddedIndex := trackIndex - len(file.ExternalSubtitles)
	if embeddedIndex < 0 || embeddedIndex >= len(file.SubtitleTracks) {
		writeError(w, http.StatusNotFound, "not_found", "Embedded subtitle track not found")
		return
	}
	if !playback.IsASS(file.SubtitleTracks[embeddedIndex].Codec) {
		writeError(w, http.StatusBadRequest, "bad_request", "Subtitle font bundles are only available for ASS/SSA tracks")
		return
	}

	fonts, err := playback.ExtractAttachedSubtitleFonts(r.Context(), file.FilePath, h.ffmpegPath())
	if err != nil {
		slog.WarnContext(r.Context(), "subtitle font extraction failed", "component", "api",
			"file_id", file.ID,
			"track", trackIndex,
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, "font_extract_failed", "Failed to extract subtitle fonts")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(playback.EncodeSubtitleFontBundle(fonts)); err != nil {
		slog.WarnContext(r.Context(), "subtitle font response encode failed", "component", "api", "error", err)
	}
}

func (h *StreamHandler) syncSessionsNow(ctx context.Context, reason string) {
	if h == nil || h.SessionSyncer == nil {
		return
	}
	if err := h.SessionSyncer.SyncNow(ctx); err != nil {
		slog.ErrorContext(ctx, "failed to sync sessions", "component", "api", "reason", reason, "error", err)
	}
}

func (h *StreamHandler) finalizeSessionAbort(ctx context.Context, session *playback.Session, syncNow bool, syncReason string) {
	if h == nil || session == nil || session.ID == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if h.AdminStore != nil {
		if err := h.AdminStore.DeleteSession(ctx, session.ID); err != nil {
			slog.ErrorContext(ctx, "failed to delete synced session", "component", "api", "session", session.ID, "error", err)
		}
	}
	if syncNow {
		h.syncSessionsNow(ctx, syncReason)
	}
}

func (h *StreamHandler) abortPlaybackSession(ctx context.Context, session *playback.Session) {
	if h == nil || session == nil || session.ID == "" {
		return
	}
	if err := h.sessionMgr.StopSession(session.ID); err != nil {
		return
	}
	h.finalizeSessionAbort(ctx, session, true, "stream_abort")
}

func (h *StreamHandler) handleTransportStartFailure(ctx context.Context, session *playback.Session, file *models.MediaFile, err error) {
	if ctx == nil || session == nil || err == nil {
		return
	}
	if preflightErr := preflightPlaybackFile(ctx, file, h.MissingMarker, h.EventsHub); preflightErr != nil {
		err = preflightErr
	}
	if isPlaybackFileMissing(err) || errors.Is(err, os.ErrNotExist) {
		h.abortPlaybackSession(ctx, session)
		return
	}
	slog.WarnContext(ctx, "stream transport startup failed", "component", "api",
		"session", session.ID,
		"file_id", session.MediaFileID,
		"error", err,
		"playback_session_id", session.ID,
	)
}

// streamEmbeddedSubtitle runs a dedicated ffmpeg for a single embedded
// track, seeked to the best-known playback position, and pipes its
// stdout directly to w. Because this ffmpeg is independent of the video
// pipeline, it works the same for direct play, remux, and transcode.
func (h *StreamHandler) streamEmbeddedSubtitle(w http.ResponseWriter, r *http.Request, file *models.MediaFile, embeddedIndex int, session *playback.Session, requestedFormat ...string) {
	track := file.SubtitleTracks[embeddedIndex]
	outFormat := "vtt"
	switch {
	case playback.IsASS(track.Codec):
		outFormat = "ass"
	case playback.IsPGS(track.Codec):
		outFormat = "sup"
	}

	// ASS is fetched exactly once and consumed whole by its client-side
	// renderer (JASSUB), so it must never be windowed. PGS defaults to
	// the same whole-track behavior, but a client that manages its own
	// sliding window (the web player's libpgs hook) opts in explicitly
	// with ?windowed=1 + ?position=/?duration=; there is deliberately no
	// session-position fallback for sup — an implicit window would
	// silently drop cues for clients that fetch once. Note
	// subtitleSeekPosition falls back to the session's last reported
	// position even without a ?position= query — relying on
	// StreamExtractSubtitle's codec guard alone would still log a
	// misleading nonzero seek here.
	var seek, duration float64
	var allowWindow bool
	switch outFormat {
	case "vtt":
		seek = subtitleSeekPosition(r, session)
		duration = subtitleWindowDuration(r)
	case "sup":
		allowWindow, seek, duration = playback.PGSWindowRequest(r.URL.Query())
	}
	slog.InfoContext(r.Context(), "subtitle stream requested", "component", "api",
		"file_id", file.ID,
		"embedded_index", embeddedIndex,
		"track_language", track.Language,
		"track_codec", track.Codec,
		"track_probed_index", track.Index,
		"seek_seconds", seek,
		"duration_seconds", duration,
	)

	opts := playback.StreamExtractOpts{
		InputPath:       file.FilePath,
		TrackIndex:      embeddedIndex,
		SourceCodec:     track.Codec,
		SeekSeconds:     seek,
		DurationSeconds: duration,
		AllowWindow:     allowWindow,
		FFmpegPath:      h.ffmpegPath(),
	}
	if len(requestedFormat) > 0 && requestedFormat[0] == "vtt" {
		// Only text sources can be converted to WebVTT. A bitmap track (PGS
		// reaches here because it is deliverable as .sup; DVD/DVB are rejected
		// upstream) carries no text, so honoring the override would spawn an
		// ffmpeg that always fails after the 200 and headers are committed.
		// Reject before any spawn or header write.
		if playback.NeedsBurnIn(track.Codec) {
			writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type",
				"Bitmap subtitle tracks cannot be converted to WebVTT")
			return
		}
		opts.TargetFormat = "vtt"
		outFormat = "vtt"
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Full-track PGS extracts are expensive (whole-file demux) and byte-
	// identical across requests, so they are served from / teed into the
	// subtitle cache; windowed PGS requests extract their slice from the
	// cached full track when present (warming it in the background when
	// not). All other formats stream uncached: VTT is already windowed
	// and fast, ASS is small.
	if outFormat == "sup" {
		err := h.SubtitleCache.ServeSUPExtract(w, r, opts, playback.StreamExtractSubtitle)
		playback.LogSubtitleStreamError(r.Context(), err, file.ID, embeddedIndex)
		return
	}

	switch outFormat {
	case "ass":
		w.Header().Set("Content-Type", "text/x-ssa; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	opts.Writer = w
	if err := playback.StreamExtractSubtitle(r.Context(), opts); err != nil {
		// Headers already committed — best we can do is log and let
		// the client see a truncated response.
		playback.LogSubtitleStreamError(r.Context(), err, file.ID, embeddedIndex)
	}
}

// subtitleSeekPosition picks the best-known starting position for a
// subtitle extract. A caller-supplied ?position= query wins (the player
// has the most accurate clock), falling back to the session's last
// reported position, then to 0.
func subtitleSeekPosition(r *http.Request, session *playback.Session) float64 {
	if raw := r.URL.Query().Get("position"); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
			return v
		}
	}
	if session != nil && session.Position > 0 {
		return session.Position
	}
	return 0
}

// subtitleWindowDuration picks the bounded extract length. The client
// overrides via ?duration=; absent that we use a 10-minute window,
// which is long enough that a single fetch covers many minutes of
// uninterrupted playback but short enough that the ffmpeg process
// finishes (and frees its input handle) well before the next window
// is requested.
func subtitleWindowDuration(r *http.Request) float64 {
	const defaultDuration = 600.0
	const maxDuration = 3600.0
	if raw := r.URL.Query().Get("duration"); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 && v <= maxDuration {
			return v
		}
	}
	return defaultDuration
}
