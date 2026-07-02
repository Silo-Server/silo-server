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
)

// Jellyfin Web is sensitive to startup latency. Use shorter compat segments
// than the native global playback default so the first requested HLS chunk and
// the near-head follow-up segments arrive quickly enough for browser playback.
const compatSegmentDuration = 2

type sessionReportRequest struct {
	ItemID              string          `json:"ItemId"`
	MediaSourceID       string          `json:"MediaSourceId"`
	PlaySessionID       string          `json:"PlaySessionId"`
	PositionTicks       int64           `json:"PositionTicks"`
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
		playSession, source, err = h.createStaticPlaySession(r.Context(), session, routeID, mediaSourceID)
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

	switch method {
	case "remux":
		audioTrackIndex := -1
		if resolvedAudioTrackIndex, ok := compatAudioTrackIndex(*source); ok {
			audioTrackIndex = resolvedAudioTrackIndex
		}
		_ = playback.ServeRemux(w, r, file.FilePath, "mp4", seekSeconds, source.TranscodeAudio, audioTrackIndex)
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
		upstreamSession, upstreamErr := h.sessionMgr.GetSession(playSession.UpstreamSessionID)
		if upstreamErr == nil {
			plan := h.NodePlanner.PlanSession(playSession.UpstreamSessionID, upstreamSession.TranscodeNodeURL, true, source.Version.Bitrate)
			if tcNode := plan.TranscodeNode; tcNode != nil {
				if h.fileResolver == nil {
					writeError(w, http.StatusInternalServerError, "ServerError", "File resolver not available")
					return
				}
				file, fileErr := h.fileResolver.GetByID(r.Context(), source.FileID)
				if fileErr != nil {
					writeError(w, http.StatusNotFound, "NotFound", "Media file not found")
					return
				}
				if err := h.sessionMgr.SetTranscodeNodeURL(playSession.UpstreamSessionID, tcNode.URL); err != nil {
					writeError(w, http.StatusInternalServerError, "ServerError", "Failed to bind transcode node")
					return
				}
				if err := h.startRemoteTranscode(r.Context(), playSession.UpstreamSessionID, *source, file, playSession.InitialSeekSeconds, tcNode.URL); err != nil {
					if errors.Is(err, errTranscode4KDisallowed) {
						writeError(w, http.StatusForbidden, "Forbidden", "4K video transcoding is disabled on this server")
						return
					}
					writeError(w, http.StatusBadGateway, "TranscodeStartFailed", "Transcode node rejected the request")
					return
				}
				redirectURL, redirectErr := h.buildProxyRedirectURL(playSession.ID, playSession.UpstreamSessionID, string(playback.PlayTranscode), file, *source, tcNode.URL, 0, plan.ProxyNode)
				if redirectErr != nil {
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
		writeError(w, http.StatusServiceUnavailable, "NoTranscodeNode",
			"No transcode node is available and local transcode fallback is disabled")
		return
	}

	// Ensure the transcode process is running.
	_, err = h.ensureTranscodeManifest(r.Context(), session, playSession.ID, *source)
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

	manifest := generateFullManifest(source.Version.Duration, segDuration, source.TranscodeAudio, playSession.InitialSeekSeconds)

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
	_, err := h.ensureTranscodeManifest(r.Context(), session, playSession.ID, *source)
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

	manifest := generateFullManifest(source.Version.Duration, segDuration, source.TranscodeAudio, playSession.InitialSeekSeconds)
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

	upstreamSession, err := h.sessionMgr.GetSession(playSession.UpstreamSessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NotFound", "Upstream session not found")
		return
	}
	_ = upstreamSession // session exists, serve the segment

	transcodeSession := h.getTranscodeSession(playSession.UpstreamSessionID)
	if transcodeSession == nil {
		writeError(w, http.StatusNotFound, "NotFound", "Transcode session not found")
		return
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
			slog.Info("transcode segment missing",
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
				slog.Info("transcode segment wait",
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
					slog.Info("transcode segment wait timeout",
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
					slog.Error("resolve transcode seek target",
						"error", seekErr,
						"segment", segmentFile,
						"play_session", playSessionID,
						"session", playSession.UpstreamSessionID,
						"playback_session_id", playSession.UpstreamSessionID,
					)
				}

				if ok {
					slog.Info("transcode seek restart",
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

					if restartErr := transcodeSession.Restart(
						context.WithoutCancel(r.Context()),
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
	return len(file.VideoTracks) + len(file.AudioTracks) + len(file.SubtitleTracks) + ordinal
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

	h.teardownPlaySession(r.Context(), playSession)

	w.WriteHeader(http.StatusNoContent)
}

// teardownPlaySession stops the upstream playback session and removes the compat
// play session from the store. Every step is idempotent, so it is safe to call
// from both the explicit ActiveEncodings teardown and a Stopped playback report
// (which may race). It is a no-op-safe teardown for an already-gone session.
func (h *PlaybackHandler) teardownPlaySession(ctx context.Context, playSession *PlaybackSession) {
	transcodeNodeURL := ""
	if h.sessionMgr != nil {
		if upstreamSession, err := h.sessionMgr.GetSession(playSession.UpstreamSessionID); err == nil {
			transcodeNodeURL = upstreamSession.TranscodeNodeURL
		}
	}
	h.closeTranscodeSession(playSession.UpstreamSessionID, transcodeNodeURL)
	if h.sessionMgr != nil {
		_ = h.sessionMgr.StopSession(playSession.UpstreamSessionID)
	}
	h.playbackStore.Delete(playSession.ID)
	// Clients often drop the connection right after reporting a stop, so detach
	// the sync from request cancellation to keep the admin view accurate.
	h.syncSessionsNow(context.WithoutCancel(ctx), "compat_stop")
}

// compatSessionSyncTimeout bounds the immediate session sync issued from
// request paths, so a stalled database degrades to the periodic reconciler
// tick instead of pinning request goroutines.
const compatSessionSyncTimeout = 5 * time.Second

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
		slog.Error("jellycompat: failed to sync sessions", "reason", reason, "error", err)
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

	playSession, ok := h.playbackStore.Get(req.PlaySessionID)
	if !ok || playSession.CompatToken != session.Token || playSession.UpstreamSessionID == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	positionSeconds := float64(req.PositionTicks) / 10_000_000
	audioTrackIndex := 0
	audioRestarted := false
	// Jellyfin web/mobile clients send AudioStreamIndex on every progress
	// report, not just on track changes. Restarting ffmpeg on each report
	// (every ~10s) tears down segments the player is still appending and
	// causes an hls.js retry loop. Only act when the index actually changes.
	if req.AudioStreamIndex != nil && audioSelectionChanged(playSession, req.MediaSourceID, int(*req.AudioStreamIndex)) {
		selectedAudioStreamIndex := int(*req.AudioStreamIndex)
		updatedPlaySession, updatedSource, updateErr := h.setSelectedAudioStream(req.PlaySessionID, req.MediaSourceID, selectedAudioStreamIndex)
		if updateErr == nil {
			playSession = updatedPlaySession
			if resolvedAudioTrackIndex, ok := compatAudioTrackIndex(*updatedSource); ok {
				audioTrackIndex = resolvedAudioTrackIndex
			}
			if syncErr := h.syncUpstreamAudioSelection(playSession, *updatedSource); syncErr != nil {
				slog.Warn("jellycompat audio selection sync failed",
					"play_session_id", req.PlaySessionID,
					"upstream_session_id", playSession.UpstreamSessionID,
					"error", syncErr,
				)
			}
			restarted, restartErr := h.restartCompatTranscodeForAudioSelection(r.Context(), playSession, *updatedSource, positionSeconds)
			if restartErr != nil {
				slog.Warn("jellycompat audio selection restart failed",
					"play_session_id", req.PlaySessionID,
					"upstream_session_id", playSession.UpstreamSessionID,
					"error", restartErr,
				)
			}
			audioRestarted = restarted
			slog.Info("jellycompat audio selection updated",
				"play_session_id", req.PlaySessionID,
				"media_source_id", updatedSource.ID,
				"audio_stream_index", selectedAudioStreamIndex,
				"audio_track_index", audioTrackIndex,
				"transcode_restarted", audioRestarted,
			)
		}
	}
	if positionSeconds > 0 && h.sessionMgr != nil {
		_ = h.sessionMgr.UpdateProgress(playSession.UpstreamSessionID, positionSeconds, req.IsPaused)
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
		h.teardownPlaySession(r.Context(), playSession)
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *PlaybackHandler) ensureUpstreamPlayback(ctx context.Context, compatSession *Session, playSessionID string, source PlaybackMediaSource, method string) (*PlaybackSession, error) {
	playSession, ok := h.playbackStore.Get(playSessionID)
	if !ok {
		return nil, ErrSessionNotFound
	}
	if playSession.UpstreamSessionID != "" && playSession.UpstreamPlayMethod == method {
		_ = h.syncUpstreamAudioSelection(playSession, source)
		return playSession, nil
	}

	if h.sessionMgr == nil {
		return nil, fmt.Errorf("session manager not available")
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
		transcodeNodeURL := ""
		if current, err := h.sessionMgr.GetSession(playSession.UpstreamSessionID); err == nil {
			transcodeNodeURL = current.TranscodeNodeURL
		}
		_ = h.sessionMgr.StopSession(playSession.UpstreamSessionID)
		h.closeTranscodeSession(playSession.UpstreamSessionID, transcodeNodeURL)
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
	if err := h.playbackStore.Update(playSessionID, func(current *PlaybackSession) error {
		current.UpstreamSessionID = session.ID
		current.UpstreamPlayMethod = method
		current.TranscodeStarted = false
		return nil
	}); err != nil {
		return nil, err
	}
	updated, ok := h.playbackStore.Get(playSessionID)
	if !ok {
		return nil, ErrSessionNotFound
	}
	h.syncSessionsNow(ctx, "compat_start")
	return updated, nil
}

func (h *PlaybackHandler) ensureTranscodeManifest(ctx context.Context, compatSession *Session, playSessionID string, source PlaybackMediaSource) ([]byte, error) {
	playSession, err := h.ensureUpstreamPlayback(ctx, compatSession, playSessionID, source, "transcode")
	if err != nil {
		return nil, err
	}

	transcodeSession, err := h.ensureTranscodeSession(ctx, playSessionID, playSession.UpstreamSessionID, source)
	if err != nil {
		return nil, err
	}

	// When duration is known, Jellycompat serves its own synthetic VOD manifest.
	// We only need ffmpeg running; waiting for startup segments here adds
	// unnecessary latency before the player can request the actual target segment.
	if source.Version.Duration > 0 {
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
			return manifest, nil
		}
		if !errors.Is(err, playback.ErrManifestNotReady) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, playback.ErrManifestNotReady
		case <-time.After(pollInterval):
		}
	}
}

func (h *PlaybackHandler) ensureTranscodeSession(ctx context.Context, playSessionID, upstreamSessionID string, source PlaybackMediaSource) (*playback.TranscodeSession, error) {
	if existing := h.getTranscodeSession(upstreamSessionID); existing != nil {
		return existing, nil
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

	h.transcodeMu.Lock()
	if existing := h.transcodes[upstreamSessionID]; existing != nil {
		h.transcodeMu.Unlock()
		return existing, nil
	}

	initialSeekSeconds := 0.0
	startSegmentNumber := 0
	if playSession, ok := h.playbackStore.Get(playSessionID); ok {
		initialSeekSeconds = playSession.InitialSeekSeconds
		segDuration := h.compatSegmentDuration()
		if initialSeekSeconds > 0 && segDuration > 0 {
			startSegmentNumber = int(initialSeekSeconds / float64(segDuration))
		}
	}

	opts := playback.TranscodeOpts{
		SessionID:          upstreamSessionID,
		InputPath:          file.FilePath,
		OutputDir:          filepath.Join(h.TranscodeDir, upstreamSessionID),
		SeekSeconds:        initialSeekSeconds,
		StartSegmentNumber: startSegmentNumber,
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		FFmpegPath:         h.FFmpegPath,
		HWAccel:            h.HWAccel,
		AudioTrackIndex:    compatAudioTrackIndexOrDefault(source),
		FastStart:          true,
	}
	if source.TranscodeAudio {
		opts.TargetCodecVideo = "copy"
	}
	opts.SegmentDuration = h.compatSegmentDuration()

	transcodeSession, err := playback.StartTranscode(context.WithoutCancel(ctx), opts)
	if err != nil {
		h.transcodeMu.Unlock()
		return nil, err
	}
	h.transcodes[upstreamSessionID] = transcodeSession
	h.transcodeMu.Unlock()

	if err := h.playbackStore.Update(playSessionID, func(current *PlaybackSession) error {
		current.TranscodeStarted = true
		return nil
	}); err != nil {
		closeErr := transcodeSession.Close()
		h.transcodeMu.Lock()
		delete(h.transcodes, upstreamSessionID)
		h.transcodeMu.Unlock()
		if closeErr != nil {
			return nil, fmt.Errorf("update playback session: %w (cleanup: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("update playback session: %w", err)
	}

	return transcodeSession, nil
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

	if transcodeSession := h.getTranscodeSession(playSession.UpstreamSessionID); transcodeSession != nil {
		transcodeSession.SetAudioTrackIndex(audioTrackIndex)
		startSegment := 0
		if segmentDuration := transcodeSession.Opts().SegmentDuration; segmentDuration > 0 && positionSeconds > 0 {
			startSegment = int(positionSeconds / float64(segmentDuration))
		}
		if err := transcodeSession.Restart(context.WithoutCancel(ctx), positionSeconds, startSegment); err != nil {
			return false, err
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
	if err := h.startRemoteTranscode(context.WithoutCancel(ctx), playSession.UpstreamSessionID, source, file, positionSeconds, upstreamSession.TranscodeNodeURL); err != nil {
		return false, err
	}
	return true, nil
}

func (h *PlaybackHandler) compatSegmentDuration() int {
	return compatSegmentDuration
}

// createStaticPlaySession builds an on-the-fly play session for Infuse-style
// Static=true direct play requests that skip PlaybackInfo.
func (h *PlaybackHandler) createStaticPlaySession(ctx context.Context, session *Session, routeID, mediaSourceID string) (*PlaybackSession, *PlaybackMediaSource, error) {
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
		ID:           playSessionID,
		CompatToken:  session.Token,
		ItemID:       detail.ContentID,
		RouteItemID:  routeID,
		UserID:       session.PseudoUserID.String(),
		MediaSources: sources,
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
	if playSessionID := newCaseInsensitiveQuery(r.URL.Query()).Get("PlaySessionId"); playSessionID != "" {
		if playSession, ok := h.playbackStore.Get(playSessionID); ok && playSession.CompatToken == compatSession.Token {
			if mediaSourceID != "" {
				source := findMediaSource(playSession, mediaSourceID)
				return playSession, source, nil
			}
			return playSession, firstMediaSource(playSession), nil
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

func (h *PlaybackHandler) getTranscodeSession(sessionID string) *playback.TranscodeSession {
	h.transcodeMu.RLock()
	defer h.transcodeMu.RUnlock()
	return h.transcodes[sessionID]
}

func (h *PlaybackHandler) closeTranscodeSession(sessionID, transcodeNodeURL string) {
	h.transcodeMu.Lock()
	session := h.transcodes[sessionID]
	delete(h.transcodes, sessionID)
	h.transcodeMu.Unlock()

	if session != nil {
		_ = session.Close()
	}
	if transcodeNodeURL != "" && h.JWTSecret != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, transcodeNodeURL+"/transcode/"+sessionID, nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+h.JWTSecret)
			if resp, doErr := http.DefaultClient.Do(req); doErr == nil {
				resp.Body.Close()
			}
		}
	}
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
