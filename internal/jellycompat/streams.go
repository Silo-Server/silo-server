package jellycompat

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"encoding/json"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// Jellyfin Web is sensitive to startup latency. Use shorter compat segments
// than the native global playback default so the first requested HLS chunk and
// the near-head follow-up segments arrive quickly enough for browser playback.
const compatSegmentDuration = 2

// errUpstreamReplaced signals that a concurrent request attached a different
// upstream session to the play session while this one was being created.
var errUpstreamReplaced = errors.New("upstream session replaced concurrently")

type sessionReportRequest struct {
	ItemID              string          `json:"ItemId"`
	MediaSourceID       string          `json:"MediaSourceId"`
	PlaySessionID       string          `json:"PlaySessionId"`
	PositionTicks       *int64          `json:"PositionTicks,omitempty"`
	IsPaused            bool            `json:"IsPaused"`
	AudioStreamIndex    *compatIntValue `json:"AudioStreamIndex,omitempty"`
	SubtitleStreamIndex *compatIntValue `json:"SubtitleStreamIndex,omitempty"`
}

// HandleVideoStream serves Jellyfin-style progressive stream URLs.
func (h *PlaybackHandler) HandleVideoStream(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	routeID := chiURLParam(r, "id")
	mediaSourceID := firstNonEmpty(r.URL.Query().Get("mediaSourceId"), r.URL.Query().Get("MediaSourceId"))
	staticRequest := strings.EqualFold(newCaseInsensitiveQuery(r.URL.Query()).Get("Static"), "true")
	playSession, source, err := h.resolvePlaybackRoute(r, session, routeID, mediaSourceID)
	if err != nil && staticRequest {
		// Infuse uses Static=true for direct play without calling PlaybackInfo first.
		// Create an on-the-fly play session so the stream can proceed. The key
		// lookup must be case-insensitive: SenPlayer sends "static=true"
		// (lowercase) and a case-sensitive Get("Static") would miss it, dropping
		// the client to a 404 "Playback session not found" on every direct play.
		clientPlaySessionID := newCaseInsensitiveQuery(r.URL.Query()).Get("PlaySessionId")
		playSession, source, err = h.createStaticPlaySession(r.Context(), session, routeID, mediaSourceID, clientPlaySessionID)
	}
	if err != nil {
		writeError(w, http.StatusNotFound, "NotFound", "Playback session not found")
		return
	}
	if source == nil {
		writeError(w, http.StatusBadRequest, "BadRequest", "Media source is required")
		return
	}

	method := "direct"
	if !staticRequest && !source.SupportsDirectPlay {
		if source.SupportsDirectStream {
			method = "remux"
		} else {
			writeError(w, http.StatusBadRequest, "BadRequest", "Media source requires transcoding")
			return
		}
	}

	playSession, err = h.ensureUpstreamPlayback(r.Context(), session, playSession.ID, *source, method)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	if h.fileResolver == nil {
		writeError(w, http.StatusInternalServerError, "ServerError", "File resolver not available")
		return
	}
	file, err := h.fileResolver.GetByID(r.Context(), source.FileID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NotFound", "Media file not found")
		return
	}

	seekSeconds := seekSecondsFromTicks(r.URL.Query().Get("StartTimeTicks"))
	if d := float64(source.Version.Duration); d > 0 && seekSeconds > d {
		seekSeconds = d
	}
	if h.NodePlanner != nil && h.JWTSecret != "" {
		plan := h.NodePlanner.PlanSession(playSession.UpstreamSessionID, "", false, source.Version.Bitrate)
		if redirectURL, redirectErr := h.buildProxyRedirectURL(playSession.ID, playSession.UpstreamSessionID, method, file, *source, "", seekSeconds, plan.ProxyNode); redirectErr == nil {
			http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
			return
		}
	}

	// Mark an in-flight media transport, mirroring the native stream handler:
	// a long-lived direct-play range transfer emits no progress reports, and
	// without the transport marker stale cleanup reaps the session mid-stream.
	if h.sessionMgr != nil && playSession.UpstreamSessionID != "" {
		if err := h.sessionMgr.BeginTransport(playSession.UpstreamSessionID); err == nil {
			upstreamSessionID := playSession.UpstreamSessionID
			defer func() {
				_ = h.sessionMgr.EndTransport(upstreamSessionID)
			}()
		}
	}

	switch method {
	case "remux":
		audioTrackIndex := -1
		if resolvedAudioTrackIndex, ok := compatAudioTrackIndex(*source); ok {
			audioTrackIndex = resolvedAudioTrackIndex
		}
		_ = playback.ServeRemuxWithOptions(w, r, file.FilePath, "mp4", seekSeconds, source.TranscodeAudio, audioTrackIndex, file.PrimaryDVProfile(), playback.RemuxServeOptions{
			ContentType: playback.RemuxContentType(file.IsAudioOnly()),
			AudioOnly:   file.IsAudioOnly(),
		})
	default:
		_ = playback.ServeDirectPlay(w, r, file.FilePath)
	}
}

// HandleDownload serves the original media file for /Items/{id}/Download.
// This route backs the CanDownload flag set in mapping.go. CanDownload is
// load-bearing for Infuse: it refuses Direct Play (Static=true streaming)
// for items it believes it cannot download, so the flag must stay true and
// this route must exist.
func (h *PlaybackHandler) HandleDownload(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	contentID, err := decodeContentID(h.codec, chiURLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}
	detail, err := h.content.GetItemDetail(r.Context(), session, contentID, nil)
	if err != nil || detail == nil || len(detail.Versions) == 0 {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}

	version := detail.Versions[0]
	if mediaSourceID := firstNonEmpty(r.URL.Query().Get("mediaSourceId"), r.URL.Query().Get("MediaSourceId")); mediaSourceID != "" {
		if fileID, decodeErr := h.codec.DecodeIntID(EncodedIDMediaSource, mediaSourceID); decodeErr == nil {
			for _, v := range detail.Versions {
				if int64(v.FileID) == fileID {
					version = v
					break
				}
			}
		}
	}

	if h.fileResolver == nil {
		writeError(w, http.StatusInternalServerError, "ServerError", "File resolver not available")
		return
	}
	file, err := h.fileResolver.GetByID(r.Context(), version.FileID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NotFound", "Media file not found")
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(filepath.Base(file.FilePath)))
	_ = playback.ServeDirectPlay(w, r, file.FilePath)
}

// HandleMasterManifest serves the compat-owned HLS manifest route.
// It returns a full-duration VOD manifest so clients can seek to any position.
// Segments that haven't been transcoded yet are served on-demand by the segment handler.
func (h *PlaybackHandler) HandleMasterManifest(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	playSessionID := newCaseInsensitiveQuery(r.URL.Query()).Get("PlaySessionId")
	if playSessionID == "" {
		writeError(w, http.StatusBadRequest, "BadRequest", "PlaySessionId is required")
		return
	}

	playSession, ok := h.playbackStore.Get(playSessionID)
	if !ok || playSession.CompatToken != session.Token {
		writeError(w, http.StatusNotFound, "NotFound", "Playback session not found")
		return
	}

	source := findMediaSource(playSession, firstNonEmpty(r.URL.Query().Get("MediaSourceId"), r.URL.Query().Get("mediaSourceId")))
	if source == nil {
		writeError(w, http.StatusBadRequest, "BadRequest", "Media source is required")
		return
	}

	var err error
	if h.NodePlanner != nil && h.JWTSecret != "" {
		playSession, err = h.ensureUpstreamPlayback(r.Context(), session, playSession.ID, *source, "transcode")
		if err != nil {
			writeCompatUpstreamError(w, err)
			return
		}
		failRemoteStart := func() {
			h.teardownPlaySession(context.WithoutCancel(r.Context()), playSession, nil, nil)
		}
		upstreamSession, upstreamErr := h.sessionMgr.GetSession(playSession.UpstreamSessionID)
		if upstreamErr == nil {
			plan := h.NodePlanner.PlanSession(playSession.UpstreamSessionID, upstreamSession.TranscodeNodeURL, true, source.Version.Bitrate)
			if tcNode := plan.TranscodeNode; tcNode != nil {
				if h.fileResolver == nil {
					failRemoteStart()
					writeError(w, http.StatusInternalServerError, "ServerError", "File resolver not available")
					return
				}
				file, fileErr := h.fileResolver.GetByID(r.Context(), source.FileID)
				if fileErr != nil {
					failRemoteStart()
					writeError(w, http.StatusNotFound, "NotFound", "Media file not found")
					return
				}
				if err := h.sessionMgr.SetTranscodeNodeURL(playSession.UpstreamSessionID, tcNode.URL); err != nil {
					failRemoteStart()
					writeError(w, http.StatusInternalServerError, "ServerError", "Failed to bind transcode node")
					return
				}
				initialSeekSeconds, _ := compatInitialTranscodePosition(*source, h.compatSegmentDuration(), playSession.InitialSeekSeconds)
				if err := h.startRemoteTranscode(r.Context(), playSession.ID, playSession.UpstreamSessionID, *source, file, initialSeekSeconds, tcNode.URL); err != nil {
					failRemoteStart()
					if errors.Is(err, errTranscode4KDisallowed) {
						writeError(w, http.StatusForbidden, "Forbidden", "4K video transcoding is disabled on this server")
						return
					}
					writeError(w, http.StatusBadGateway, "TranscodeStartFailed", "Transcode node rejected the request")
					return
				}
				redirectURL, redirectErr := h.buildProxyRedirectURL(playSession.ID, playSession.UpstreamSessionID, string(playback.PlayTranscode), file, *source, tcNode.URL, 0, plan.ProxyNode)
				if redirectErr != nil {
					failRemoteStart()
					writeError(w, http.StatusInternalServerError, "ServerError", "Failed to sign proxy stream URL")
					return
				}
				http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
				return
			}
		}
	}

	// In distributed mode admins can disable the local fallback so the API
	// server never transcodes when no eligible node exists.
	if h.NodePlanner != nil && !nodepool.LocalTranscodeFallbackAllowed(r.Context(), h.SettingsRepo) {
		if playSession.UpstreamSessionID != "" {
			h.teardownPlaySession(context.WithoutCancel(r.Context()), playSession, nil, nil)
		}
		writeError(w, http.StatusServiceUnavailable, "NoTranscodeNode",
			"No transcode node is available and local transcode fallback is disabled")
		return
	}

	// Ensure the transcode process is running.
	manifest, err := h.ensureTranscodeManifest(r.Context(), session, playSession.ID, *source)
	if err != nil {
		if errors.Is(err, errTranscode4KDisallowed) {
			writeError(w, http.StatusForbidden, "Forbidden", "4K video transcoding is disabled on this server")
			return
		}
		if errors.Is(err, playback.ErrManifestNotReady) {
			writeError(w, http.StatusServiceUnavailable, "NotReady", "Transcode playlist not ready")
			return
		}
		if errors.Is(err, playback.ErrTranscodeFailed) {
			writeError(w, http.StatusInternalServerError, "TranscodeFailed", "Transcode session failed")
			return
		}
		writeCompatUpstreamError(w, err)
		return
	}

	segDuration := h.compatSegmentDuration()

	if manifest == nil {
		manifest = generateFullManifest(source.Version.Duration, segDuration, source.TranscodeAudio, playSession.InitialSeekSeconds)
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rewriteManifest(manifest, playSession.RouteItemID, playSession.ID, source.ID))
}

// HandleHLSManifest serves the compat playlist route used after the master URL.
func (h *PlaybackHandler) HandleHLSManifest(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	playSessionID := chiURLParam(r, "playlistId")
	playSession, ok := h.playbackStore.Get(playSessionID)
	if !ok || playSession.CompatToken != session.Token {
		writeError(w, http.StatusNotFound, "NotFound", "Playback session not found")
		return
	}
	source := firstMediaSource(playSession)
	if mediaSourceID := firstNonEmpty(r.URL.Query().Get("MediaSourceId"), r.URL.Query().Get("mediaSourceId")); mediaSourceID != "" {
		source = findMediaSource(playSession, mediaSourceID)
	}
	if source == nil {
		writeError(w, http.StatusBadRequest, "BadRequest", "Media source is required")
		return
	}

	// Ensure the transcode process is running.
	manifest, err := h.ensureTranscodeManifest(r.Context(), session, playSession.ID, *source)
	if err != nil {
		if errors.Is(err, errTranscode4KDisallowed) {
			writeError(w, http.StatusForbidden, "Forbidden", "4K video transcoding is disabled on this server")
			return
		}
		if errors.Is(err, playback.ErrManifestNotReady) {
			writeError(w, http.StatusServiceUnavailable, "NotReady", "Transcode playlist not ready")
			return
		}
		if errors.Is(err, playback.ErrTranscodeFailed) {
			writeError(w, http.StatusInternalServerError, "TranscodeFailed", "Transcode session failed")
			return
		}
		writeCompatUpstreamError(w, err)
		return
	}

	segDuration := h.compatSegmentDuration()

	if manifest == nil {
		manifest = generateFullManifest(source.Version.Duration, segDuration, source.TranscodeAudio, playSession.InitialSeekSeconds)
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rewriteManifest(manifest, playSession.RouteItemID, playSession.ID, source.ID))
}

// HandleHLSSegment proxies HLS segment requests through compat-owned routes.
// If a segment doesn't exist yet (seek beyond transcoded range), it restarts
// the transcode from the requested position and waits for the segment.
func (h *PlaybackHandler) HandleHLSSegment(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	playSessionID := chiURLParam(r, "playlistId")
	playSession, ok := h.playbackStore.Get(playSessionID)
	if !ok || playSession.CompatToken != session.Token || playSession.UpstreamSessionID == "" {
		writeError(w, http.StatusNotFound, "NotFound", "Playback session not found")
		return
	}

	name := chiURLParam(r, "segmentId")
	ext := chiURLParam(r, "segmentContainer")

	// Load the upstream native session, reconstructing it from the compat-stored
	// recipe on a not-found miss (e.g. after a server restart). Ownership is
	// re-bound to the Jellyfin caller's native user id (StreamAppUserID), matching
	// the recipe owner.
	upstreamSession, status := h.tm.LoadOrReconstructSession(r.Context(), h.sessionMgr.GetSession, playSession.UpstreamSessionID, session.StreamAppUserID, playSession.Recipe)
	switch status {
	case playback.SessionMissing:
		writeError(w, http.StatusNotFound, "NotFound", "Upstream session not found")
		return
	case playback.SessionLoadFailed:
		writeError(w, http.StatusInternalServerError, "ServerError", "Failed to load upstream session")
		return
	case playback.SessionForbidden:
		writeError(w, http.StatusForbidden, "Forbidden", "Session belongs to another user")
		return
	}

	transcodeSession := h.tm.GetTranscodeSession(playSession.UpstreamSessionID)
	if transcodeSession == nil {
		// Local transcode whose process state was lost (restart): reconstruct it
		// seeked to the requested segment. Remote-node sessions are served by the
		// proxy, not here, so only reconstruct an integrated (no node URL) session.
		if upstreamSession.TranscodeNodeURL == "" && playSession.Recipe != nil {
			requestedSegment := -1
			if segNum, parseErr := playback.ParseSegmentNumber(name); parseErr == nil {
				requestedSegment = segNum
			}
			transcodeSession = h.tm.ReconstructTranscode(r.Context(), playSession.UpstreamSessionID, requestedSegment, *playSession.Recipe)
		}
		if transcodeSession == nil {
			writeError(w, http.StatusNotFound, "NotFound", "Transcode session not found")
			return
		}
	}

	segmentFile := name + "." + ext
	segmentPath, err := transcodeSession.GetSegment(segmentFile)
	if err != nil && errors.Is(err, playback.ErrSegmentNotFound) {
		segNum, parseErr := playback.ParseSegmentNumber(name)
		if parseErr == nil {
			now := time.Now()
			decision := transcodeSession.SegmentRecoveryDecision(segNum, now)
			lastProducedAgeMS := int64(-1)
			if !decision.Progress.LastProducedAt.IsZero() {
				lastProducedAgeMS = now.Sub(decision.Progress.LastProducedAt).Milliseconds()
			}
			slog.InfoContext(r.Context(), "transcode segment missing", "component", "jellycompat",
				"segment", segmentFile,
				"requested_segment", segNum,
				"produced_head", decision.Progress.ProducedHead,
				"last_requested_segment", decision.Progress.LastRequestedSegment,
				"start_segment_number", decision.Progress.StartSegmentNumber,
				"last_produced_age_ms", lastProducedAgeMS,
				"wait_timeout_ms", decision.WaitTimeout.Milliseconds(),
				"reason", decision.Reason,
				"play_session", playSessionID,
				"session", playSession.UpstreamSessionID,
				"playback_session_id", playSession.UpstreamSessionID,
			)
			if decision.Wait {
				slog.InfoContext(r.Context(), "transcode segment wait", "component", "jellycompat",
					"segment", segmentFile,
					"requested_segment", segNum,
					"produced_head", decision.Progress.ProducedHead,
					"last_requested_segment", decision.Progress.LastRequestedSegment,
					"start_segment_number", decision.Progress.StartSegmentNumber,
					"last_produced_age_ms", lastProducedAgeMS,
					"wait_timeout_ms", decision.WaitTimeout.Milliseconds(),
					"reason", decision.Reason,
					"play_session", playSessionID,
					"session", playSession.UpstreamSessionID,
					"playback_session_id", playSession.UpstreamSessionID,
				)
				segmentPath, err = transcodeSession.WaitForSegment(segmentFile, decision.WaitTimeout)
				if err != nil && errors.Is(err, playback.ErrSegmentNotFound) {
					slog.InfoContext(r.Context(), "transcode segment wait timeout", "component", "jellycompat",
						"segment", segmentFile,
						"requested_segment", segNum,
						"produced_head", decision.Progress.ProducedHead,
						"last_requested_segment", decision.Progress.LastRequestedSegment,
						"start_segment_number", decision.Progress.StartSegmentNumber,
						"last_produced_age_ms", lastProducedAgeMS,
						"wait_timeout_ms", decision.WaitTimeout.Milliseconds(),
						"reason", decision.Reason,
						"play_session", playSessionID,
						"session", playSession.UpstreamSessionID,
						"playback_session_id", playSession.UpstreamSessionID,
					)
				}
			}

			if err != nil && errors.Is(err, playback.ErrSegmentNotFound) && decision.RestartOnTimeout {
				seekSeconds, ok, seekErr := transcodeSession.RestartSeekTarget(segNum)
				if seekErr != nil && !errors.Is(seekErr, playback.ErrManifestNotReady) {
					slog.ErrorContext(r.Context(), "resolve transcode seek target", "component", "jellycompat",
						"error", seekErr,
						"segment", segmentFile,
						"play_session", playSessionID,
						"session", playSession.UpstreamSessionID,
						"playback_session_id", playSession.UpstreamSessionID,
					)
				}

				// Copy-mode with an unresolved seek target (ok=false, no error)
				// means the manifest can't place this segment yet. Don't restart
				// at a fabricated position; surface ErrSegmentNotFound so the
				// client retries while the session keeps producing manifest.
				// Mirrors the transcode-node guard in
				// internal/transcodenode/server.go.
				if !ok && seekErr == nil && transcodeSession.IsCopyVideo() {
					err = playback.ErrSegmentNotFound
				}

				if ok {
					slog.InfoContext(r.Context(), "transcode seek restart", "component", "jellycompat",
						"segment", segmentFile,
						"requested_segment", segNum,
						"produced_head", decision.Progress.ProducedHead,
						"last_requested_segment", decision.Progress.LastRequestedSegment,
						"start_segment_number", decision.Progress.StartSegmentNumber,
						"last_produced_age_ms", lastProducedAgeMS,
						"wait_timeout_ms", decision.WaitTimeout.Milliseconds(),
						"reason", decision.Reason,
						"seek_seconds", seekSeconds,
						"play_session", playSessionID,
						"session", playSession.UpstreamSessionID,
						"playback_session_id", playSession.UpstreamSessionID,
					)

					if restartErr := h.tm.RestartSessionLocked(
						context.WithoutCancel(r.Context()),
						playSession.UpstreamSessionID,
						transcodeSession,
						seekSeconds,
						segNum,
					); restartErr == nil {
						segmentPath, err = transcodeSession.WaitForSegment(segmentFile, 30*time.Second)
					}
				}
			}
		} else if transcodeSession.IsRunning() {
			// Non-numbered segment (e.g. init.mp4 for fMP4 HLS).
			// Wait briefly — the init segment is written almost immediately.
			segmentPath, err = transcodeSession.WaitForSegment(segmentFile, 10*time.Second)
		}
	}
	if err != nil {
		status, code, message := hlsSegmentErrorResponse(err)
		writeError(w, status, code, message)
		return
	}

	if segNum, parseErr := playback.ParseSegmentNumber(name); parseErr == nil {
		transcodeSession.ReportSegmentDownloaded(segNum)
	}

	http.ServeFile(w, r, segmentPath)
}

// hlsSegmentErrorResponse maps a segment-retrieval error to a Jellyfin-faithful
// HTTP status. A segment that is absent (ErrSegmentNotFound) or whose transcode
// process started and then exited non-zero (ErrTranscodeFailed, surfaced by
// WaitForSegment after the recovery/restart path is exhausted) will never
// materialize. Jellyfin serves both as 404: its DynamicHls segment handler falls
// through to a PhysicalFileResult for the missing file, which ASP.NET returns as
// 404, never 500. Reserve 500 for genuinely unexpected errors (e.g. a stat
// failure on a file that does exist).
func hlsSegmentErrorResponse(err error) (status int, code, message string) {
	switch {
	case errors.Is(err, playback.ErrSegmentNotFound), errors.Is(err, playback.ErrTranscodeFailed):
		return http.StatusNotFound, "NotFound", "Segment not found"
	default:
		return http.StatusInternalServerError, "ServerError", "Failed to load segment"
	}
}

// HandleSubtitleStream proxies subtitle requests through the native stream subtitle route.
func (h *PlaybackHandler) HandleSubtitleStream(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	_, source, err := h.resolvePlaybackRoute(r, session, chiURLParam(r, "routeMediaSourceId"), chiURLParam(r, "routeMediaSourceId"))
	if err != nil || source == nil {
		writeError(w, http.StatusNotFound, "NotFound", "Playback session not found")
		return
	}

	if h.fileResolver == nil {
		writeError(w, http.StatusInternalServerError, "ServerError", "File resolver not available")
		return
	}
	file, err := h.fileResolver.GetByID(r.Context(), source.FileID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NotFound", "Media file not found")
		return
	}

	routeIndex := chiURLParam(r, "routeIndex")
	trackIndex, parseErr := strconv.Atoi(routeIndex)
	if parseErr != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", "Invalid subtitle index")
		return
	}
	requestedFormat := strings.ToLower(strings.TrimSpace(chiURLParam(r, "routeFormat")))
	if requestedFormat == "" {
		requestedFormat = "vtt"
	}

	// Check for external subtitles first.
	for i, sub := range file.ExternalSubtitles {
		if externalSubtitleRouteIndex(file, i) == trackIndex {
			// Serve ASS/SSA as raw data when requested.
			if requestedFormat == "ass" && playback.IsASS(sub.Format) {
				data, readErr := os.ReadFile(sub.Path)
				if readErr != nil {
					writeError(w, http.StatusInternalServerError, "ServerError", "Failed to load subtitle")
					return
				}
				writeSubtitleResponse(w, "ass", data)
				return
			}
			if requestedFormat == "srt" && subtitleCanServeSRT(sub.Format) {
				data, readErr := os.ReadFile(sub.Path)
				if readErr != nil {
					writeError(w, http.StatusInternalServerError, "ServerError", "Failed to load subtitle")
					return
				}
				writeSubtitleResponse(w, requestedFormat, data)
				return
			}
			data, subErr := playback.LoadExternalSubtitleAsVTT(r.Context(), sub.Path, sub.Format)
			if subErr != nil {
				writeError(w, http.StatusInternalServerError, "ServerError", "Failed to load subtitle")
				return
			}
			writeSubtitleResponse(w, "vtt", data)
			return
		}
	}

	// Check downloaded subtitles (from S3).
	if h.SubtitleRepo != nil && h.S3Client != nil {
		downloaded, _ := h.SubtitleRepo.ListDownloadedSubtitles(r.Context(), file.ID)
		// Compute the base index for downloaded subtitles to match how PlaybackInfo assigns them.
		// Downloaded subs are indexed after all existing streams (last existing index + 1).
		baseIndex := computeDownloadedSubBaseIndex(file)
		downloadedIndex := trackIndex - baseIndex
		if downloadedIndex >= 0 && downloadedIndex < len(downloaded) {
			dl := downloaded[downloadedIndex]
			data, err := h.S3Client.GetObject(r.Context(), h.S3Bucket, dl.S3Key)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "ServerError", "Failed to load subtitle from storage")
				return
			}

			// Serve downloaded ASS/SSA as raw data when requested.
			if requestedFormat == "ass" && playback.IsASS(string(dl.Format)) {
				writeSubtitleResponse(w, "ass", data)
				return
			}
			if requestedFormat == "srt" && subtitleCanServeSRT(string(dl.Format)) {
				writeSubtitleResponse(w, requestedFormat, data)
				return
			}
			// If already VTT, serve directly.
			if dl.Format == subtitles.FormatVTT {
				writeSubtitleResponse(w, "vtt", data)
				return
			}

			vttData, convErr := playback.ConvertToVTT(data, string(dl.Format))
			if convErr != nil {
				writeError(w, http.StatusInternalServerError, "ServerError", "Failed to convert subtitle")
				return
			}
			writeSubtitleResponse(w, "vtt", vttData)
			return
		}
	}

	embeddedOrdinal, embeddedTrack := findEmbeddedSubtitle(file, trackIndex)
	if embeddedOrdinal < 0 {
		writeError(w, http.StatusNotFound, "NotFound", "Subtitle not found")
		return
	}
	if playback.NeedsBurnIn(embeddedTrack.Codec) {
		writeError(w, http.StatusBadRequest, "BadRequest", "Subtitle requires burn-in")
		return
	}

	// Serve ASS/SSA as raw ASS when requested, preserving styled subtitle data.
	if requestedFormat == "ass" && playback.IsASS(embeddedTrack.Codec) {
		data, err := playback.ExtractSubtitleWithFormat(r.Context(), file.FilePath, embeddedOrdinal, "ass", h.FFmpegPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "ServerError", "Failed to extract subtitle")
			return
		}
		writeSubtitleResponse(w, "ass", data)
		return
	}

	data, format, subErr := playback.ExtractSubtitle(r.Context(), file.FilePath, embeddedOrdinal)
	if subErr != nil {
		writeError(w, http.StatusInternalServerError, "ServerError", "Failed to extract subtitle")
		return
	}
	if requestedFormat == "srt" && subtitleCanServeSRT(format) {
		writeSubtitleResponse(w, requestedFormat, data)
		return
	}
	vttData, convErr := playback.ConvertToVTT(data, format)
	if convErr != nil {
		writeError(w, http.StatusInternalServerError, "ServerError", "Failed to convert subtitle")
		return
	}
	writeSubtitleResponse(w, "vtt", vttData)
}

func findEmbeddedSubtitle(file *models.MediaFile, routeIndex int) (int, models.SubtitleTrack) {
	for i, track := range file.SubtitleTracks {
		if subtitleTrackRouteIndex(file, i, track) == routeIndex {
			return i, track
		}
	}
	return -1, models.SubtitleTrack{}
}

func subtitleTrackRouteIndex(file *models.MediaFile, ordinal int, track models.SubtitleTrack) int {
	if track.Index > 0 {
		return track.Index
	}
	return len(file.VideoTracks) + len(file.AudioTracks) + ordinal
}

func externalSubtitleRouteIndex(file *models.MediaFile, ordinal int) int {
	nextIndex := len(file.VideoTracks) + len(file.AudioTracks)
	for i, track := range file.SubtitleTracks {
		index := subtitleTrackRouteIndex(file, i, track)
		if index >= nextIndex {
			nextIndex = index + 1
		}
	}
	if nextIndex < 1 {
		nextIndex = 1
	}
	return nextIndex + ordinal
}

func subtitleCanServeSRT(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "srt", "subrip":
		return true
	default:
		return false
	}
}

func writeSubtitleResponse(w http.ResponseWriter, format string, data []byte) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "ass", "ssa":
		w.Header().Set("Content-Type", "text/x-ssa; charset=utf-8")
	case "srt", "subrip":
		w.Header().Set("Content-Type", "application/x-subrip; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// HandleSessionPlaying handles POST /Sessions/Playing.
func (h *PlaybackHandler) HandleSessionPlaying(w http.ResponseWriter, r *http.Request) {
	h.handlePlaybackReport(w, r, false)
}

// HandleSessionPlayingProgress handles POST /Sessions/Playing/Progress.
func (h *PlaybackHandler) HandleSessionPlayingProgress(w http.ResponseWriter, r *http.Request) {
	h.handlePlaybackReport(w, r, false)
}

// HandleSessionPlayingStopped handles POST /Sessions/Playing/Stopped.
func (h *PlaybackHandler) HandleSessionPlayingStopped(w http.ResponseWriter, r *http.Request) {
	h.handlePlaybackReport(w, r, true)
}

// HandleDeleteActiveEncodings handles DELETE /Videos/ActiveEncodings.
//
// Jellyfin clients (e.g. JellyCon) call this endpoint when playback stops to
// signal the server to tear down any running HLS transcode for the session.
// Without it, the transcode process keeps running until the playback session
// TTL expires (default 6 h). We honour the request by stopping the transcode
// identified by the playSessionId query parameter.
func (h *PlaybackHandler) HandleDeleteActiveEncodings(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	q := newCaseInsensitiveQuery(r.URL.Query())
	// DeviceId is intentionally ignored: Silo's playback store is keyed by
	// PlaySessionId, clients always send it, and Jellyfin's own teardown matches
	// by playSessionId (ignoring deviceId) whenever playSessionId is non-empty.
	playSessionID := q.Get("PlaySessionId")
	if playSessionID == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Ownership guard (mirrors the Stopped report path): only the session's own
	// caller may tear it down, and a session with no upstream transcode yet has
	// nothing to tear down. The PlaybackSession is created by PlaybackInfo with
	// an empty UpstreamSessionID; it is only populated once the first manifest
	// request reaches ensureUpstreamPlayback. Deleting it before then would drop
	// a live session and 404 the pending manifest, so an unknown, not-owned, or
	// not-yet-started PlaySessionId is a uniform idempotent 204 no-op (no
	// cross-session teardown, no ownership oracle, no premature deletion).
	playSession, ok := h.playbackStore.Get(playSessionID)
	if !ok || playSession.CompatToken != session.Token || playSession.UpstreamSessionID == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	fallback := compatScrobbleFallbackSession(session, playSession, nil, 0, false, false)
	upstreamSession, transcodeNodeURL := h.compatStopSnapshot(playSession, fallback)
	if event, ok := h.compatScrobbleEvent(
		r.Context(), compatScrobbleStop, playSession, upstreamSession, nil, nil,
	); ok {
		h.stageCompatTerminal(r.Context(), playSession, upstreamSession, transcodeNodeURL, event, false, false, 0)
	} else if upstreamSession == nil {
		// With no native session and no reported position, publishing a zero-value
		// fallback could move provider progress backwards. Keep only the terminal
		// authenticated mapping for a possible later Stopped report.
		if err := h.playbackStore.HideFromRouting(playSession.ID, playSession.CompatToken); err != nil &&
			!errors.Is(err, ErrSessionNotFound) {
			h.scheduleCompatTerminalHide(playSession.ID, playSession.CompatToken, playSession.ExpiresAt, 1)
		}
		h.cleanupPlaySession(r.Context(), playSession, nil, transcodeNodeURL)
	} else {
		h.playbackStore.Delete(playSession.ID)
		h.cleanupPlaySession(r.Context(), playSession, upstreamSession, transcodeNodeURL)
	}

	w.WriteHeader(http.StatusNoContent)
}

// teardownPlaySession stages the authoritative stop before resource cleanup,
// then delivers it through a leased durable record. The record is removed only
// after watch-sync accepts the event, so a provider-queue failure remains
// retryable by the client or the delayed ActiveEncodings fallback.
func (h *PlaybackHandler) teardownPlaySession(
	ctx context.Context,
	playSession *PlaybackSession,
	fallbackSession *playback.Session,
	positionOverride *float64,
) {
	upstreamSession, transcodeNodeURL := h.compatStopSnapshot(playSession, fallbackSession)
	if event, ok := h.compatScrobbleEvent(
		ctx, compatScrobbleStop, playSession, upstreamSession, nil, positionOverride,
	); ok {
		h.stageCompatTerminal(ctx, playSession, upstreamSession, transcodeNodeURL, event, true, false, 0)
	} else if playSession.Terminal {
		// A late Stopped report without PositionTicks cannot replace a staged
		// fallback after ActiveEncodings already removed the native session. Keep
		// that durable event (or terminal shell) and retry its delivery instead of
		// deleting the only recoverable stop position.
		h.cleanupPlaySession(ctx, playSession, upstreamSession, transcodeNodeURL)
		if playSession.TerminalScrobbleEvent != nil {
			h.deliverCompatTerminal(
				ctx,
				playSession.ID,
				playSession.CompatToken,
				playSession.TerminalAuthoritative,
				playSession.ExpiresAt,
				0,
				true,
			)
		}
	} else {
		h.playbackStore.Delete(playSession.ID)
		h.cleanupPlaySession(ctx, playSession, upstreamSession, transcodeNodeURL)
	}
}

func (h *PlaybackHandler) compatStopSnapshot(
	playSession *PlaybackSession,
	fallbackSession *playback.Session,
) (*playback.Session, string) {
	transcodeNodeURL := ""
	var upstreamSession *playback.Session
	if h.sessionMgr != nil {
		if current, err := h.sessionMgr.GetSession(playSession.UpstreamSessionID); err == nil {
			upstreamSession = current
			transcodeNodeURL = upstreamSession.TranscodeNodeURL
		}
	}
	if upstreamSession == nil && fallbackSession != nil {
		copy := *fallbackSession
		copy.ID = playSession.UpstreamSessionID
		if source := compatScrobbleSource(playSession, &copy, nil); source != nil {
			copy.MediaFileID = source.FileID
		}
		upstreamSession = &copy
	}
	return upstreamSession, transcodeNodeURL
}

// cleanupPlaySession performs idempotent process/resource cleanup after the
// terminal provider event has been staged (or intentionally omitted).
func (h *PlaybackHandler) cleanupPlaySession(
	ctx context.Context,
	playSession *PlaybackSession,
	upstreamSession *playback.Session,
	transcodeNodeURL string,
) {
	h.tm.CloseTranscodeSession(playSession.UpstreamSessionID, transcodeNodeURL)
	if h.sessionMgr != nil {
		_ = h.sessionMgr.StopSession(playSession.UpstreamSessionID)
	}
	// Deliberate stop: drop the node recipe so a buffered/retrying request after
	// a node restart cannot reconstruct a fresh ffmpeg for this stopped session.
	// Best effort and bounded — never fail teardown on a recipe-store hiccup.
	if h.RecipeNodeStore != nil {
		delCtx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 2*time.Second)
		defer cancel()
		if err := h.RecipeNodeStore.Delete(delCtx, playSession.UpstreamSessionID); err != nil {
			slog.WarnContext(ctx, "delete node transcode recipe failed", "component", "jellycompat", "error", err,
				"playback_session_id", playSession.UpstreamSessionID)
		}
	}
	// Clients often drop the connection right after reporting a stop, so detach
	// the sync from request cancellation to keep the admin view accurate.
	h.syncSessionsNow(context.WithoutCancel(ctx), "compat_stop")
}

// The compat transcode ladder always lands on H.264/AAC; these name the
// target codecs the Jellyfin-compat pipeline hands to ffmpeg.
const (
	compatTargetVideoCodec = "h264"
	compatTargetAudioCodec = "aac"
)

const (
	compatTerminalClaimLease           = 10 * time.Second
	compatTerminalInitialRetryDelay    = 250 * time.Millisecond
	compatTerminalMaxRetryDelay        = 30 * time.Second
	defaultCompatTerminalFallbackDelay = 2 * time.Second
)

func (h *PlaybackHandler) compatTerminalFallbackDelay() time.Duration {
	if h != nil && h.terminalFallbackDelay > 0 {
		return h.terminalFallbackDelay
	}
	return defaultCompatTerminalFallbackDelay
}

func compatTerminalRetryDelay(attempt int) time.Duration {
	delay := compatTerminalInitialRetryDelay
	for i := 0; i < attempt && delay < compatTerminalMaxRetryDelay; i++ {
		delay *= 2
		if delay > compatTerminalMaxRetryDelay {
			return compatTerminalMaxRetryDelay
		}
	}
	return delay
}

func (h *PlaybackHandler) stageCompatTerminal(
	ctx context.Context,
	playSession *PlaybackSession,
	upstreamSession *playback.Session,
	transcodeNodeURL string,
	event watchsync.ScrobbleEvent,
	authoritative bool,
	cleanupDone bool,
	attempt int,
) {
	staged, err := h.playbackStore.StageTerminal(playSession.ID, playSession.CompatToken, event, authoritative)
	if err != nil {
		// Production durable staging installs its local marker before I/O. Keep
		// the interface invariant for alternate stores that fail before doing so.
		_ = h.playbackStore.HideFromRouting(playSession.ID, playSession.CompatToken)
		if errors.Is(err, ErrSessionNotFound) {
			if !cleanupDone {
				h.cleanupPlaySession(ctx, playSession, upstreamSession, transcodeNodeURL)
			}
			return
		}
		if !cleanupDone {
			h.cleanupPlaySession(ctx, playSession, upstreamSession, transcodeNodeURL)
			cleanupDone = true
		}
		if playSession.ExpiresAt.IsZero() || time.Now().Before(playSession.ExpiresAt) {
			h.scheduleCompatTerminalStage(
				playSession, upstreamSession, transcodeNodeURL, event, authoritative, cleanupDone, attempt+1,
			)
		} else if !cleanupDone {
			h.cleanupPlaySession(ctx, playSession, upstreamSession, transcodeNodeURL)
		}
		return
	}
	if !cleanupDone {
		h.cleanupPlaySession(ctx, staged, upstreamSession, transcodeNodeURL)
	}
	if authoritative {
		h.deliverCompatTerminal(ctx, staged.ID, staged.CompatToken, true, staged.ExpiresAt, 0, true)
		return
	}
	h.scheduleCompatTerminalDelivery(
		staged.ID, staged.CompatToken, false, staged.ExpiresAt, h.compatTerminalFallbackDelay(), 0,
	)
}

func (h *PlaybackHandler) scheduleCompatTerminalHide(
	playSessionID string,
	compatToken string,
	expiresAt time.Time,
	attempt int,
) {
	time.AfterFunc(compatTerminalRetryDelay(attempt), func() {
		if !expiresAt.IsZero() && !time.Now().Before(expiresAt) {
			return
		}
		err := h.playbackStore.HideFromRouting(playSessionID, compatToken)
		if err != nil && !errors.Is(err, ErrSessionNotFound) {
			h.scheduleCompatTerminalHide(playSessionID, compatToken, expiresAt, attempt+1)
		}
	})
}

func (h *PlaybackHandler) scheduleCompatTerminalStage(
	playSession *PlaybackSession,
	upstreamSession *playback.Session,
	transcodeNodeURL string,
	event watchsync.ScrobbleEvent,
	authoritative bool,
	cleanupDone bool,
	attempt int,
) {
	playSessionCopy := *playSession
	var upstreamCopy *playback.Session
	if upstreamSession != nil {
		copy := *upstreamSession
		upstreamCopy = &copy
	}
	time.AfterFunc(compatTerminalRetryDelay(attempt), func() {
		h.stageCompatTerminal(
			context.Background(), &playSessionCopy, upstreamCopy, transcodeNodeURL,
			event, authoritative, cleanupDone, attempt,
		)
	})
}

func (h *PlaybackHandler) scheduleCompatTerminalDelivery(
	playSessionID string,
	compatToken string,
	requireAuthoritative bool,
	expiresAt time.Time,
	delay time.Duration,
	attempt int,
) {
	time.AfterFunc(delay, func() {
		h.deliverCompatTerminal(
			context.Background(), playSessionID, compatToken, requireAuthoritative, expiresAt, attempt, true,
		)
	})
}

// deliverCompatTerminal leases the staged event, persists it into watch-sync's
// durable queue, and only then completes the compat terminal record. A
// provisional ActiveEncodings fallback remains available for a later
// authoritative Stopped replacement.
func (h *PlaybackHandler) deliverCompatTerminal(
	ctx context.Context,
	playSessionID string,
	compatToken string,
	requireAuthoritative bool,
	expiresAt time.Time,
	attempt int,
	retry bool,
) {
	if h == nil || h.playbackStore == nil || h.WatchScrobbler == nil {
		return
	}
	if !expiresAt.IsZero() && !time.Now().Before(expiresAt) {
		return
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	claimUntil := now.Add(compatTerminalClaimLease)
	playSession, err := h.playbackStore.ClaimTerminal(playSessionID, compatToken, claimUntil)
	if err != nil {
		if !requireAuthoritative && errors.Is(err, ErrTerminalClaimUnavailable) {
			if pending, ok := h.playbackStore.GetFinalizable(playSessionID, compatToken); ok &&
				pending.TerminalFallbackSent && !pending.TerminalAuthoritative {
				return
			}
		}
		if retry && !errors.Is(err, ErrSessionNotFound) {
			h.scheduleCompatTerminalDelivery(
				playSessionID, compatToken, requireAuthoritative, expiresAt,
				compatTerminalRetryDelay(attempt), attempt+1,
			)
		}
		return
	}
	ownedClaimUntil := playSession.TerminalClaimUntil
	if playSession.TerminalScrobbleEvent == nil || (requireAuthoritative && !playSession.TerminalAuthoritative) {
		h.playbackStore.ReleaseTerminalClaim(
			playSessionID, compatToken, ownedClaimUntil, playSession.TerminalClaimVersion, false,
		)
		if retry {
			h.scheduleCompatTerminalDelivery(
				playSessionID, compatToken, requireAuthoritative, expiresAt,
				compatTerminalRetryDelay(attempt), attempt+1,
			)
		}
		return
	}

	err = h.dispatchCompatScrobbleEventConfirmed(
		ctx,
		compatScrobbleStop,
		*playSession.TerminalScrobbleEvent,
		playSession.TerminalAuthoritative,
	)
	if err != nil {
		h.playbackStore.ReleaseTerminalClaim(
			playSessionID, compatToken, ownedClaimUntil, playSession.TerminalClaimVersion, false,
		)
		if retry {
			h.scheduleCompatTerminalDelivery(
				playSessionID, compatToken, requireAuthoritative, expiresAt,
				compatTerminalRetryDelay(attempt), attempt+1,
			)
		}
		return
	}
	if playSession.TerminalAuthoritative {
		h.playbackStore.CompleteTerminal(
			playSessionID, compatToken, ownedClaimUntil, playSession.TerminalClaimVersion,
		)
		// If a newer authoritative report replaced this event while it was in
		// flight, completion intentionally failed. Release the old lease so the
		// replacement can be claimed immediately instead of waiting for expiry.
		h.playbackStore.ReleaseTerminalClaim(
			playSessionID, compatToken, ownedClaimUntil, playSession.TerminalClaimVersion, false,
		)
		return
	}
	h.playbackStore.ReleaseTerminalClaim(
		playSessionID, compatToken, ownedClaimUntil, playSession.TerminalClaimVersion, true,
	)
}

// compatSessionSyncTimeout bounds the immediate session sync issued from
// request paths, so a stalled database degrades to the periodic reconciler
// tick instead of pinning request goroutines.
const compatSessionSyncTimeout = 5 * time.Second

func compatDetachedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(ctx, compatSessionSyncTimeout)
}

// syncSessionsNow flushes the native-session snapshot to the shared admin
// live-session table so compat start/stop events are visible immediately
// instead of on the next reconciler tick.
func (h *PlaybackHandler) syncSessionsNow(ctx context.Context, reason string) {
	if h == nil || h.SessionSyncer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, compatSessionSyncTimeout)
	defer cancel()
	if err := h.SessionSyncer.SyncNow(ctx); err != nil {
		slog.ErrorContext(ctx, "jellycompat: failed to sync sessions", "component", "jellycompat", "reason", reason, "error", err)
	}
}

func (h *PlaybackHandler) handlePlaybackReport(w http.ResponseWriter, r *http.Request, stop bool) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	var req sessionReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "BadRequest", "Invalid session report")
		return
	}
	if req.PlaySessionID == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var playSession *PlaybackSession
	var ok bool
	if stop {
		playSession, ok = h.playbackStore.GetFinalizable(req.PlaySessionID, session.Token)
	} else {
		playSession, ok = h.playbackStore.Get(req.PlaySessionID)
		if ok && playSession.CompatToken != session.Token {
			playSession, ok = nil, false
		}
	}
	if !ok {
		// Static=true direct play (Infuse, SenPlayer) skips PlaybackInfo, so the
		// client reports progress under its own generated PlaySessionId. The
		// stream path recorded that id as an alias on the play session it
		// bound; resolve by the alias first, then fall back to the same
		// route-scoped lookup the stream path uses (see resolvePlaybackRoute).
		// Without either, these reports silently no-op, the admin activity view
		// position freezes, and stale cleanup drops the still-active session.
		if stop {
			playSession, ok = h.playbackStore.FindFinalizableByClientPlaySessionID(
				session.Token, req.PlaySessionID, req.ItemID, req.MediaSourceID,
			)
		} else {
			playSession, ok = h.playbackStore.FindByClientPlaySessionID(session.Token, req.PlaySessionID)
		}
		if ok && !reportMatchesPlaySession(playSession, req) {
			playSession, ok = nil, false
		}
	}
	if !ok && !stop {
		for _, routeID := range []string{req.ItemID, req.MediaSourceID} {
			if routeID == "" {
				continue
			}
			playSession, _, ok = h.playbackStore.FindByRoute(session.Token, routeID)
			if ok && reportMatchesPlaySession(playSession, req) {
				break
			}
			playSession, ok = nil, false
		}
	}
	if !ok || playSession.UpstreamSessionID == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	positionSeconds := 0.0
	positionReported := req.PositionTicks != nil
	if positionReported {
		positionSeconds = float64(*req.PositionTicks) / 10_000_000
		if positionSeconds < 0 {
			positionSeconds = 0
		}
	}
	audioTrackIndex := 0
	audioRestarted := false
	// Jellyfin web/mobile clients send AudioStreamIndex on every progress
	// report, not just on track changes. Restarting ffmpeg on each report
	// (every ~10s) tears down segments the player is still appending and
	// causes an hls.js retry loop. Only act when the index actually changes.
	if req.AudioStreamIndex != nil && audioSelectionChanged(playSession, req.MediaSourceID, int(*req.AudioStreamIndex)) {
		selectedAudioStreamIndex := int(*req.AudioStreamIndex)
		// Key store mutations by the resolved session id: after an alias or
		// route fallback, req.PlaySessionID is the client's own id and is not
		// a store key.
		updatedPlaySession, updatedSource, updateErr := h.setSelectedAudioStream(playSession.ID, req.MediaSourceID, selectedAudioStreamIndex)
		if updateErr == nil {
			playSession = updatedPlaySession
			if resolvedAudioTrackIndex, ok := compatAudioTrackIndex(*updatedSource); ok {
				audioTrackIndex = resolvedAudioTrackIndex
			}
			if syncErr := h.syncUpstreamAudioSelection(playSession, *updatedSource); syncErr != nil {
				slog.WarnContext(r.Context(), "jellycompat audio selection sync failed", "component", "jellycompat",
					"play_session_id", playSession.ID,
					"upstream_session_id", playSession.UpstreamSessionID,
					"error", syncErr,
				)
			}
			restarted, restartErr := h.restartCompatTranscodeForAudioSelection(r.Context(), playSession, *updatedSource, positionSeconds)
			if restartErr != nil {
				slog.WarnContext(r.Context(), "jellycompat audio selection restart failed", "component", "jellycompat",
					"play_session_id", playSession.ID,
					"upstream_session_id", playSession.UpstreamSessionID,
					"error", restartErr,
				)
			}
			audioRestarted = restarted
			slog.InfoContext(r.Context(), "jellycompat audio selection updated", "component", "jellycompat",
				"play_session_id", playSession.ID,
				"media_source_id", updatedSource.ID,
				"audio_stream_index", selectedAudioStreamIndex,
				"audio_track_index", audioTrackIndex,
				"transcode_restarted", audioRestarted,
			)
		}
	}
	var previousSession *playback.Session
	progressUpdated := false
	if positionReported && h.sessionMgr != nil {
		if current, err := h.sessionMgr.GetSession(playSession.UpstreamSessionID); err == nil && current != nil {
			copy := *current
			previousSession = &copy
		}
		err := h.sessionMgr.UpdateProgress(playSession.UpstreamSessionID, positionSeconds, req.IsPaused)
		progressUpdated = err == nil
		if errors.Is(err, playback.ErrSessionNotFound) && !stop {
			// The upstream session was reaped as stale (e.g. the client buffered
			// far ahead and went quiet between range requests). The report proves
			// the client is still playing, so recreate the session instead of
			// dropping it from session tracking for the rest of playback.
			if revived := h.reviveUpstreamForReport(r.Context(), session, playSession, req.MediaSourceID); revived != nil {
				playSession = revived
				progressUpdated = h.sessionMgr.UpdateProgress(playSession.UpstreamSessionID, positionSeconds, req.IsPaused) == nil
				previousSession = nil
			}
		}
	}
	if progressUpdated && !stop && previousSession != nil && previousSession.IsPaused != req.IsPaused {
		updatedSession := *previousSession
		updatedSession.Position = positionSeconds
		updatedSession.IsPaused = req.IsPaused
		action := compatScrobbleStart
		if req.IsPaused {
			action = compatScrobblePause
		}
		h.dispatchCompatScrobbleAt(
			r.Context(), action, playSession, &updatedSession,
			findMediaSource(playSession, req.MediaSourceID), &positionSeconds,
		)
	}
	// Persist progress to user store
	if positionSeconds > 0 && h.storeProvider != nil && playSession.ItemID != "" {
		if store, storeErr := h.storeProvider.ForUser(r.Context(), session.StreamAppUserID); storeErr == nil {
			// Find the duration from the media source
			var duration float64
			for _, src := range playSession.MediaSources {
				if src.Version.Duration > 0 {
					duration = float64(src.Version.Duration)
					break
				}
			}
			if err := store.UpdateProgress(r.Context(), session.ProfileID, playSession.ItemID, positionSeconds, duration, h.playbackThresholds(r.Context())); err == nil {
				triggerProfileRefresh(r.Context(), h.profileStaler, h.profileRefreshRequester, session.StreamAppUserID, session.ProfileID)
			}
		}
	}
	if stop {
		// Direct ids and recorded aliases are per-play, caller-owned identifiers.
		// Route-only matching is intentionally excluded for Stopped reports: a
		// delayed stop for an earlier play of the same item must never tear down
		// the current play.
		source := findMediaSource(playSession, req.MediaSourceID)
		fallback := compatScrobbleFallbackSession(
			session, playSession, source, positionSeconds, positionReported, req.IsPaused,
		)
		var positionOverride *float64
		if positionReported {
			positionOverride = &positionSeconds
		}
		h.teardownPlaySession(r.Context(), playSession, fallback, positionOverride)
	}

	w.WriteHeader(http.StatusNoContent)
}

// upstreamRecipeCard returns the reconstruction recipe for a compat upstream
// session. A transcode carries its full recipe in the compat store
// (PlaybackSession.Recipe); direct/remux need only identity, rebuilt here from
// the compat session and the negotiated source.
func (h *PlaybackHandler) upstreamRecipeCard(ps *PlaybackSession, cs *Session, source PlaybackMediaSource, method string) playback.RecipeCard {
	if ps != nil && ps.Recipe != nil {
		return *ps.Recipe
	}
	if method == "remux" {
		return playback.NewRemuxRecipeCard(ps.UpstreamSessionID, cs.StreamAppUserID, cs.ProfileID, source.FileID, source.TranscodeAudio, compatAudioTrackIndexOrDefault(source))
	}
	return playback.NewDirectRecipeCard(ps.UpstreamSessionID, cs.StreamAppUserID, cs.ProfileID, source.FileID)
}

// reportMatchesPlaySession rejects an alias-resolved session whose item or
// media source contradicts the report, so a stale or reused client id cannot
// route a report (or its teardown) to the wrong play.
func reportMatchesPlaySession(playSession *PlaybackSession, req sessionReportRequest) bool {
	if req.ItemID != "" && !mediaSourceIDsEqual(playSession.RouteItemID, req.ItemID) {
		return false
	}
	if req.MediaSourceID != "" && findMediaSource(playSession, req.MediaSourceID) == nil {
		return false
	}
	return true
}

// reviveUpstreamForReport recreates the upstream playback session backing a
// progress report after stale cleanup reaped it. Returns nil when the play
// session has no usable media source or the recreation fails.
func (h *PlaybackHandler) reviveUpstreamForReport(ctx context.Context, session *Session, playSession *PlaybackSession, mediaSourceID string) *PlaybackSession {
	if playSession.UpstreamPlayMethod == "" {
		return nil
	}
	source := findMediaSource(playSession, mediaSourceID)
	if source == nil {
		source = firstMediaSource(playSession)
	}
	if source == nil {
		return nil
	}
	revived, err := h.ensureUpstreamPlayback(ctx, session, playSession.ID, *source, playSession.UpstreamPlayMethod)
	if err != nil {
		slog.WarnContext(ctx, "jellycompat upstream session revive failed", "component", "jellycompat",
			"play_session_id", playSession.ID,
			"upstream_session_id", playSession.UpstreamSessionID,
			"error", err,
		)
		return nil
	}
	return revived
}

func (h *PlaybackHandler) ensureUpstreamPlayback(ctx context.Context, compatSession *Session, playSessionID string, source PlaybackMediaSource, method string) (*PlaybackSession, error) {
	playSession, ok := h.playbackStore.Get(playSessionID)
	if !ok {
		return nil, ErrSessionNotFound
	}
	// Captured before any mutation: the CAS attach below verifies no concurrent
	// request replaced the upstream session this request observed.
	observedUpstreamID := playSession.UpstreamSessionID
	if h.sessionMgr == nil {
		return nil, fmt.Errorf("session manager not available")
	}
	if playSession.UpstreamSessionID != "" && playSession.UpstreamPlayMethod == method {
		// After a restart the durable play session survives but the in-memory
		// native session is gone; rebuild it from the recipe card so ownership and
		// accounting are restored before the transcode is (re)started.
		if _, err := h.sessionMgr.GetSession(playSession.UpstreamSessionID); err != nil {
			if !errors.Is(err, playback.ErrSessionNotFound) {
				return nil, err
			}
			if h.tm != nil {
				card := h.upstreamRecipeCard(playSession, compatSession, source, method)
				// Cards minted before client metadata was recorded (and the
				// direct/remux fallback cards built here from scratch) carry
				// none; the current compat request identifies the client, so
				// the reconstructed session keeps its label and JF pill.
				info := playback.ClientInfoFromContext(ctx)
				card.IsJellyfinCompat = info.IsCompat
				if card.ClientName == "" && card.ClientUserAgent == "" {
					card.ClientName, card.ClientVersion, card.ClientUserAgent = info.Name, info.Version, info.UserAgent
				}
				if reconstructed := h.tm.ReconstructSession(ctx, playSession.UpstreamSessionID, compatSession.StreamAppUserID, card); reconstructed != nil {
					if !playSession.ProgressPersistenceKnown ||
						playSession.DisableProgressPersistence != reconstructed.DisableProgressPersistence {
						h.recordCompatProgressPersistence(playSession.ID, reconstructed.DisableProgressPersistence)
					}
					_ = h.syncUpstreamAudioSelection(playSession, source)
					h.dispatchCompatScrobble(ctx, compatScrobbleStart, playSession, reconstructed, &source)
					return playSession, nil
				}
			}
			// The durable compat row outlived the native session and no recipe card
			// can rebuild it. Any transcode still keyed to the stale id must go
			// first, or a second ffmpeg would start alongside it. Then fall through
			// to create a fresh upstream session and persist the replacement
			// instead of serving under a stale ID.
			if h.tm != nil {
				h.tm.CloseTranscodeSession(playSession.UpstreamSessionID, "")
			}
			playSession.UpstreamSessionID = ""
			playSession.UpstreamPlayMethod = ""
			playSession.TranscodeStarted = false
		} else {
			if current, currentErr := h.sessionMgr.GetSession(playSession.UpstreamSessionID); currentErr == nil &&
				(!playSession.ProgressPersistenceKnown ||
					playSession.DisableProgressPersistence != current.DisableProgressPersistence) {
				h.recordCompatProgressPersistence(playSession.ID, current.DisableProgressPersistence)
			}
			_ = h.syncUpstreamAudioSelection(playSession, source)
			return playSession, nil
		}
	}

	var playMethod playback.PlayMethod
	transcodeAudio := source.TranscodeAudio
	switch method {
	case "direct":
		playMethod = playback.PlayDirect
		transcodeAudio = false
	case "remux":
		playMethod = playback.PlayRemux
	case "transcode":
		playMethod = playback.PlayTranscode
		transcodeAudio = false
	default:
		playMethod = playback.PlayDirect
		transcodeAudio = false
	}

	if playSession.UpstreamSessionID != "" && playSession.UpstreamPlayMethod != "" && playSession.UpstreamPlayMethod != method {
		oldUpstreamSessionID := playSession.UpstreamSessionID
		transcodeNodeURL := ""
		if current, err := h.sessionMgr.GetSession(oldUpstreamSessionID); err == nil {
			transcodeNodeURL = current.TranscodeNodeURL
			h.dispatchCompatScrobble(ctx, compatScrobbleStop, playSession, current, nil)
		}
		_ = h.sessionMgr.StopSession(oldUpstreamSessionID)
		h.tm.CloseTranscodeSession(oldUpstreamSessionID, transcodeNodeURL)
		// Method switch discards the old upstream session: drop its node recipe so
		// the abandoned id cannot reconstruct ffmpeg after a node restart. Best
		// effort and bounded — never block the new method's start on a store hiccup.
		if h.RecipeNodeStore != nil {
			delCtx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 2*time.Second)
			if err := h.RecipeNodeStore.Delete(delCtx, oldUpstreamSessionID); err != nil {
				slog.WarnContext(ctx, "delete node transcode recipe failed", "component", "jellycompat", "error", err,
					"playback_session_id", oldUpstreamSessionID)
			}
			cancel()
		}
	}

	var session *playback.Session
	var err error
	if starter, ok := h.sessionMgr.(sessionStarterContext); ok {
		session, err = starter.StartSessionWithContext(ctx, compatSession.StreamAppUserID, compatSession.ProfileID, source.FileID, playMethod, transcodeAudio)
	} else {
		session, err = h.sessionMgr.StartSession(compatSession.StreamAppUserID, compatSession.ProfileID, source.FileID, playMethod, transcodeAudio)
	}
	if err != nil {
		return nil, err
	}
	_ = h.syncUpstreamAudioSelection(&PlaybackSession{
		UpstreamSessionID:  session.ID,
		UpstreamPlayMethod: method,
	}, source)
	// Attach the new upstream session only if no concurrent request replaced
	// the one we observed (range requests race with progress-report revives).
	// The loser stops its session instead of leaving an orphan that counts
	// toward the user's stream limits until stale cleanup.
	if updateErr := h.playbackStore.Update(playSessionID, func(current *PlaybackSession) error {
		if current.UpstreamSessionID != observedUpstreamID {
			return errUpstreamReplaced
		}
		current.UpstreamSessionID = session.ID
		current.UpstreamPlayMethod = method
		current.TranscodeStarted = false
		current.ProgressPersistenceKnown = true
		current.DisableProgressPersistence = session.DisableProgressPersistence
		return nil
	}); updateErr != nil {
		_ = h.sessionMgr.StopSession(session.ID)
		if errors.Is(updateErr, errUpstreamReplaced) {
			// Adopt the winner only when it serves the same play method;
			// otherwise a concurrent method switch made this caller's
			// negotiated stream obsolete — surface the conflict rather than
			// continuing on a session with mismatched transcode bookkeeping.
			if winner, ok := h.playbackStore.Get(playSessionID); ok && winner.UpstreamPlayMethod == method {
				return winner, nil
			}
			return nil, errUpstreamReplaced
		}
		return nil, updateErr
	}
	updated, ok := h.playbackStore.Get(playSessionID)
	if !ok {
		return nil, ErrSessionNotFound
	}
	h.syncSessionsNow(ctx, "compat_start")
	h.dispatchCompatScrobble(ctx, compatScrobbleStart, updated, session, &source)
	return updated, nil
}

func (h *PlaybackHandler) recordCompatProgressPersistence(playSessionID string, disabled bool) {
	if h == nil || h.playbackStore == nil || playSessionID == "" {
		return
	}
	_ = h.playbackStore.Update(playSessionID, func(session *PlaybackSession) error {
		session.ProgressPersistenceKnown = true
		session.DisableProgressPersistence = disabled
		return nil
	})
}

func (h *PlaybackHandler) ensureTranscodeManifest(ctx context.Context, compatSession *Session, playSessionID string, source PlaybackMediaSource) ([]byte, error) {
	playSession, err := h.ensureUpstreamPlayback(ctx, compatSession, playSessionID, source, "transcode")
	if err != nil {
		return nil, err
	}

	transcodeSession, err := h.ensureTranscodeSession(ctx, playSessionID, playSession.UpstreamSessionID, source)
	if err != nil {
		requestErr := ctx.Err()
		if requestErr == nil || !errors.Is(err, requestErr) {
			h.teardownPlaySession(ctx, playSession, nil, nil)
		}
		return nil, err
	}

	// When the duration fits the shared segment-count bound, Jellycompat serves
	// its own synthetic VOD manifest. Longer media waits for FFmpeg's bounded
	// real playlist so one request cannot allocate hundreds of thousands of
	// segment entries.
	if shouldGenerateCompatFullManifest(source, h.compatSegmentDuration()) {
		return nil, nil
	}

	// Poll for manifest readiness so clients that don't retry on 503 (e.g. MPV/Streamyfin)
	// can still start playback. Typically ready within a few seconds.
	const maxWait = 30 * time.Second
	const pollInterval = 250 * time.Millisecond
	deadline := time.After(maxWait)
	for {
		manifest, err := transcodeSession.GetManifest()
		if err == nil {
			return playback.AlignRealManifestToSourceTimeline(manifest, transcodeSession.Opts(), "")
		}
		if !errors.Is(err, playback.ErrManifestNotReady) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			h.teardownPlaySession(ctx, playSession, nil, nil)
			return nil, playback.ErrManifestNotReady
		case <-time.After(pollInterval):
		}
	}
}

func (h *PlaybackHandler) ensureTranscodeSession(ctx context.Context, playSessionID, upstreamSessionID string, source PlaybackMediaSource) (*playback.TranscodeSession, error) {
	if existing := h.tm.GetTranscodeSession(upstreamSessionID); existing != nil {
		return existing, nil
	}
	// If a recipe survived in the compat store (e.g. a server restart), rebuild
	// the transcode from it — at the recipe's position — rather than starting
	// fresh at the original seek. On a first play there is no recipe yet, so this
	// is a no-op and we fall through to the normal start below.
	if h.playbackStore != nil {
		if ps, ok := h.playbackStore.Get(playSessionID); ok && ps.Recipe != nil {
			if reconstructed := h.tm.ReconstructTranscode(ctx, upstreamSessionID, -1, *ps.Recipe); reconstructed != nil {
				return reconstructed, nil
			}
		}
	}
	if !source.TranscodeAudio && is4KResolution(source.Version.Resolution) && !h.allow4KVideoTranscode(ctx) {
		return nil, errTranscode4KDisallowed
	}
	if h.fileResolver == nil {
		return nil, fmt.Errorf("file resolver not available")
	}

	file, err := h.fileResolver.GetByID(ctx, source.FileID)
	if err != nil {
		return nil, fmt.Errorf("resolve file: %w", err)
	}
	if err := os.MkdirAll(h.TranscodeDir, 0o755); err != nil {
		return nil, fmt.Errorf("prepare transcode dir: %w", err)
	}
	sourceVideoCodec, sourceVideoProfile, sourceVideoBitDepth := playback.SourceVideoTranscodeFacts(file)

	initialSeekSeconds := 0.0
	startSegmentNumber := 0
	if playSession, ok := h.playbackStore.Get(playSessionID); ok {
		initialSeekSeconds, startSegmentNumber = compatInitialTranscodePosition(
			source,
			h.compatSegmentDuration(),
			playSession.InitialSeekSeconds,
		)
	}

	opts := playback.TranscodeOpts{
		SessionID:           upstreamSessionID,
		InputPath:           file.FilePath,
		SourceVideoCodec:    sourceVideoCodec,
		SourceVideoProfile:  sourceVideoProfile,
		SourceVideoBitDepth: sourceVideoBitDepth,
		OutputDir:           filepath.Join(h.TranscodeDir, upstreamSessionID),
		SeekSeconds:         initialSeekSeconds,
		StartSegmentNumber:  startSegmentNumber,
		TargetCodecVideo:    compatTargetVideoCodec,
		TargetCodecAudio:    compatTargetAudioCodec,
		FFmpegPath:          h.FFmpegPath,
		HWAccel:             h.HWAccel,
		AudioTrackIndex:     compatAudioTrackIndexOrDefault(source),
		TotalDuration:       float64(source.Version.Duration),
		FastStart:           true,
	}
	if source.TranscodeAudio {
		opts.TargetCodecVideo = "copy"
	}
	opts.SegmentDuration = h.compatSegmentDuration()

	// Hold the per-session lifecycle lock across "check existing → spawn →
	// register" so a concurrent reconstruct (or another manifest request) cannot
	// run a second ffmpeg writer against this session's output dir. Re-check under
	// the lock and yield to any live session instead of spawning a duplicate.
	unlock := h.tm.LockSessionLifecycle(upstreamSessionID)
	if existing := h.tm.GetTranscodeSession(upstreamSessionID); existing != nil {
		unlock()
		return existing, nil
	}
	transcodeSession, err := playback.StartTranscode(context.WithoutCancel(ctx), opts)
	if err != nil {
		unlock()
		return nil, err
	}
	// Safe under the lifecycle lock: the re-check above held, so no other path
	// registered this session.
	h.tm.RegisterTranscodeSession(upstreamSessionID, transcodeSession)
	unlock()

	// Mirror the actual encode decisions onto the upstream session before the
	// recipe is persisted — video-copy HLS must not sync as a video transcode.
	h.recordTranscodeStreamDetails(ctx, upstreamSessionID, opts)

	// Register the exit monitor and persist the reconstruction recipe (shared with
	// the remote path). On a failed compat-store write roll back this abandoned
	// transcode rather than leaking it.
	h.tm.MonitorLocalTranscodeExit(upstreamSessionID, transcodeSession)

	if err := h.persistTranscodeRecipe(ctx, playSessionID, upstreamSessionID, opts); err != nil {
		h.tm.CloseTranscodeSession(upstreamSessionID, "")
		return nil, err
	}

	return transcodeSession, nil
}

func shouldGenerateCompatFullManifest(source PlaybackMediaSource, segmentDuration int) bool {
	return playback.CanGenerateSyntheticManifest(float64(source.Version.Duration), segmentDuration)
}

// compatInitialTranscodePosition keeps FFmpeg close to the requested resume
// position. Bounded synthetic manifests list the omitted source segments;
// seeked real manifests receive an EXT-X-GAP timeline anchor before serving.
func compatInitialTranscodePosition(source PlaybackMediaSource, segmentDuration int, requested float64) (float64, int) {
	if requested <= 0 {
		return 0, 0
	}
	if duration := float64(source.Version.Duration); duration > 0 && requested > duration {
		requested = duration
	}
	if segmentDuration <= 0 {
		segmentDuration = compatSegmentDuration
	}
	return requested, int(requested / float64(segmentDuration))
}

// audioSelectionChanged reports whether an incoming AudioStreamIndex differs
// from what the play session already records for the target media source.
// Used to short-circuit progress reports that merely echo the current
// selection — restarting ffmpeg for no-op updates causes segment churn and
// stalls the client player.
func audioSelectionChanged(session *PlaybackSession, mediaSourceID string, incomingStreamIndex int) bool {
	if session == nil || len(session.MediaSources) == 0 {
		return true
	}
	for _, source := range session.MediaSources {
		if mediaSourceID != "" && !mediaSourceIDsEqual(source.ID, mediaSourceID) {
			continue
		}
		if source.SelectedAudioStreamIndex == nil {
			return true
		}
		return *source.SelectedAudioStreamIndex != incomingStreamIndex
	}
	// Unknown media source — fall back to the original behavior.
	return true
}

func (h *PlaybackHandler) setSelectedAudioStream(playSessionID, mediaSourceID string, audioStreamIndex int) (*PlaybackSession, *PlaybackMediaSource, error) {
	var updatedSource PlaybackMediaSource
	if err := h.playbackStore.Update(playSessionID, func(current *PlaybackSession) error {
		sourceIndex := 0
		if mediaSourceID != "" {
			sourceIndex = -1
			for index := range current.MediaSources {
				if mediaSourceIDsEqual(current.MediaSources[index].ID, mediaSourceID) {
					sourceIndex = index
					break
				}
			}
		}
		if sourceIndex < 0 || sourceIndex >= len(current.MediaSources) {
			return ErrSessionNotFound
		}
		if !isValidCompatAudioStreamIndex(current.MediaSources[sourceIndex].Version, audioStreamIndex) {
			return fmt.Errorf("invalid compat audio stream index")
		}
		current.MediaSources[sourceIndex].SelectedAudioStreamIndex = intPtr(audioStreamIndex)
		updatedSource = current.MediaSources[sourceIndex]
		return nil
	}); err != nil {
		return nil, nil, err
	}

	updatedPlaySession, ok := h.playbackStore.Get(playSessionID)
	if !ok {
		return nil, nil, ErrSessionNotFound
	}
	return updatedPlaySession, &updatedSource, nil
}

func (h *PlaybackHandler) syncUpstreamAudioSelection(playSession *PlaybackSession, source PlaybackMediaSource) error {
	if h.sessionMgr == nil || playSession == nil || playSession.UpstreamSessionID == "" {
		return nil
	}
	audioTrackIndex, ok := compatAudioTrackIndex(source)
	if !ok {
		return nil
	}
	return h.sessionMgr.UpdateAudioTrack(
		playSession.UpstreamSessionID,
		audioTrackIndex,
		compatPlayMethod(playSession.UpstreamPlayMethod),
	)
}

func (h *PlaybackHandler) restartCompatTranscodeForAudioSelection(
	ctx context.Context,
	playSession *PlaybackSession,
	source PlaybackMediaSource,
	positionSeconds float64,
) (bool, error) {
	if playSession == nil || playSession.UpstreamSessionID == "" || playSession.UpstreamPlayMethod != "transcode" {
		return false, nil
	}

	audioTrackIndex, ok := compatAudioTrackIndex(source)
	if !ok {
		return false, nil
	}

	if transcodeSession := h.tm.GetTranscodeSession(playSession.UpstreamSessionID); transcodeSession != nil {
		transcodeSession.SetAudioTrackIndex(audioTrackIndex)
		startSegment := 0
		if segmentDuration := transcodeSession.Opts().SegmentDuration; segmentDuration > 0 && positionSeconds > 0 {
			startSegment = int(positionSeconds / float64(segmentDuration))
		}
		if err := h.tm.RestartSessionLocked(context.WithoutCancel(ctx), playSession.UpstreamSessionID, transcodeSession, positionSeconds, startSegment); err != nil {
			return false, err
		}
		// Re-persist the durable recipe so reconstruct after a central restart
		// rebuilds ffmpeg from the newly selected audio track rather than the
		// stale original. SetAudioTrackIndex mutated the live opts, so read them
		// back. Best-effort: a stale recipe only costs node-restart resilience,
		// not the live stream.
		opts := transcodeSession.Opts()
		if err := h.persistTranscodeRecipe(context.WithoutCancel(ctx), playSession.ID, playSession.UpstreamSessionID, opts); err != nil {
			slog.WarnContext(ctx, "persist audio-restarted transcode recipe", "component", "jellycompat", "error", err,
				"playback_session_id", playSession.ID)
		}
		return true, nil
	}

	if h.sessionMgr == nil {
		return false, nil
	}
	upstreamSession, err := h.sessionMgr.GetSession(playSession.UpstreamSessionID)
	if err != nil {
		return false, err
	}
	if upstreamSession.TranscodeNodeURL == "" {
		return false, nil
	}
	if h.fileResolver == nil {
		return false, fmt.Errorf("file resolver not available")
	}
	file, err := h.fileResolver.GetByID(ctx, source.FileID)
	if err != nil {
		return false, err
	}
	if err := h.startRemoteTranscode(context.WithoutCancel(ctx), playSession.ID, playSession.UpstreamSessionID, source, file, positionSeconds, upstreamSession.TranscodeNodeURL); err != nil {
		return false, err
	}
	return true, nil
}

func (h *PlaybackHandler) compatSegmentDuration() int {
	return compatSegmentDuration
}

// createStaticPlaySession builds an on-the-fly play session for Infuse-style
// Static=true direct play requests that skip PlaybackInfo. clientPlaySessionID
// is the client's own PlaySessionId (if it sent one) so later playback reports
// carrying it can resolve this session directly.
func (h *PlaybackHandler) createStaticPlaySession(ctx context.Context, session *Session, routeID, mediaSourceID, clientPlaySessionID string) (*PlaybackSession, *PlaybackMediaSource, error) {
	contentID, err := decodeContentID(h.codec, routeID)
	if err != nil {
		return nil, nil, ErrSessionNotFound
	}
	detail, err := h.content.GetItemDetail(ctx, session, contentID, nil)
	if err != nil || detail == nil || len(detail.Versions) == 0 {
		return nil, nil, ErrSessionNotFound
	}

	playSessionID := h.codec.EncodeStringID(EncodedIDPlaySession, uuidNewString())
	sources := make([]PlaybackMediaSource, 0, len(detail.Versions))
	allow4KTranscode := h.allow4KVideoTranscode(ctx)
	for _, version := range detail.Versions {
		source := h.buildPlaybackSource(routeID, playSessionID, version, DeviceProfile{}, playbackInfoRequest{}, allow4KTranscode)
		sources = append(sources, source)
	}

	ps := &PlaybackSession{
		ID:                  playSessionID,
		CompatToken:         session.Token,
		ItemID:              detail.ContentID,
		RouteItemID:         routeID,
		ClientPlaySessionID: clientPlaySessionID,
		UserID:              session.PseudoUserID.String(),
		MediaSources:        sources,
	}
	h.playbackStore.Put(*ps)

	var matched *PlaybackMediaSource
	if mediaSourceID != "" {
		matched = findMediaSource(ps, mediaSourceID)
	}
	if matched == nil {
		matched = firstMediaSource(ps)
	}
	return ps, matched, nil
}

func (h *PlaybackHandler) resolvePlaybackRoute(r *http.Request, compatSession *Session, routeID, mediaSourceID string) (*PlaybackSession, *PlaybackMediaSource, error) {
	clientPlaySessionID := newCaseInsensitiveQuery(r.URL.Query()).Get("PlaySessionId")
	if clientPlaySessionID != "" {
		if playSession, ok := h.playbackStore.Get(clientPlaySessionID); ok && playSession.CompatToken == compatSession.Token {
			// Fall back to the primary source only for the Jellyfin
			// MediaSource.Id == Item.Id convention: a client that reused the
			// server's PlaySessionId may send the item id (== routeID) as
			// mediaSourceId, which never matches Silo's fileID-based source ids.
			// Any other unmatched id (stale/foreign, or a wrong multi-version
			// id) keeps source nil so HandleVideoStream rejects it rather than
			// silently serving the wrong file. Mirrors Jellyfin's
			// StreamingHelpers, which defaults to the primary source only for an
			// empty or item-id mediaSourceId.
			source := findMediaSource(playSession, mediaSourceID)
			if source == nil && (mediaSourceID == "" || mediaSourceIDsEqual(mediaSourceID, routeID)) {
				source = firstMediaSource(playSession)
			}
			return playSession, source, nil
		}
		// The PlaySessionId is unknown to us (the client never called PlaybackInfo,
		// so it is the client's own id) or belongs to another caller. Fall through
		// to route-based reuse below instead of erroring: a Static=true direct play
		// repeats this same client id on every range request, and minting a fresh,
		// separately stream-capped upstream session each time piles up orphaned
		// sessions that trip the per-user stream limit (429). Route reuse keeps one
		// session per direct play. (Reuse stays scoped to this caller's CompatToken
		// via FindByRoute, so a guessed/foreign id cannot bind another user's session.)
	}

	playSession, source, ok := h.playbackStore.FindByRoute(compatSession.Token, routeID)
	if !ok {
		return nil, nil, ErrSessionNotFound
	}
	if clientPlaySessionID != "" && playSession.ClientPlaySessionID != clientPlaySessionID {
		// Remember the client's own PlaySessionId so playback reports carrying
		// it resolve to this session directly instead of by ambiguous route.
		if h.playbackStore.Update(playSession.ID, func(current *PlaybackSession) error {
			current.ClientPlaySessionID = clientPlaySessionID
			return nil
		}) == nil {
			playSession.ClientPlaySessionID = clientPlaySessionID
		}
	}
	if source == nil && mediaSourceID != "" {
		source = findMediaSource(playSession, mediaSourceID)
	}
	if source == nil {
		source = firstMediaSource(playSession)
	}
	return playSession, source, nil
}

func firstMediaSource(session *PlaybackSession) *PlaybackMediaSource {
	if session == nil || len(session.MediaSources) == 0 {
		return nil
	}
	source := session.MediaSources[0]
	return &source
}

func findMediaSource(session *PlaybackSession, mediaSourceID string) *PlaybackMediaSource {
	if session == nil {
		return nil
	}
	for _, source := range session.MediaSources {
		if mediaSourceIDsEqual(source.ID, mediaSourceID) {
			copy := source
			return &copy
		}
	}
	return nil
}

func compatPlayMethod(method string) playback.PlayMethod {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "remux":
		return playback.PlayRemux
	case "transcode":
		return playback.PlayTranscode
	default:
		return playback.PlayDirect
	}
}

func rewriteManifest(manifest []byte, routeItemID, playlistID, mediaSourceID string) []byte {
	var out strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(string(manifest)))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "#EXT-X-MAP:URI=\""):
			prefix := "#EXT-X-MAP:URI=\""
			uri := strings.TrimSuffix(strings.TrimPrefix(line, prefix), "\"")
			line = prefix + buildSegmentProxyPath(routeItemID, playlistID, mediaSourceID, uri) + "\""
		case line != "" && !strings.HasPrefix(line, "#"):
			line = buildSegmentProxyPath(routeItemID, playlistID, mediaSourceID, line)
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return []byte(out.String())
}

func buildSegmentProxyPath(routeItemID, playlistID, mediaSourceID, current string) string {
	base := path.Base(current)
	query := url.Values{}
	if parsed, err := url.Parse(current); err == nil {
		base = path.Base(parsed.Path)
		query = parsed.Query()
	}
	query.Set("PlaySessionId", playlistID)
	if mediaSourceID != "" {
		query.Set("MediaSourceId", mediaSourceID)
	}
	qs := "?" + query.Encode()
	if base == "stream.m3u8" {
		return fmt.Sprintf("/Videos/%s/hls/%s/stream.m3u8%s", routeItemID, playlistID, qs)
	}
	if strings.Contains(base, ".") {
		ext := path.Ext(base)
		name := strings.TrimSuffix(base, ext)
		return fmt.Sprintf("/Videos/%s/hls/%s/%s%s%s", routeItemID, playlistID, name, ext, qs)
	}
	return fmt.Sprintf("/Videos/%s/hls/%s/%s%s", routeItemID, playlistID, base, qs)
}

func copyProxyResponse(w http.ResponseWriter, resp *http.Response) {
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func chiURLParam(r *http.Request, key string) string {
	return chi.URLParam(r, key)
}

func seekSecondsFromTicks(seekStr string) float64 {
	if seekStr == "" {
		return 0
	}
	ticks, err := strconv.ParseInt(seekStr, 10, 64)
	if err != nil {
		return 0
	}
	return float64(ticks) / 10_000_000
}

// computeDownloadedSubBaseIndex returns the first index available for downloaded subtitles.
// This mirrors how buildMediaStreams assigns indices in handlers_playback.go:
// video tracks → audio tracks → subtitle tracks (using ffprobe index or positional index).
func computeDownloadedSubBaseIndex(file *models.MediaFile) int {
	maxIndex := -1

	// Check video tracks — indexed positionally starting at 0.
	for i := range file.VideoTracks {
		if i > maxIndex {
			maxIndex = i
		}
	}

	// Check audio tracks — indexed after video tracks.
	for i := range file.AudioTracks {
		idx := len(file.VideoTracks) + i
		if idx > maxIndex {
			maxIndex = idx
		}
	}

	// Check embedded subtitle tracks — they may use ffprobe indices (track.Index)
	// which can be non-sequential. Fall back to positional when Index is 0.
	for i, track := range file.SubtitleTracks {
		var idx int
		if track.Index > 0 {
			idx = track.Index
		} else {
			idx = len(file.VideoTracks) + len(file.AudioTracks) + i
		}
		if idx > maxIndex {
			maxIndex = idx
		}
	}

	// Check external subtitles — indexed after all embedded subtitle entries,
	// mirroring buildVersionSubtitleTracks + subtitleTrackIndex in PlaybackInfo.
	for i := range file.ExternalSubtitles {
		idx := externalSubtitleRouteIndex(file, i)
		if idx > maxIndex {
			maxIndex = idx
		}
	}

	return maxIndex + 1
}

// generateFullManifest builds a complete VOD-style HLS manifest covering the
// entire video duration. This allows clients to seek to any position even
// though segments may not have been transcoded yet.
//
// When startTimeOffsetSeconds > 0 (resume), the playlist still lists every
// segment but emits #EXT-X-START:TIME-OFFSET so the player begins playback at
// the resume position. Trimming the playlist to seg_K..seg_(N-1) instead would
// confuse clients that apply their own initial seek (Jellyfin Android TV's
// ExoPlayer): playlist-time and source-time would diverge, and seekTo(K*segDur)
// would land on seg_2K. The full-playlist + START tag form keeps the two
// timelines aligned for every client.
func generateFullManifest(durationSeconds, segDuration int, fmp4 bool, startTimeOffsetSeconds float64) []byte {
	if durationSeconds <= 0 {
		durationSeconds = 1
	}
	if segDuration <= 0 {
		segDuration = compatSegmentDuration
	}

	numSegments := int(math.Ceil(float64(durationSeconds) / float64(segDuration)))
	if startTimeOffsetSeconds < 0 || startTimeOffsetSeconds >= float64(durationSeconds) {
		startTimeOffsetSeconds = 0
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	// EXT-X-START is a HLS protocol-version-6 tag, so a TS playlist that
	// emits it must advertise at least version 6 or strict clients can
	// reject the playlist — defeating the very resume case this code path
	// is for. fmp4 already requires version 7.
	hlsVersion := 3
	switch {
	case fmp4:
		hlsVersion = 7
	case startTimeOffsetSeconds > 0:
		hlsVersion = 6
	}
	b.WriteString(fmt.Sprintf("#EXT-X-VERSION:%d\n", hlsVersion))
	b.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", segDuration))
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	if startTimeOffsetSeconds > 0 {
		b.WriteString(fmt.Sprintf("#EXT-X-START:TIME-OFFSET=%.6f,PRECISE=YES\n", startTimeOffsetSeconds))
	}
	if fmp4 {
		b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
	}

	remaining := float64(durationSeconds)
	for i := range numSegments {
		segLen := math.Min(float64(segDuration), remaining)
		b.WriteString(fmt.Sprintf("#EXTINF:%.6f,\n", segLen))
		if fmp4 {
			b.WriteString(fmt.Sprintf("seg_%05d.m4s\n", i))
		} else {
			b.WriteString(fmt.Sprintf("seg_%05d.ts\n", i))
		}
		remaining -= segLen
	}

	b.WriteString("#EXT-X-ENDLIST\n")
	return []byte(b.String())
}
