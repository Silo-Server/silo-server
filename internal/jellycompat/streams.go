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
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// Jellyfin Web is sensitive to startup latency. Use shorter compat segments
// than the native global playback default so the first requested HLS chunk and
// the near-head follow-up segments arrive quickly enough for browser playback.
const (
	compatSegmentDuration    = 2
	compatHLSPathSegment     = "hls"
	compatAudioV2PathSegment = "audio-v2"
	compatRemuxV1PathSegment = "remux-v1"
)

type compatAudioV2RouteContextKey struct{}
type compatRemuxV1RouteContextKey struct{}

func withCompatAudioV2Route(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), compatAudioV2RouteContextKey{}, true))
}

func isCompatAudioV2Route(r *http.Request) bool {
	marked, _ := r.Context().Value(compatAudioV2RouteContextKey{}).(bool)
	return marked
}

func withCompatRemuxV1Route(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), compatRemuxV1RouteContextKey{}, true))
}

func isCompatRemuxV1Route(r *http.Request) bool {
	marked, _ := r.Context().Value(compatRemuxV1RouteContextKey{}).(bool)
	return marked
}

func validateCompatAudioV2Route(w http.ResponseWriter, r *http.Request, required bool) bool {
	if isCompatAudioV2Route(r) == required {
		return true
	}
	writeError(w, http.StatusNotFound, "NotFound", "Playback route not found")
	return false
}

func validateCompatRemuxV1Route(w http.ResponseWriter, r *http.Request, required bool) bool {
	if isCompatRemuxV1Route(r) == required {
		return true
	}
	writeError(w, http.StatusNotFound, "NotFound", "Playback route not found")
	return false
}

func validateCompatAudioV2RouteIdentity(
	w http.ResponseWriter,
	r *http.Request,
	playSession *PlaybackSession,
	source *PlaybackMediaSource,
	routeItemID, routeMediaSourceID string,
) bool {
	if !isCompatAudioV2Route(r) {
		return true
	}
	if playSession == nil || source == nil || routeItemID == "" || routeMediaSourceID == "" ||
		!mediaSourceIDsEqual(playSession.RouteItemID, routeItemID) ||
		!mediaSourceIDsEqual(source.ID, routeMediaSourceID) {
		writeError(w, http.StatusNotFound, "NotFound", "Playback route not found")
		return false
	}
	return true
}

func compatProgressiveRequiresAudioV2(source PlaybackMediaSource, method string) bool {
	return method == string(playback.PlayRemux) && source.TranscodeAudio && compatSourceHasSurroundAudio(source)
}

func compatHLSRequiresAudioV2(source PlaybackMediaSource) bool {
	// Every compatibility HLS encode targets AAC, including a full video
	// transcode where TranscodeAudio is false. Audio-copy remuxes use their own
	// route version. Version AAC recipes when any selectable track can need v2:
	// audio selection changes do not mint a new playlist URL, so selected-track-
	// only routing would permit a later switch to cross back onto an old API pod.
	return compatHLSTranscodesAudio(source) && compatSourceHasSurroundAudio(source)
}

func compatHLSUsesAudioCopyV1(source PlaybackMediaSource) bool {
	return source.HLSRemux && !source.TranscodeAudio
}

func compatHLSRoutePathSegment(source PlaybackMediaSource) string {
	if compatHLSUsesAudioCopyV1(source) {
		return compatRemuxV1PathSegment
	}
	if compatHLSRequiresAudioV2(source) {
		return compatAudioV2PathSegment
	}
	return ""
}

func compatHLSCopiesVideo(source PlaybackMediaSource) bool {
	// TranscodeAudio implied the legacy fMP4/video-copy route before HLSRemux
	// was stored explicitly. Keep that interpretation for durable sessions
	// negotiated by an older process during a rolling deployment.
	return source.HLSRemux || source.TranscodeAudio
}

func compatHLSTranscodesAudio(source PlaybackMediaSource) bool {
	if compatHLSCopiesVideo(source) {
		return source.TranscodeAudio
	}
	return true
}

func compatHLSRecipeSourceAudioChannels(source PlaybackMediaSource) int {
	if !compatHLSTranscodesAudio(source) {
		return 0
	}
	return compatSourceAudioChannels(source)
}

func compatSourceHasSurroundAudio(source PlaybackMediaSource) bool {
	for _, track := range source.Version.AudioTracks {
		if track.Channels > 2 {
			return true
		}
	}
	return false
}

func compatRecipeMatchesSource(recipe *playback.RecipeCard, source PlaybackMediaSource) bool {
	return recipe != nil &&
		recipe.MediaFileID == source.FileID &&
		recipe.AudioTrackIndex == compatAudioTrackIndexOrDefault(source) &&
		recipe.SourceAudioChannels == compatHLSRecipeSourceAudioChannels(source)
}

// Versioned wrappers put a literal path segment in every byte URL whose
// execution depends on audio_to_aac@2. An older router has no matching shape
// and returns 404; current handlers additionally validate the persisted source
// before delegating to any side-effecting work.
func (h *PlaybackHandler) HandleAudioV2VideoStream(w http.ResponseWriter, r *http.Request) {
	h.HandleVideoStream(w, withCompatAudioV2Route(r))
}

func (h *PlaybackHandler) HandleAudioV2MasterManifest(w http.ResponseWriter, r *http.Request) {
	h.HandleMasterManifest(w, withCompatAudioV2Route(r))
}

func (h *PlaybackHandler) HandleAudioV2HLSManifest(w http.ResponseWriter, r *http.Request) {
	h.HandleHLSManifest(w, withCompatAudioV2Route(r))
}

func (h *PlaybackHandler) HandleAudioV2HLSSegment(w http.ResponseWriter, r *http.Request) {
	h.HandleHLSSegment(w, withCompatAudioV2Route(r))
}

func (h *PlaybackHandler) HandleRemuxV1MasterManifest(w http.ResponseWriter, r *http.Request) {
	h.HandleMasterManifest(w, withCompatRemuxV1Route(r))
}

func (h *PlaybackHandler) HandleRemuxV1HLSManifest(w http.ResponseWriter, r *http.Request) {
	h.HandleHLSManifest(w, withCompatRemuxV1Route(r))
}

func (h *PlaybackHandler) HandleRemuxV1HLSSegment(w http.ResponseWriter, r *http.Request) {
	h.HandleHLSSegment(w, withCompatRemuxV1Route(r))
}

// errUpstreamReplaced signals that a concurrent request attached a different
// upstream session to the play session while this one was being created.
var errUpstreamReplaced = errors.New("upstream session replaced concurrently")

// errAudioDownmixCapabilityUnavailable keeps the exact audio_to_aac recipe
// probe failure internal while allowing Jellyfin transports to return a
// retryable unavailable response instead of starting an incompatible FFmpeg
// filter graph.
var errAudioDownmixCapabilityUnavailable = errors.New("audio downmix capability is temporarily unavailable")

// errCompatRecipeSourceMismatch prevents a segment request from reconstructing
// bytes for an earlier media source or audio selection after durable state has
// moved on. The client may retry through the master route, which builds and
// persists a fresh recipe from the frozen source.
var errCompatRecipeSourceMismatch = errors.New("transcode recipe does not match the selected media source")

// errCompatHLSRemuxAudioUnsupported prevents an audio-copy playlist from
// switching to a track the negotiated device profile cannot carry in fMP4.
// The playlist route freezes copy semantics, so changing to AAC encoding would
// require a new PlaybackInfo negotiation and a differently versioned URL.
var errCompatHLSRemuxAudioUnsupported = errors.New("selected audio stream is not supported by the negotiated HLS remux")

// requireLocalAudioDownmixCapability gates only recipes whose bytes use the
// versioned surround-to-stereo boost. Zero is the normalized legacy value for
// stereo, mono, unknown-channel, and audio-copy paths; those must retain their
// historical behavior without requiring the new probe.
func (h *PlaybackHandler) requireLocalAudioDownmixCapability(ctx context.Context, sourceAudioChannels int) error {
	if sourceAudioChannels <= 0 {
		return nil
	}
	registry, err := h.localAudioTransformationRegistry(ctx)
	if err != nil {
		return fmt.Errorf("%w: probe audio_to_aac recipe: %w", errAudioDownmixCapabilityUnavailable, err)
	}
	if !compatSupportsAudioBoost(registry.Advertised()) {
		return errAudioDownmixCapabilityUnavailable
	}
	return nil
}

// localAudioTransformationRegistry mirrors the native v3 registry's
// success-only cache. A complete positive or negative capability result is
// stable for one FFmpeg path; infrastructure and deadline failures remain
// retryable and are never cached.
func (h *PlaybackHandler) localAudioTransformationRegistry(ctx context.Context) (*playback.TransformationRegistryV3, error) {
	ffmpegPath := playback.ResolveFFmpegPath(h.FFmpegPath)
	h.compatAudioRegistryMu.Lock()
	defer h.compatAudioRegistryMu.Unlock()
	if h.compatAudioRegistry != nil && h.compatAudioRegistryPath == ffmpegPath {
		return h.compatAudioRegistry, nil
	}
	probe := playback.ProbeTransformationRegistryWithToneMapV3Result
	if h.compatAudioRegistryProbe != nil {
		probe = h.compatAudioRegistryProbe
	}
	registry, err := probe(context.WithoutCancel(ctx), ffmpegPath, nil)
	if err == nil {
		h.compatAudioRegistry = registry
		h.compatAudioRegistryPath = ffmpegPath
	}
	return registry, err
}

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
	if !validateCompatAudioV2RouteIdentity(w, r, playSession, source, routeID, mediaSourceID) {
		return
	}
	if !validateCompatAudioV2Route(w, r, compatProgressiveRequiresAudioV2(*source, method)) {
		return
	}
	attachCompatStream(r.Context(), session, playSession, source.FileID)

	playSession, err = h.ensureUpstreamPlayback(r.Context(), session, playSession.ID, *source, method)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	// The attach above is a no-op on the first request of a session, which has
	// no upstream id yet. Now it does, and no byte has been written.
	attachCompatStream(r.Context(), session, playSession, source.FileID)

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
	requiresAudioBoost := method == string(playback.PlayRemux) && source.TranscodeAudio && compatSourceAudioChannels(*source) > 0
	if h.NodePlanner != nil && h.JWTSecret != "" {
		plan := h.planCompatProxySession(r.Context(), playSession.UpstreamSessionID, source.Version.Bitrate, requiresAudioBoost)
		if redirectURL, redirectErr := h.buildProxyRedirectURL(playSession.ID, playSession.UpstreamSessionID, method, file, *source, session, playSession.CreatedAt, "", seekSeconds, plan.ProxyNode); redirectErr == nil {
			http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
			return
		}
	}
	if method == string(playback.PlayRemux) && source.TranscodeAudio && h.NodePlanner != nil && !nodepool.LocalTranscodeFallbackAllowed(r.Context(), h.SettingsRepo) {
		h.teardownPlaySession(context.WithoutCancel(r.Context()), playSession, nil, nil)
		writeError(w, http.StatusServiceUnavailable, "NoTranscodeNode",
			"No proxy node is available and local transcode fallback is disabled")
		return
	}
	if requiresAudioBoost {
		if capabilityErr := h.requireLocalAudioDownmixCapability(r.Context(), compatSourceAudioChannels(*source)); capabilityErr != nil {
			h.teardownPlaySession(context.WithoutCancel(r.Context()), playSession, nil, nil)
			writeCompatTranscodeError(w, capabilityErr)
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
		sourceAudioChannels := 0
		if source.TranscodeAudio {
			sourceAudioChannels = compatSourceAudioChannels(*source)
		}
		_ = playback.ServeRemuxWithOptions(w, r, file.FilePath, "mp4", seekSeconds, source.TranscodeAudio, audioTrackIndex, file.PrimaryDVProfile(), playback.RemuxServeOptions{
			ContentType:         playback.RemuxContentType(file.IsAudioOnly()),
			AudioOnly:           file.IsAudioOnly(),
			FFmpegPath:          h.FFmpegPath,
			SourceAudioChannels: sourceAudioChannels,
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
	// §4.2b: a download has a user but no stable playback session, so it is a
	// Transfer rather than a logical session.
	attachCompatTransfer(r.Context(), session, version.FileID)

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
	if !validateCompatAudioV2RouteIdentity(w, r, playSession, source, chiURLParam(r, "id"), firstNonEmpty(r.URL.Query().Get("MediaSourceId"), r.URL.Query().Get("mediaSourceId"))) {
		return
	}
	if !validateCompatAudioV2Route(w, r, compatHLSRequiresAudioV2(*source)) {
		return
	}
	if !validateCompatRemuxV1Route(w, r, compatHLSUsesAudioCopyV1(*source)) {
		return
	}
	// Attach BEFORE ensureUpstreamPlayback below: this route can start a
	// transcode before it writes a byte, which is the whole reason §4.2 enrolls
	// manifest routes. A cut has to be able to act here, not after the side
	// effect. See the boundary note in streamtelemetry.go.
	attachCompatStream(r.Context(), session, playSession, source.FileID)

	var err error
	requiredToneMapMode := tonemap.Mode("")
	var exhaustedRemoteToneMapErr error
	remoteValidationOnly := true
	adoptedLocal := false
	if h.NodePlanner != nil && h.JWTSecret != "" {
		playSession, err = h.ensureUpstreamPlayback(r.Context(), session, playSession.ID, *source, "transcode")
		if err != nil {
			writeCompatUpstreamError(w, err)
			return
		}
		// See HandleVideoStream: the pre-side-effect attach cannot know the
		// upstream id on a session's first request, and this is where it exists.
		attachCompatStream(r.Context(), session, playSession, source.FileID)
		failRemoteStart := func() {
			h.releaseCompatSessionReservation(playSession.UpstreamSessionID)
			h.teardownPlaySession(context.WithoutCancel(r.Context()), playSession, nil, nil)
		}
		upstreamSession, upstreamErr := h.sessionMgr.GetSession(playSession.UpstreamSessionID)
		if upstreamErr == nil {
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
			plan, planErr := h.planCompatTranscodeSession(r.Context(), upstreamSession, file, source.Version.Bitrate, !compatHLSCopiesVideo(*source), compatHLSRecipeSourceAudioChannels(*source))
			if planErr != nil {
				failRemoteStart()
				if errors.Is(planErr, errToneMapCapabilityUnavailable) {
					writeError(w, http.StatusServiceUnavailable, "TranscodeUnavailable", planErr.Error())
					return
				}
				writeError(w, http.StatusUnsupportedMediaType, "TranscodeUnsupported", planErr.Error())
				return
			}
			failedNodeURLs := make(map[string]struct{})
			for plan.TranscodeNode != nil {
				tcNode := plan.TranscodeNode
				initialSeekSeconds, _ := compatInitialTranscodePosition(*source, h.compatSegmentDuration(), playSession.InitialSeekSeconds)
				redirectNodeURL := tcNode.URL
				if err := h.startRemoteTranscodeWithToneMapMode(r.Context(), playSession.ID, playSession.UpstreamSessionID, *source, file, initialSeekSeconds, tcNode.URL, requiredToneMapMode); err != nil {
					if errors.Is(err, errRemoteStartAdoptedLocal) {
						adoptedLocal = true
						break
					}
					var adoptedRemote *remoteStartAdoptedRemoteError
					if errors.As(err, &adoptedRemote) {
						if health, ok := h.NodePlanner.(compatTranscodeNodeHealth); ok && !health.TranscodeNodeHealthy(adoptedRemote.nodeURL) {
							// The winner published this recipe while its node was
							// healthy, but the node is no longer serving; don't
							// redirect the client into it. Tear down and fail
							// retryable so the client restarts the session on a
							// healthy node and the orphaned winner is reaped.
							failRemoteStart()
							writeError(w, http.StatusBadGateway, "TranscodeStartFailed", "Transcode node rejected the request")
							return
						}
						redirectNodeURL = adoptedRemote.nodeURL
					} else {
						if errors.Is(err, errRemoteSoftwareToneMapStartFailed) {
							exhaustedRemoteToneMapErr = errors.Join(exhaustedRemoteToneMapErr, err)
							if !isCompatToneMapExecutionError(err) {
								remoteValidationOnly = false
							}
							h.releaseCompatSessionReservation(playSession.UpstreamSessionID)
							failedNodeURLs[strings.TrimRight(tcNode.URL, "/")] = struct{}{}
							requiredToneMapMode = tonemap.ModeSoftware
							plan, planErr = h.planCompatSoftwareToneMapSession(r.Context(), upstreamSession, file, source.Version.Bitrate, compatSourceAudioChannels(*source), failedNodeURLs)
							if planErr == nil {
								continue
							}
							err = planErr
						}
						failRemoteStart()
						if errors.Is(err, errTranscode4KDisallowed) {
							writeError(w, http.StatusForbidden, "Forbidden", "4K video transcoding is disabled on this server")
							return
						}
						if errors.Is(err, errHDRTranscodeUnsupported) {
							writeError(w, http.StatusUnsupportedMediaType, "TranscodeUnsupported", err.Error())
							return
						}
						writeError(w, http.StatusBadGateway, "TranscodeStartFailed", "Transcode node rejected the request")
						return
					}
				}
				// redirectNodeURL, not tcNode.URL: an adopted remote start moved this
				// session to another healthy node, and the redirect must follow it.
				redirectURL, redirectErr := h.buildProxyRedirectURL(playSession.ID, playSession.UpstreamSessionID, string(playback.PlayTranscode), file, *source, session, playSession.CreatedAt, redirectNodeURL, 0, plan.ProxyNode)
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
	if h.NodePlanner != nil && !adoptedLocal && !nodepool.LocalTranscodeFallbackAllowed(r.Context(), h.SettingsRepo) {
		if playSession.UpstreamSessionID != "" {
			h.releaseCompatSessionReservation(playSession.UpstreamSessionID)
			h.teardownPlaySession(context.WithoutCancel(r.Context()), playSession, nil, nil)
		}
		if exhaustedRemoteToneMapErr != nil {
			if remoteValidationOnly {
				writeCompatTranscodeError(w, exhaustedRemoteToneMapErr)
				return
			}
			writeError(w, http.StatusBadGateway, "TranscodeStartFailed", "Transcode node rejected the request")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "NoTranscodeNode",
			"No transcode node is available and local transcode fallback is disabled")
		return
	}
	if requiredToneMapMode != "" && !adoptedLocal && playSession.UpstreamSessionID != "" {
		if err := h.sessionMgr.SetTranscodeNodeURL(playSession.UpstreamSessionID, ""); err != nil {
			h.teardownPlaySession(context.WithoutCancel(r.Context()), playSession, nil, nil)
			writeError(w, http.StatusInternalServerError, "ServerError", "Failed to bind local transcode")
			return
		}
	}

	// Ensure the transcode process is running.
	manifest, err := h.ensureTranscodeManifestWithToneMapMode(r.Context(), session, playSession.ID, *source, requiredToneMapMode)
	if err == nil {
		// Local-fallback path: the upstream session was minted in here, so this
		// is the first point at which the observation can carry the merged view's
		// canonical key. No-op when the earlier attach already succeeded.
		playSession = h.refreshPlaySession(playSession)
		attachCompatStream(r.Context(), session, playSession, source.FileID)
	}
	if err != nil {
		if exhaustedRemoteToneMapErr != nil && remoteValidationOnly &&
			(errors.Is(exhaustedRemoteToneMapErr, playback.ErrToneMapSourceValidationUnavailable) ||
				errors.Is(err, tonemap.ErrSourceRevisionChanged) || errors.Is(err, playback.ErrToneMapSourceValidationUnavailable)) {
			err = errors.Join(exhaustedRemoteToneMapErr, err)
		}
		writeCompatTranscodeError(w, err)
		return
	}

	segDuration := h.compatSegmentDuration()

	if manifest == nil {
		manifest = generateFullManifest(source.Version.Duration, segDuration, compatHLSCopiesVideo(*source), playSession.InitialSeekSeconds)
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rewriteManifest(manifest, playSession.RouteItemID, playSession.ID, source.ID, compatHLSRoutePathSegment(*source)))
}

func writeCompatTranscodeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errToneMapCapabilityUnavailable),
		errors.Is(err, errAudioDownmixCapabilityUnavailable),
		errors.Is(err, errCompatRecipeSourceMismatch),
		errors.Is(err, playback.ErrToneMapSourceValidationUnavailable),
		errors.Is(err, playback.ErrToneMapExecutorUnavailable):
		slog.Warn("compat transcode unavailable", "component", "jellycompat", "error", err)
		writeError(w, http.StatusServiceUnavailable, "TranscodeUnavailable", "Transcode is temporarily unavailable")
	case errors.Is(err, tonemap.ErrSourceRevisionChanged):
		slog.Warn("compat transcode source changed", "component", "jellycompat", "error", err)
		writeError(w, http.StatusUnsupportedMediaType, "TranscodeUnsupported", "The media source changed; refresh playback information")
	case errors.Is(err, tonemap.ErrSourcePreflightRejected):
		writeError(w, http.StatusUnsupportedMediaType, "TranscodeUnsupported", "The media source is unsupported by the selected tone-map executor")
	case errors.Is(err, errTranscode4KDisallowed):
		writeError(w, http.StatusForbidden, "Forbidden", "4K video transcoding is disabled on this server")
	case errors.Is(err, errHDRTranscodeUnsupported):
		writeError(w, http.StatusUnsupportedMediaType, "TranscodeUnsupported", err.Error())
	case errors.Is(err, playback.ErrManifestNotReady):
		writeError(w, http.StatusServiceUnavailable, "NotReady", "Transcode playlist not ready")
	case errors.Is(err, playback.ErrTranscodeFailed):
		writeError(w, http.StatusInternalServerError, "TranscodeFailed", "Transcode session failed")
	default:
		writeCompatUpstreamError(w, err)
	}
}

func isCompatToneMapExecutionError(err error) bool {
	return errors.Is(err, tonemap.ErrSourceRevisionChanged) ||
		errors.Is(err, tonemap.ErrSourcePreflightRejected) ||
		errors.Is(err, playback.ErrToneMapSourceValidationUnavailable) ||
		errors.Is(err, playback.ErrToneMapExecutorUnavailable)
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
	if !validateCompatAudioV2RouteIdentity(w, r, playSession, source, chiURLParam(r, "id"), firstNonEmpty(r.URL.Query().Get("MediaSourceId"), r.URL.Query().Get("mediaSourceId"))) {
		return
	}
	if !validateCompatAudioV2Route(w, r, compatHLSRequiresAudioV2(*source)) {
		return
	}
	if !validateCompatRemuxV1Route(w, r, compatHLSUsesAudioCopyV1(*source)) {
		return
	}
	// Before ensureTranscodeManifest, for the same reason as the master manifest.
	attachCompatStream(r.Context(), session, playSession, source.FileID)

	// Ensure the transcode process is running.
	manifest, err := h.ensureTranscodeManifest(r.Context(), session, playSession.ID, *source)
	if err == nil {
		// Local-fallback path: the upstream session was minted in here, so this
		// is the first point at which the observation can carry the merged view's
		// canonical key. No-op when the earlier attach already succeeded.
		playSession = h.refreshPlaySession(playSession)
		attachCompatStream(r.Context(), session, playSession, source.FileID)
	}
	if err != nil {
		writeCompatTranscodeError(w, err)
		return
	}

	segDuration := h.compatSegmentDuration()

	if manifest == nil {
		manifest = generateFullManifest(source.Version.Duration, segDuration, compatHLSCopiesVideo(*source), playSession.InitialSeekSeconds)
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rewriteManifest(manifest, playSession.RouteItemID, playSession.ID, source.ID, compatHLSRoutePathSegment(*source)))
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
	source := firstMediaSource(playSession)
	if mediaSourceID := firstNonEmpty(r.URL.Query().Get("MediaSourceId"), r.URL.Query().Get("mediaSourceId")); mediaSourceID != "" {
		source = findMediaSource(playSession, mediaSourceID)
	}
	if source == nil {
		writeError(w, http.StatusBadRequest, "BadRequest", "Media source is required")
		return
	}
	if !validateCompatAudioV2RouteIdentity(w, r, playSession, source, chiURLParam(r, "id"), firstNonEmpty(r.URL.Query().Get("MediaSourceId"), r.URL.Query().Get("mediaSourceId"))) {
		return
	}
	if !validateCompatAudioV2Route(w, r, compatHLSRequiresAudioV2(*source)) {
		return
	}
	if !validateCompatRemuxV1Route(w, r, compatHLSUsesAudioCopyV1(*source)) {
		return
	}
	segmentSourceFileID := source.FileID
	attachCompatStream(r.Context(), session, playSession, segmentSourceFileID)
	playSession, err := h.prepareCompatSegmentRecipe(r.Context(), playSession, *source)
	if err != nil {
		writeCompatTranscodeError(w, err)
		return
	}

	name := chiURLParam(r, "segmentId")
	ext := chiURLParam(r, "segmentContainer")

	requestedSegment := -1
	if segNum, parseErr := playback.ParseSegmentNumber(name); parseErr == nil {
		requestedSegment = segNum
	}
	// Recover the playback session and local runtime as one transaction. If a
	// frozen tone-map recipe cannot be rebuilt, the manager rolls back the exact
	// provisional playback session before this handler returns an error.
	_, transcodeSession, status, reconstructErr := h.tm.LoadOrReconstructTranscodeWithError(
		r.Context(), h.sessionMgr.GetSession, playSession.UpstreamSessionID,
		session.StreamAppUserID, requestedSegment, playSession.Recipe,
	)
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
	case playback.SessionUnavailable:
		writeCompatTranscodeError(w, reconstructErr)
		return
	case playback.SessionUnauthorized:
		// Defensive against invariant drift, not a reachable path: this caller
		// resolves a non-zero user before loading. Falling through would
		// dereference the nil session the status carries.
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Authentication required")
		return
	}

	if transcodeSession == nil {
		writeError(w, http.StatusNotFound, "NotFound", "Transcode session not found")
		return
	}
	// The manager can adopt a runtime registered after the precheck above.
	// Revalidate the byte-affecting audio tuple before serving a segment.
	if !compatLiveTranscodeMatchesAudioSource(transcodeSession, *source) {
		writeCompatTranscodeError(w, errCompatRecipeSourceMismatch)
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
						r.Context(),
						playSession.UpstreamSessionID,
						transcodeSession,
						seekSeconds,
						segNum,
					); restartErr == nil {
						segmentPath, err = transcodeSession.WaitForSegment(segmentFile, 30*time.Second)
					} else {
						err = restartErr
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

func (h *PlaybackHandler) prepareCompatSegmentRecipe(
	ctx context.Context,
	playSession *PlaybackSession,
	source PlaybackMediaSource,
) (*PlaybackSession, error) {
	if playSession == nil || h.tm == nil {
		return playSession, nil
	}
	if live := h.tm.GetTranscodeSession(playSession.UpstreamSessionID); live != nil {
		if compatLiveTranscodeMatchesAudioSource(live, source) {
			return playSession, nil
		}
		return nil, errCompatRecipeSourceMismatch
	}
	if !compatRecipeMatchesSource(playSession.Recipe, source) {
		// A local API pod can repair a stale card from the current frozen source.
		// Never replace a remote executor here: its owning node must be restarted
		// through the normal audio-selection/manifest flow and attest v2 itself.
		if h.sessionMgr == nil {
			return nil, errCompatRecipeSourceMismatch
		}
		upstream, err := h.sessionMgr.GetSession(playSession.UpstreamSessionID)
		if err != nil || upstream == nil || upstream.TranscodeNodeURL != "" {
			return nil, errCompatRecipeSourceMismatch
		}
		if _, err = h.ensureTranscodeSession(ctx, playSession.ID, playSession.UpstreamSessionID, source); err != nil {
			return nil, err
		}
		playSession = h.refreshPlaySession(playSession)
		if playSession == nil || !compatRecipeMatchesSource(playSession.Recipe, source) {
			return nil, errCompatRecipeSourceMismatch
		}
	}
	if playSession.Recipe.TranscodeNodeURL == "" {
		if err := h.requireLocalAudioDownmixCapability(ctx, compatHLSRecipeSourceAudioChannels(source)); err != nil {
			return nil, err
		}
	}
	return playSession, nil
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
	case errors.Is(err, tonemap.ErrSourceRevisionChanged):
		return http.StatusUnsupportedMediaType, "TranscodeUnsupported", "The media source changed; refresh playback information"
	case errors.Is(err, playback.ErrToneMapSourceValidationUnavailable):
		return http.StatusServiceUnavailable, "TranscodeUnavailable", "Transcode is temporarily unavailable"
	case errors.Is(err, playback.ErrToneMapExecutorUnavailable):
		return http.StatusServiceUnavailable, "TranscodeUnavailable", "Transcode is temporarily unavailable"
	case errors.Is(err, tonemap.ErrSourcePreflightRejected):
		return http.StatusUnsupportedMediaType, "TranscodeUnsupported", "The media source is unsupported by the selected tone-map executor"
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

	playSession, source, err := h.resolvePlaybackRoute(r, session, chiURLParam(r, "routeMediaSourceId"), chiURLParam(r, "routeMediaSourceId"))
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
	// Identity is fully known here. The later 400/404 branches for a bad index or
	// a missing subtitle then record an outcome on a real session, which is
	// correct: they are failures by an already-authorized principal.
	attachCompatStream(r.Context(), session, playSession, source.FileID)

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
	compatCopyCodec        = "copy"
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
		updatedPlaySession, updatedSource, restarted, selectionErr := h.applyCompatAudioSelection(
			r.Context(), playSession, req.MediaSourceID, selectedAudioStreamIndex, positionSeconds,
		)
		if updatedPlaySession != nil {
			playSession = updatedPlaySession
		}
		if selectionErr != nil {
			slog.WarnContext(r.Context(), "jellycompat audio selection update failed", "component", "jellycompat",
				"play_session_id", playSession.ID,
				"audio_stream_index", selectedAudioStreamIndex,
				"error", selectionErr,
			)
		} else if updatedSource != nil {
			if resolvedAudioTrackIndex, ok := compatAudioTrackIndex(*updatedSource); ok {
				audioTrackIndex = resolvedAudioTrackIndex
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
	var card playback.RecipeCard
	if ps != nil && ps.Recipe != nil {
		card = *ps.Recipe
	} else if method == "remux" {
		card = playback.NewRemuxRecipeCard(ps.UpstreamSessionID, cs.StreamAppUserID, cs.ProfileID, source.FileID, source.TranscodeAudio, compatAudioTrackIndexOrDefault(source))
		if source.TranscodeAudio {
			card.SourceAudioChannels = compatSourceAudioChannels(source)
		}
	} else {
		card = playback.NewDirectRecipeCard(ps.UpstreamSessionID, cs.StreamAppUserID, cs.ProfileID, source.FileID)
	}
	if ps != nil && !ps.CreatedAt.IsZero() {
		card.OriginalStartedAt = ps.CreatedAt
	}
	return card
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

// refreshPlaySession re-reads a play session from the store so a caller that
// just triggered upstream-session creation sees the minted UpstreamSessionID.
// Returns the original on a miss so callers never have to nil-check.
func (h *PlaybackHandler) refreshPlaySession(current *PlaybackSession) *PlaybackSession {
	if current == nil {
		return nil
	}
	if refreshed, ok := h.playbackStore.Get(current.ID); ok && refreshed != nil {
		return refreshed
	}
	return current
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
	return h.ensureTranscodeManifestWithToneMapMode(ctx, compatSession, playSessionID, source, "")
}

func (h *PlaybackHandler) ensureTranscodeManifestWithToneMapMode(
	ctx context.Context,
	compatSession *Session,
	playSessionID string,
	source PlaybackMediaSource,
	requiredToneMapMode tonemap.Mode,
) ([]byte, error) {
	playSession, err := h.ensureUpstreamPlayback(ctx, compatSession, playSessionID, source, "transcode")
	if err != nil {
		return nil, err
	}

	transcodeSession, err := h.ensureTranscodeSessionWithToneMapMode(ctx, playSessionID, playSession.UpstreamSessionID, source, requiredToneMapMode)
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

var compatManifestStartupTimeout = playback.ManifestStartupTimeout

func compatTranscodeSessionUsesToneMapMode(session *playback.TranscodeSession, required tonemap.Mode) bool {
	return required == "" || (session != nil && session.Opts().ToneMapMode == required)
}

// ensureTranscodeSession returns, reconstructs, or starts the requested transcode.
func (h *PlaybackHandler) ensureTranscodeSession(ctx context.Context, playSessionID, upstreamSessionID string, source PlaybackMediaSource) (*playback.TranscodeSession, error) {
	return h.ensureTranscodeSessionWithToneMapMode(ctx, playSessionID, upstreamSessionID, source, "")
}

func (h *PlaybackHandler) ensureTranscodeSessionWithToneMapMode(
	ctx context.Context,
	playSessionID, upstreamSessionID string,
	source PlaybackMediaSource,
	requiredToneMapMode tonemap.Mode,
) (*playback.TranscodeSession, error) {
	sourceAudioChannels := compatHLSRecipeSourceAudioChannels(source)
	audioTrackIndex := compatAudioTrackIndexOrDefault(source)
	if existing := h.tm.GetTranscodeSession(upstreamSessionID); existing != nil && compatTranscodeSessionUsesToneMapMode(existing, requiredToneMapMode) {
		if compatLiveTranscodeMatchesAudioSource(existing, source) {
			return existing, nil
		}
		return nil, errCompatRecipeSourceMismatch
	}
	if err := h.requireLocalAudioDownmixCapability(ctx, sourceAudioChannels); err != nil {
		return nil, err
	}
	// If a recipe survived in the compat store (e.g. a server restart), rebuild
	// the transcode from it — at the recipe's position — rather than starting
	// fresh at the original seek. On a first play there is no recipe yet, so this
	// is a no-op and we fall through to the normal start below.
	if h.playbackStore != nil {
		if ps, ok := h.playbackStore.Get(playSessionID); ok && compatRecipeMatchesSource(ps.Recipe, source) {
			// A forced software failover must not reconstruct the stale hardware
			// recipe that preceded it. If a concurrent caller already registered a
			// runtime, also verify the returned process rather than trusting the card.
			if requiredToneMapMode != "" && ps.Recipe.ToneMapMode != requiredToneMapMode {
				// Fall through to build a newly validated recipe in the required mode.
			} else if reconstructed, reconstructErr := h.tm.ReconstructTranscodeWithError(ctx, upstreamSessionID, -1, *ps.Recipe); reconstructErr != nil && ps.Recipe.ToneMapMode != "" {
				// A frozen tone-map recipe must not be bypassed by a fresh plan after
				// execution-time source validation fails.
				return nil, reconstructErr
			} else if reconstructed != nil && compatTranscodeSessionUsesToneMapMode(reconstructed, requiredToneMapMode) {
				// ReconstructTranscodeWithError may return a runtime that raced into
				// the manager after the fast-path check. Never adopt it solely by ID.
				if !compatLiveTranscodeMatchesAudioSource(reconstructed, source) {
					return nil, errCompatRecipeSourceMismatch
				}
				h.recordTranscodeStreamDetails(ctx, upstreamSessionID, reconstructed.Opts())
				return reconstructed, nil
			}
		}
	}
	if !compatHLSCopiesVideo(source) && is4KResolution(source.Version.Resolution) && !h.allow4KVideoTranscode(ctx) {
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
		AudioTrackIndex:     audioTrackIndex,
		SourceAudioChannels: sourceAudioChannels,
		TotalDuration:       float64(source.Version.Duration),
		FastStart:           true,
	}
	if sourceAudioChannels > 0 {
		opts.TargetAudioChannels = 2
	}
	if compatHLSCopiesVideo(source) {
		opts.TargetCodecVideo = compatCopyCodec
		opts.VideoSampleEntry = playback.VideoSampleEntryForDVCopy(file.PrimaryDVProfile())
	}
	if !compatHLSTranscodesAudio(source) {
		opts.TargetCodecAudio = compatCopyCodec
	}
	var toneMapCapabilities tonemap.Capabilities
	autoVideoToolboxBitrate := 0
	if !compatHLSCopiesVideo(source) {
		metadata := tonemap.MetadataForFile(file)
		if metadata.DynamicRange != "" && metadata.DynamicRange != playback.DynamicRangeSDRV3 {
			var capabilityErr error
			toneMapCapabilities, capabilityErr = h.localToneMapCapabilities(ctx)
			if capabilityErr != nil {
				return nil, fmt.Errorf("%w: %w", errToneMapCapabilityUnavailable, capabilityErr)
			}
		}
		toneMapRecipe, toneMapErr := h.resolveCompatToneMapRecipe(ctx, file, toneMapCapabilities)
		if toneMapErr != nil {
			return nil, toneMapErr
		}
		if toneMapErr = requireCompatToneMapMode(&toneMapRecipe, toneMapCapabilities, requiredToneMapMode); toneMapErr != nil {
			return nil, toneMapErr
		}
		toneMapRecipe.apply(&opts)
		autoVideoToolboxBitrate = compatVideoToolboxToneMapBitrateKbps(source.Version, toneMapRecipe)
		if autoVideoToolboxBitrate > 0 {
			opts.TargetBitrateKbps = autoVideoToolboxBitrate
		}
	}
	opts.SegmentDuration = h.compatSegmentDuration()

	// Hold the per-session lifecycle lock across "check existing → spawn →
	// register" so a concurrent reconstruct cannot run a second ffmpeg writer
	// against this session's output dir. Readiness waits happen after registration
	// and outside this lock so concurrent manifest requests can use the same session.
	unlock := h.tm.LockSessionLifecycle(upstreamSessionID)
	if existing := h.tm.GetTranscodeSession(upstreamSessionID); existing != nil {
		if compatTranscodeSessionUsesToneMapMode(existing, requiredToneMapMode) &&
			compatLiveTranscodeMatchesAudioSource(existing, source) {
			unlock()
			return existing, nil
		}
		if compatTranscodeSessionUsesToneMapMode(existing, requiredToneMapMode) {
			unlock()
			return nil, errCompatRecipeSourceMismatch
		}
		// The failed remote sequence has narrowed this session to software.
		// Remove a concurrently started or stale hardware writer before spawning
		// the replacement in the same output directory.
		h.tm.CloseTranscodeSessionIf(upstreamSessionID, existing, "")
	}
	manifestDeadline := time.Now().Add(compatManifestStartupTimeout)
	transcodeSession, err := playback.StartTranscode(ctx, opts)
	if err != nil && downgradeCompatLocalToneMap(&opts, toneMapCapabilities, autoVideoToolboxBitrate) {
		transcodeSession, err = playback.StartTranscode(ctx, opts)
		if err == nil {
			manifestDeadline = time.Now().Add(compatManifestStartupTimeout)
		}
	}
	if err != nil {
		unlock()
		return nil, err
	}
	h.tm.RegisterTranscodeSession(upstreamSessionID, transcodeSession)
	unlock()

	if opts.ToneMapMode != "" {
		if _, readyErr := transcodeSession.WaitForManifest(time.Until(manifestDeadline)); readyErr != nil {
			fallbackEligible := downgradeCompatLocalToneMap(&opts, toneMapCapabilities, autoVideoToolboxBitrate)
			replaceUnlock := h.tm.LockSessionLifecycle(upstreamSessionID)
			if live := h.tm.GetTranscodeSession(upstreamSessionID); live != transcodeSession {
				replaceUnlock()
				if live != nil && compatTranscodeSessionUsesToneMapMode(live, requiredToneMapMode) {
					if compatLiveTranscodeMatchesAudioSource(live, source) {
						return live, nil
					}
					return nil, errCompatRecipeSourceMismatch
				}
				if live != nil {
					return nil, errHDRTranscodeUnsupported
				}
				return nil, readyErr
			}
			h.tm.CloseTranscodeSessionIf(upstreamSessionID, transcodeSession, "")
			if !fallbackEligible {
				replaceUnlock()
				return nil, readyErr
			}
			transcodeSession, err = playback.StartTranscode(ctx, opts)
			if err != nil {
				replaceUnlock()
				return nil, err
			}
			h.tm.RegisterTranscodeSession(upstreamSessionID, transcodeSession)
			replaceUnlock()
			fallbackManifestDeadline := time.Now().Add(compatManifestStartupTimeout)
			if _, fallbackErr := transcodeSession.WaitForManifest(time.Until(fallbackManifestDeadline)); fallbackErr != nil {
				cleanupUnlock := h.tm.LockSessionLifecycle(upstreamSessionID)
				h.tm.CloseTranscodeSessionIf(upstreamSessionID, transcodeSession, "")
				cleanupUnlock()
				return nil, fallbackErr
			}
		}
	}
	if h.compatLocalTranscodeReady != nil {
		h.compatLocalTranscodeReady(transcodeSession)
	}

	// Readiness is not ownership: another caller may replace this runtime after
	// it becomes ready but before its execution facts and durable recipe are
	// published. Fence that publication under the same lifecycle lock used for
	// replacement, and yield to an exact live successor rather than publishing
	// stale facts or rolling the successor back.
	publishUnlock := h.tm.LockSessionLifecycle(upstreamSessionID)
	if live := h.tm.GetTranscodeSession(upstreamSessionID); live != transcodeSession {
		publishUnlock()
		if live != nil && compatTranscodeSessionUsesToneMapMode(live, requiredToneMapMode) {
			if compatLiveTranscodeMatchesAudioSource(live, source) {
				return live, nil
			}
			return nil, errCompatRecipeSourceMismatch
		}
		if live != nil {
			return nil, errHDRTranscodeUnsupported
		}
		return nil, playback.ErrSessionSuperseded
	}
	effectiveOpts := transcodeSession.Opts()

	// Mirror the actual encode decisions onto the upstream session before the
	// recipe is persisted — video-copy HLS must not sync as a video transcode.
	h.recordTranscodeStreamDetails(ctx, upstreamSessionID, effectiveOpts)

	// Register the exit monitor and persist the reconstruction recipe (shared with
	// the remote path). On a failed compat-store write roll back this abandoned
	// transcode rather than leaking it.
	h.tm.MonitorLocalTranscodeExit(upstreamSessionID, transcodeSession)

	if err := h.persistTranscodeRecipe(ctx, playSessionID, upstreamSessionID, effectiveOpts); err != nil {
		h.tm.CloseTranscodeSessionIf(upstreamSessionID, transcodeSession, "")
		publishUnlock()
		return nil, err
	}
	publishUnlock()

	return transcodeSession, nil
}

// downgradeCompatLocalToneMap removes the bitrate synthesized solely for a
// VideoToolbox hardware attempt when the local session falls back to software.
// Explicit client constraints are represented by a zero automatic bitrate and
// remain intact.
func downgradeCompatLocalToneMap(opts *playback.TranscodeOpts, capabilities tonemap.Capabilities, autoVideoToolboxBitrate int) bool {
	if opts == nil || !downgradeToSoftwareToneMap(
		opts.ToneMapPolicy, &opts.ToneMapMode, &opts.ToneMapFilter, &opts.HWAccel,
		opts.ToneMapSourceKind, capabilities,
	) {
		return false
	}
	if autoVideoToolboxBitrate > 0 && opts.TargetBitrateKbps == autoVideoToolboxBitrate {
		opts.TargetBitrateKbps = 0
	}
	return true
}

func compatLiveTranscodeMatchesAudioSource(transcodeSession *playback.TranscodeSession, source PlaybackMediaSource) bool {
	if transcodeSession == nil {
		return false
	}
	opts := transcodeSession.Opts()
	return opts.AudioTrackIndex == compatAudioTrackIndexOrDefault(source) &&
		opts.SourceAudioChannels == compatHLSRecipeSourceAudioChannels(source)
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

func compatPlaybackSource(session *PlaybackSession, mediaSourceID string) (*PlaybackMediaSource, error) {
	if session == nil || len(session.MediaSources) == 0 {
		return nil, ErrSessionNotFound
	}
	sourceIndex := 0
	if mediaSourceID != "" {
		sourceIndex = -1
		for index := range session.MediaSources {
			if mediaSourceIDsEqual(session.MediaSources[index].ID, mediaSourceID) {
				sourceIndex = index
				break
			}
		}
	}
	if sourceIndex < 0 || sourceIndex >= len(session.MediaSources) {
		return nil, ErrSessionNotFound
	}
	source := session.MediaSources[sourceIndex]
	return &source, nil
}

// selectedCompatAudioSource validates a requested stream against a detached
// source copy. Report handlers use it to prove an integrated v2 recipe is
// executable before mutating the durable play session or its upstream mirror.
func selectedCompatAudioSource(session *PlaybackSession, mediaSourceID string, audioStreamIndex int) (*PlaybackMediaSource, error) {
	source, err := compatPlaybackSource(session, mediaSourceID)
	if err != nil {
		return nil, err
	}
	if !isValidCompatAudioStreamIndex(source.Version, audioStreamIndex) {
		return nil, fmt.Errorf("invalid compat audio stream index")
	}
	if !compatHLSRemuxSupportsAudioStream(*source, audioStreamIndex) {
		return nil, errCompatHLSRemuxAudioUnsupported
	}
	source.SelectedAudioStreamIndex = intPtr(audioStreamIndex)
	return source, nil
}

func compatHLSRemuxSupportsAudioStream(source PlaybackMediaSource, audioStreamIndex int) bool {
	if !compatHLSUsesAudioCopyV1(source) {
		return true
	}
	for _, supportedStreamIndex := range source.HLSRemuxAudioStreamIndexes {
		if supportedStreamIndex == audioStreamIndex {
			return true
		}
	}
	return false
}

func (h *PlaybackHandler) preflightCompatAudioSelection(ctx context.Context, playSession *PlaybackSession, source PlaybackMediaSource) error {
	if playSession == nil || playSession.UpstreamSessionID == "" || playSession.UpstreamPlayMethod != "transcode" || h.tm == nil {
		return nil
	}
	if h.tm.GetTranscodeSession(playSession.UpstreamSessionID) == nil {
		// A remote node executes this restart and independently attests the exact
		// recipe. The API-local capability only governs an integrated process.
		return nil
	}
	return h.requireLocalAudioDownmixCapability(ctx, compatHLSRecipeSourceAudioChannels(source))
}

func (h *PlaybackHandler) applyCompatAudioSelection(
	ctx context.Context,
	playSession *PlaybackSession,
	mediaSourceID string,
	audioStreamIndex int,
	positionSeconds float64,
) (*PlaybackSession, *PlaybackMediaSource, bool, error) {
	if playSession == nil {
		return nil, nil, false, ErrSessionNotFound
	}
	if h.tm != nil {
		unlock := h.tm.LockSessionLifecycle("compat-audio-selection\x00" + playSession.ID)
		defer unlock()
		if current, ok := h.playbackStore.Get(playSession.ID); ok {
			playSession = current
		}
	}
	if !audioSelectionChanged(playSession, mediaSourceID, audioStreamIndex) {
		currentSource, err := compatPlaybackSource(playSession, mediaSourceID)
		return playSession, currentSource, false, err
	}
	originalSource, err := compatPlaybackSource(playSession, mediaSourceID)
	if err != nil {
		return playSession, nil, false, err
	}
	candidateSource, err := selectedCompatAudioSource(playSession, mediaSourceID, audioStreamIndex)
	if err != nil {
		return playSession, nil, false, err
	}
	if err = h.preflightCompatAudioSelection(ctx, playSession, *candidateSource); err != nil {
		return playSession, nil, false, err
	}

	// Key mutations by the resolved session id: after an alias or route
	// fallback, the client's PlaySessionId is not necessarily a store key.
	updatedPlaySession, updatedSource, err := h.setSelectedAudioStream(playSession.ID, mediaSourceID, audioStreamIndex)
	if err != nil {
		restored, rollbackErr := h.rollbackCompatAudioSelection(playSession.ID, mediaSourceID, *originalSource, *candidateSource)
		return restored, nil, false, errors.Join(err, rollbackErr)
	}
	if err = h.syncUpstreamAudioSelection(updatedPlaySession, *updatedSource); err != nil {
		restored, rollbackErr := h.rollbackCompatAudioSelection(updatedPlaySession.ID, mediaSourceID, *originalSource, *candidateSource)
		return restored, nil, false, errors.Join(err, rollbackErr)
	}
	restarted, err := h.restartCompatTranscodeForAudioSelection(ctx, updatedPlaySession, *updatedSource, positionSeconds)
	if err != nil {
		restored, rollbackErr := h.rollbackCompatAudioSelection(updatedPlaySession.ID, mediaSourceID, *originalSource, *candidateSource)
		return restored, nil, false, errors.Join(err, rollbackErr)
	}
	return updatedPlaySession, updatedSource, restarted, nil
}

func (h *PlaybackHandler) rollbackCompatAudioSelection(
	playSessionID, mediaSourceID string,
	originalSource, attemptedSource PlaybackMediaSource,
) (*PlaybackSession, error) {
	if h.playbackStore == nil {
		return nil, ErrSessionNotFound
	}
	updateErr := h.playbackStore.Update(playSessionID, func(current *PlaybackSession) error {
		for index := range current.MediaSources {
			if mediaSourceID == "" && index == 0 || mediaSourceIDsEqual(current.MediaSources[index].ID, originalSource.ID) {
				if !compatOptionalIntEqual(current.MediaSources[index].SelectedAudioStreamIndex, attemptedSource.SelectedAudioStreamIndex) {
					return nil
				}
				if originalSource.SelectedAudioStreamIndex == nil {
					current.MediaSources[index].SelectedAudioStreamIndex = nil
				} else {
					current.MediaSources[index].SelectedAudioStreamIndex = intPtr(*originalSource.SelectedAudioStreamIndex)
				}
				return nil
			}
		}
		return ErrSessionNotFound
	})
	restored, ok := h.playbackStore.Get(playSessionID)
	if !ok {
		return nil, errors.Join(updateErr, ErrSessionNotFound)
	}
	restoredSource, sourceErr := compatPlaybackSource(restored, mediaSourceID)
	if sourceErr != nil {
		return restored, errors.Join(updateErr, sourceErr)
	}
	if !compatOptionalIntEqual(restoredSource.SelectedAudioStreamIndex, originalSource.SelectedAudioStreamIndex) {
		return restored, updateErr
	}
	if syncErr := h.syncUpstreamAudioSelection(restored, originalSource); syncErr != nil {
		return restored, errors.Join(updateErr, fmt.Errorf("restore upstream audio selection: %w", syncErr))
	}
	return restored, updateErr
}

func compatOptionalIntEqual(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
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
		sourceAudioChannels := compatHLSRecipeSourceAudioChannels(source)
		if err := h.requireLocalAudioDownmixCapability(ctx, sourceAudioChannels); err != nil {
			return false, err
		}
		previousOpts := transcodeSession.Opts()
		transcodeSession.SetAudioTrackIndex(audioTrackIndex)
		transcodeSession.SetSourceAudioChannels(sourceAudioChannels)
		startSegment := 0
		if segmentDuration := transcodeSession.Opts().SegmentDuration; segmentDuration > 0 && positionSeconds > 0 {
			startSegment = int(positionSeconds / float64(segmentDuration))
		}
		if err := h.tm.RestartSessionLocked(ctx, playSession.UpstreamSessionID, transcodeSession, positionSeconds, startSegment); err != nil {
			transcodeSession.SetAudioTrackIndex(previousOpts.AudioTrackIndex)
			transcodeSession.SetSourceAudioChannels(previousOpts.SourceAudioChannels)
			if errors.Is(err, playback.ErrSessionSuperseded) {
				return false, err
			}
			rollbackErr := h.tm.RestartSessionLocked(
				context.WithoutCancel(ctx),
				playSession.UpstreamSessionID,
				transcodeSession,
				positionSeconds,
				startSegment,
			)
			if rollbackErr != nil {
				return false, errors.Join(err, fmt.Errorf("restore previous audio recipe: %w", rollbackErr))
			}
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
		if errors.Is(err, errRemoteStartAdoptedLocal) || errors.Is(err, errRemoteStartAdoptedRemote) {
			return true, nil
		}
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

func rewriteManifest(manifest []byte, routeItemID, playlistID, mediaSourceID, routePathSegment string) []byte {
	var out strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(string(manifest)))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "#EXT-X-MAP:URI=\""):
			prefix := "#EXT-X-MAP:URI=\""
			uri := strings.TrimSuffix(strings.TrimPrefix(line, prefix), "\"")
			line = prefix + buildSegmentProxyPath(routeItemID, playlistID, mediaSourceID, uri, routePathSegment) + "\""
		case line != "" && !strings.HasPrefix(line, "#"):
			line = buildSegmentProxyPath(routeItemID, playlistID, mediaSourceID, line, routePathSegment)
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return []byte(out.String())
}

func buildSegmentProxyPath(routeItemID, playlistID, mediaSourceID, current, routePathSegment string) string {
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
	basePath := fmt.Sprintf("/Videos/%s", routeItemID)
	if routePathSegment != "" {
		basePath += "/" + routePathSegment
	}
	if base == "stream.m3u8" {
		return fmt.Sprintf("%s/hls/%s/stream.m3u8%s", basePath, playlistID, qs)
	}
	if strings.Contains(base, ".") {
		ext := path.Ext(base)
		name := strings.TrimSuffix(base, ext)
		return fmt.Sprintf("%s/hls/%s/%s%s%s", basePath, playlistID, name, ext, qs)
	}
	return fmt.Sprintf("%s/hls/%s/%s%s", basePath, playlistID, base, qs)
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
