package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// SessionManagerInterface defines the operations the PlaybackHandler needs
// on the session manager.
type SessionManagerInterface interface {
	StartSession(userID int, profileID string, fileID int, method playback.PlayMethod, transcodeAudio bool) (*playback.Session, error)
	StartSessionWithFiles(userID int, profileID string, effectiveFileID int, requestedFileID int, method playback.PlayMethod, transcodeAudio bool) (*playback.Session, error)
	UpdateProgress(sessionID string, position float64, isPaused bool) error
	UpdateAudioTrack(sessionID string, audioTrackIndex int, method playback.PlayMethod) error
	UpdateStreamState(sessionID string, state playback.SessionStreamState) error
	TouchActivity(sessionID string) error
	BeginTransport(sessionID string) error
	EndTransport(sessionID string) error
	SetEffectiveMediaFileID(sessionID string, fileID int) error
	SetTranscodeNodeURL(sessionID, url string) error
	SetWebSocket(sessionID string, connected bool) error
	SetRealtimeConnection(sessionID string, connected bool) error
	SetProgressPersistenceDisabled(sessionID string, disabled bool) error
	StopSession(sessionID string) error
	GetSession(sessionID string) (*playback.Session, error)
}

type sessionStarterWithFilesContext interface {
	StartSessionWithFilesContext(ctx context.Context, userID int, profileID string, effectiveFileID int, requestedFileID int, method playback.PlayMethod, transcodeAudio bool) (*playback.Session, error)
}

type PlaybackItemAccessChecker interface {
	EnsureAccessible(ctx context.Context, contentID string, filter catalog.AccessFilter) error
}

type PlaybackEpisodeLookup interface {
	GetByID(ctx context.Context, contentID string) (*models.Episode, error)
}

type PlaybackSessionSyncer interface {
	SyncNow(ctx context.Context) error
}

// PlaybackSettingsReader reads server settings for playback decisions.
type PlaybackSettingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// PlaybackFileVersionFetcher retrieves alternate file versions for a content item.
type PlaybackFileVersionFetcher interface {
	GetByContentID(ctx context.Context, contentID string) ([]*models.MediaFile, error)
	GetByEpisodeID(ctx context.Context, episodeID string) ([]*models.MediaFile, error)
}

type PlaybackProbeEnsurer interface {
	Ensure(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error)
}

type PlaybackChapterThumbnailQueuer interface {
	QueuePriorityFileAtPosition(ctx context.Context, fileID int, targetSeconds float64)
}

// PlaybackOriginalLanguageLookup fetches the original language for a content item.
type PlaybackOriginalLanguageLookup interface {
	GetOriginalLanguage(ctx context.Context, contentID string) (string, error)
}

// PlaybackHandler handles playback session HTTP endpoints.
type PlaybackHandler struct {
	sessionMgr              SessionManagerInterface
	fileResolver            FilePathResolver            // optional; enables stream_url in responses
	StoreProvider           userstore.UserStoreProvider // optional; enables progress/history persistence
	WatchScrobbler          PlaybackWatchScrobbler
	StableIdentityResolver  *watchstate.StableIdentityResolver
	CompletionObserver      watchstate.CompletionObserver // optional; auto-removes watched items from the watchlist
	profileStaler           ProfileStaler
	profileRefreshRequester ProfileRefreshRequester
	AdminStore              PlaybackAdminStore    // optional; enables admin playback history/live session cleanup
	SessionSyncer           PlaybackSessionSyncer // optional; enables immediate session sync to shared admin view
	EventsHub               *evt.Hub
	MissingMarker           MissingFileMarker
	NodePlanner             nodepool.SessionPlanner   // optional; enables proxy/transcode node selection
	JWTSecret               string                    // needed for signing stream tokens
	ItemAccess              PlaybackItemAccessChecker // optional; enables file authorization checks
	EpisodeLookup           PlaybackEpisodeLookup     // optional; resolves episode files to their series
	OriginalLangLookup      PlaybackOriginalLanguageLookup
	SettingsRepo            PlaybackSettingsReader     // optional; reads server settings (e.g., allow_4k_transcode)
	FileVersionFetcher      PlaybackFileVersionFetcher // optional; queries sibling file versions for 4K guard
	ProbeEnsurer            PlaybackProbeEnsurer       // optional; repairs missing probe metadata on demand
	ChapterThumbnailQueuer  PlaybackChapterThumbnailQueuer
	IntroAnalyzer           IntroEpisodeAnalyzer
	IntroRepository         PlaybackIntroEligibilityChecker
	MarkerRegistry          *markers.Registry
	MarkerResolver          markers.ExternalIDResolver
	MarkerUpserter          PlaybackMarkerUpserter
	MarkerUpdateNotifier    PlaybackMarkerUpdateNotifier
	MarkerLazyContext       context.Context
	MarkerLazyInFlight      sync.Map
	SubtitleRepo            subtitles.Repository // optional; enables downloaded subtitles in playback
	RealtimeHub             *playback.RealtimeHub
	CommandTracker          *playback.CommandTracker
	CommandDispatcher       *playback.CommandDispatcher
	// PlaybackConfig returns the current playback config (ffmpeg path,
	// hwaccel, transcode dir). Wired to the live config in integrated mode
	// so admin changes apply to newly started transcodes. Read it through
	// playbackConfig(), which falls back to defaults when unset.
	PlaybackConfig    func() config.PlaybackConfig
	FFmpegLogSink     playback.FFmpegLogSink
	realtimeCommandMu sync.Mutex
	realtimeCommands  map[string]playbackCommandRecord
	// tm owns the transcode-session lifecycle (live map, recipe cards, and
	// restart reconstruct) shared with the jellycompat handler. The handler
	// delegates all transcode-session and recipe operations to it.
	tm *playback.TranscodeManager
}

type PlaybackWatchScrobbler interface {
	ScrobbleStart(ctx context.Context, event watchsync.ScrobbleEvent) error
	ScrobblePause(ctx context.Context, event watchsync.ScrobbleEvent) error
	ScrobbleStop(ctx context.Context, event watchsync.ScrobbleEvent) error
}

type sessionExpirationHookSetter interface {
	SetExpirationHook(func(*playback.Session))
}

// NewPlaybackHandler creates a new PlaybackHandler backed by the given
// session manager. Pass optional FilePathResolver to enable stream_url
// and subtitle_urls in start playback responses.
func NewPlaybackHandler(sessionMgr SessionManagerInterface, opts ...FilePathResolver) *PlaybackHandler {
	h := &PlaybackHandler{
		sessionMgr:       sessionMgr,
		realtimeCommands: make(map[string]playbackCommandRecord),
		tm:               playback.NewTranscodeManager(),
	}
	if len(opts) > 0 {
		h.fileResolver = opts[0]
	}
	// Wire the shared transcode manager with closures so it reads the handler's
	// (often late-set) config/store/secret fields lazily at call time, avoiding a
	// field-ordering hazard during router setup.
	h.tm.JWTSecretFn = func() string { return h.JWTSecret }
	h.tm.LogSinkFn = func() playback.FFmpegLogSink { return h.FFmpegLogSink }
	h.tm.Config = func() playback.TranscodeRuntimeConfig {
		c := h.playbackConfig()
		return playback.TranscodeRuntimeConfig{
			TranscodeDir: c.TranscodeDir,
			FFmpegPath:   c.FFmpegPath,
			HWAccel:      c.HWAccel,
			HWDevice:     c.HWDevice,
		}
	}
	h.tm.StartThrottler = func(ctx context.Context, ts *playback.TranscodeSession) {
		h.maybeStartThrottler(ctx, ts)
	}
	h.tm.OnFFmpegCrash = func(ctx context.Context, sessionID string, dead *playback.TranscodeSession) {
		// ffmpeg crash — tear the session down; a client holding a valid stream
		// token can reconstruct it on the next request.
		//
		// Compare-and-delete the dead transcode first: between ffmpeg's error exit
		// and this teardown a reconstruct may have registered a fresh successor
		// under the same id. CloseTranscodeSessionIf only removes (and Close()s, which
		// reaps the shared output dir) the entry when it is still the dead session;
		// if a successor won, it leaves the live one untouched and we must NOT tear
		// down the reconstructed playback session that now backs it.
		var nodeURL string
		if s, err := h.sessionMgr.GetSession(sessionID); err == nil {
			nodeURL = s.TranscodeNodeURL
		}
		if successor := h.tm.GetTranscodeSession(sessionID); successor != nil && successor != dead {
			// A reconstruct already replaced the crashed process; the live successor
			// and its session stand. Cheap fast-path only — the authoritative gate is
			// the compare-and-delete result below.
			return
		}
		// CloseTranscodeSessionIf is the authoritative gate: a successor may register
		// under the same id between the pre-check above and here. We only tear down the
		// upstream playback session when the compare-and-delete actually matched the
		// dead transcode. When it returns false a successor owns the session — do
		// nothing further, or finalizeSessionStop's unconditional CloseTranscodeSession
		// would reap the live successor's output dir mid-serve.
		if !h.tm.CloseTranscodeSessionIf(sessionID, dead, nodeURL) {
			return
		}
		if err := h.stopPlaybackSessionByID(ctx, sessionID, false); err != nil && !errors.Is(err, playback.ErrSessionNotFound) {
			slog.Error("failed to stop playback after local transcode exit", "session", sessionID, "error", err, "playback_session_id", sessionID)
		}
	}
	if reg, ok := sessionMgr.(interface {
		RegisterReconstructed(s *playback.Session) *playback.Session
		RegisterReconstructedWithLimits(ctx context.Context, s *playback.Session) (*playback.Session, error)
	}); ok {
		h.tm.Sessions = reg
	}
	if setter, ok := sessionMgr.(sessionExpirationHookSetter); ok {
		setter.SetExpirationHook(h.handleExpiredSession)
	}
	return h
}

// TranscodeManager returns the shared transcode/reconstruct manager so sibling
// handlers (e.g. StreamHandler) can reuse the same recipe-card store, live
// transcode map, and reconstruct front door rather than wiring a second one.
func (h *PlaybackHandler) TranscodeManager() *playback.TranscodeManager {
	return h.tm
}

// SetProfileStaler configures an optional staleness trigger for taste profiles.
func (h *PlaybackHandler) SetProfileStaler(ps ProfileStaler) {
	h.profileStaler = ps
}

// SetProfileRefreshRequester configures an optional background refresh queue for taste profiles.
func (h *PlaybackHandler) SetProfileRefreshRequester(requester ProfileRefreshRequester) {
	h.profileRefreshRequester = requester
}

// playbackConfig returns the current playback config, falling back to the
// same defaults as config loading (transcode enabled, temp transcode dir)
// when no provider is wired (tests, minimal setups).
func (h *PlaybackHandler) playbackConfig() config.PlaybackConfig {
	if h.PlaybackConfig != nil {
		return h.PlaybackConfig()
	}
	return config.PlaybackConfig{
		TranscodeEnabled: true,
		TranscodeDir:     filepath.Join(os.TempDir(), "silo-transcode"),
	}
}

// CleanupOrphanedTranscodes removes stale per-session temp directories for
// transcodes that are no longer tracked in memory, sparing dirs whose recipe
// card still exists. Delegates to the shared transcode manager.
func (h *PlaybackHandler) CleanupOrphanedTranscodes() (int, error) {
	return h.tm.CleanupOrphanedTranscodes()
}

// playbackThresholds reads the playback.watched_threshold and
// playback.min_resume_threshold settings. Zero values mean "use defaults".
func (h *PlaybackHandler) playbackThresholds(ctx context.Context) userstore.ProgressThresholds {
	if h.SettingsRepo == nil {
		return userstore.ProgressThresholds{}
	}
	var t userstore.ProgressThresholds
	if v, _ := h.SettingsRepo.Get(ctx, "playback.watched_threshold"); v != "" {
		if pct, err := strconv.Atoi(v); err == nil && pct > 0 {
			t.WatchedPct = pct
		}
	}
	if v, _ := h.SettingsRepo.Get(ctx, "playback.min_resume_threshold"); v != "" {
		if pct, err := strconv.Atoi(v); err == nil && pct > 0 {
			t.MinResumePct = pct
		}
	}
	return t
}

// --- Request/Response types ---

// hdrDetails describes granular HDR support advertised by the client.
// Optional — absent means the resolver falls back to the boolean HDR flag.
// Dolby Vision profile numbers follow MediaCodec:
//
//	5 = DvheStn / DvheSt (single-layer)
//	7 = DvheDtb / DvheDtr / DvheDth (dual-layer BL+EL — needs multi-instance)
//	8 = DvheSt4k / DvavSe
type hdrDetails struct {
	HDR10               bool  `json:"hdr10"`
	HDR10Plus           bool  `json:"hdr10_plus"`
	HLG                 bool  `json:"hlg"`
	DolbyVisionProfiles []int `json:"dolby_vision_profiles"`
}

// audioPassthroughCapabilities describes what the connected audio sink can
// decode bit-exact. Distinct from `codecs_audio`, which describes what the
// client can decode itself. Passthrough codecs come from `AudioCapabilities`
// (HDMI EDID / Bluetooth / USB DAC capability probing on Android; equivalent
// on iOS/tvOS).
type audioPassthroughCapabilities struct {
	PassthroughCodecs  []string `json:"passthrough_codecs"`
	SpatializerEnabled bool     `json:"spatializer_enabled"`
	MaxChannels        int      `json:"max_channels"`
}

// startPlaybackRequest represents the JSON body for POST /playback/start.
type startPlaybackRequest struct {
	FileID                       int                           `json:"file_id"`
	ProfileID                    string                        `json:"profile_id"`
	PlayMethod                   string                        `json:"play_method"`
	StartPosition                *float64                      `json:"start_position,omitempty"`
	AudioTrackIndex              *int                          `json:"audio_track_index,omitempty"`
	PreserveDirectAudioSelection bool                          `json:"preserve_direct_audio_selection,omitempty"`
	DisableProgressPersistence   bool                          `json:"disable_progress_persistence,omitempty"`
	CodecsVideo                  []string                      `json:"codecs_video"`
	CodecsAudio                  []string                      `json:"codecs_audio"`
	Containers                   []string                      `json:"containers"`
	MaxResolution                string                        `json:"max_resolution"`
	HDR                          bool                          `json:"hdr"`
	HdrDetails                   *hdrDetails                   `json:"hdr_details,omitempty"`
	AudioPassthrough             *audioPassthroughCapabilities `json:"audio_passthrough,omitempty"`
}

// progressRequest represents the JSON body for POST /playback/{session_id}/progress.
type progressRequest struct {
	Position float64 `json:"position"`
	IsPaused bool    `json:"is_paused"`
}

// playbackSessionResponse represents a playback session in JSON responses.
type playbackSessionResponse struct {
	SessionID       string              `json:"session_id"`
	UserID          int                 `json:"user_id"`
	ProfileID       string              `json:"profile_id"`
	MediaFileID     int                 `json:"media_file_id"`
	PlayMethod      string              `json:"play_method"`
	Position        float64             `json:"position"`
	IsPaused        bool                `json:"is_paused"`
	StreamURL       string              `json:"stream_url"`
	AudioTrackIndex int                 `json:"audio_track_index"`
	DurationSeconds *float64            `json:"duration_seconds"`
	SubtitleURLs    []subtitleURL       `json:"subtitle_urls,omitempty"`
	PlaybackInfo    *playbackInfoResult `json:"playback_info,omitempty"`
}

type playbackInfoResult struct {
	StreamType     string `json:"stream_type"`
	TranscodeAudio bool   `json:"transcode_audio"`
	VideoCodec     string `json:"video_codec"`
	AudioCodec     string `json:"audio_codec"`
}

// subtitleURL represents a subtitle track URL in a playback response.
type subtitleURL struct {
	Index           int    `json:"index"`
	Language        string `json:"language"`
	Codec           string `json:"codec,omitempty"`
	Label           string `json:"label"`
	Source          string `json:"source"`
	Forced          bool   `json:"forced"`
	HearingImpaired bool   `json:"hearing_impaired"`
	URL             string `json:"url"`
	FontBundleURL   string `json:"font_bundle_url,omitempty"`
}

// changeAudioRequest represents the JSON body for PATCH /playback/{session_id}/audio.
type changeAudioRequest struct {
	AudioTrackIndex int     `json:"audio_track_index"`
	Position        float64 `json:"position"`
}

// changeAudioResponse represents the JSON response for PATCH /playback/{session_id}/audio.
type changeAudioResponse struct {
	AudioTrackIndex int                 `json:"audio_track_index"`
	PlayMethod      string              `json:"play_method"`
	StreamURL       string              `json:"stream_url"`
	SwitchMode      string              `json:"switch_mode"`
	PlaybackInfo    *playbackInfoResult `json:"playback_info,omitempty"`
}

type transcodeStartRequest struct {
	SessionID          string  `json:"session_id"`
	SeekSeconds        float64 `json:"seek_seconds"`
	TargetResolution   string  `json:"target_resolution"`
	TargetCodecVideo   string  `json:"target_codec_video"`
	TargetCodecAudio   string  `json:"target_codec_audio"`
	TargetBitrateKbps  int     `json:"target_bitrate_kbps"`
	SegmentDuration    int     `json:"segment_duration"`
	SubtitleTrackIndex int     `json:"subtitle_track_index"`
	SubtitleBurnIn     bool    `json:"subtitle_burn_in"`
}

type transcodeStartResponse struct {
	SessionID             string   `json:"session_id"`
	Status                string   `json:"status"`
	SwitchedFileID        *int     `json:"switched_file_id,omitempty"`
	ManifestURL           string   `json:"manifest_url"`
	DurationSeconds       *float64 `json:"duration_seconds"`
	PlayerStartSeconds    float64  `json:"player_start_seconds"`
	StreamOriginSeconds   float64  `json:"stream_origin_seconds"`
	TimelineOffsetSeconds float64  `json:"timeline_offset_seconds"`
	CanSeekAnywhere       bool     `json:"can_seek_anywhere"`
}

// toPlaybackSessionResponse converts a playback.Session to an API response.
func (h *PlaybackHandler) toPlaybackSessionResponse(s *playback.Session) playbackSessionResponse {
	return playbackSessionResponse{
		SessionID:       s.ID,
		UserID:          s.UserID,
		ProfileID:       s.ProfileID,
		MediaFileID:     s.MediaFileID,
		PlayMethod:      string(semanticPlayMethod(s)),
		Position:        s.Position,
		IsPaused:        s.IsPaused,
		StreamURL:       h.playbackStreamURL(s),
		AudioTrackIndex: s.AudioTrackIndex,
	}
}

func semanticPlayMethod(s *playback.Session) playback.PlayMethod {
	if s == nil {
		return ""
	}
	if s.BasePlayMethod != "" {
		return s.BasePlayMethod
	}
	return s.PlayMethod
}

func fileDurationSeconds(file *models.MediaFile) *float64 {
	if file == nil || file.Duration <= 0 {
		return nil
	}
	duration := float64(file.Duration)
	return &duration
}

func canSeekAnywhere(req transcodeStartRequest, file *models.MediaFile) bool {
	if file == nil || file.Duration <= 0 {
		return false
	}
	// Copy-video HLS sessions use FFmpeg's real manifest so the player only
	// seeks within the currently exposed window. Out-of-window seeks should
	// restart explicitly instead of relying on segment 404s to move FFmpeg.
	return !strings.EqualFold(req.TargetCodecVideo, "copy")
}

func buildTranscodeStartResponse(
	req transcodeStartRequest,
	file *models.MediaFile,
	switchedFileID *int,
	manifestURL string,
) transcodeStartResponse {
	resp := transcodeStartResponse{
		SessionID:       req.SessionID,
		Status:          "started",
		SwitchedFileID:  switchedFileID,
		ManifestURL:     manifestURL,
		DurationSeconds: fileDurationSeconds(file),
	}
	if canSeekAnywhere(req, file) {
		resp.PlayerStartSeconds = req.SeekSeconds
		resp.StreamOriginSeconds = 0
		resp.TimelineOffsetSeconds = 0
		resp.CanSeekAnywhere = true
		return resp
	}
	resp.PlayerStartSeconds = 0
	resp.StreamOriginSeconds = req.SeekSeconds
	resp.TimelineOffsetSeconds = req.SeekSeconds
	resp.CanSeekAnywhere = false
	return resp
}

func (h *PlaybackHandler) ensurePlaybackProbe(ctx context.Context, file *models.MediaFile) *models.MediaFile {
	if h == nil || h.ProbeEnsurer == nil || file == nil {
		return file
	}
	repaired, err := h.ProbeEnsurer.Ensure(ctx, file)
	if err != nil {
		slog.Warn("playback probe repair failed", "file_id", file.ID, "path", file.FilePath, "error", err)
		return file
	}
	if repaired != nil {
		return repaired
	}
	return file
}

// streamTokenParam is the query parameter that carries the signed stream token
// on the native integrated serve path. The token is the durable reconstruction
// descriptor: a front-end that lost its in-memory session rebuilds from it. It
// rides a query parameter (not a path segment) because the integrated server is
// hit directly by the client — there is no query-stripping proxy hop in between,
// and the transcode manifest rewriter already appends the request RawQuery to
// every segment URI, so segment requests inherit the token for free. The
// proxy/node path keeps the token in the URL path (see the proxy server).
const streamTokenParam = "st"

// signSessionToken mints a stream token carrying the session's full
// reconstruction recipe. Returns "" when no signing secret is configured
// (reconstruct effectively disabled, e.g. in tests).
func (h *PlaybackHandler) signSessionToken(card playback.RecipeCard) string {
	if h.JWTSecret == "" {
		return ""
	}
	token, err := streamtoken.Sign(card.ToClaims(), h.JWTSecret, playback.MaxTokenTTL)
	if err != nil {
		slog.Warn("sign stream token failed", "error", err, "session", card.SessionID, "playback_session_id", card.SessionID)
		return ""
	}
	return token
}

// streamCardFromQuery verifies the stream token in the request's ?st= parameter
// and returns the decoded reconstruction recipe, or nil when the token is
// absent, invalid/expired, or bound to a different session. A live session needs
// no token (the result is simply nil); the recipe is consumed only on
// reconstruct.
func (h *PlaybackHandler) streamCardFromQuery(r *http.Request, sessionID string) *playback.RecipeCard {
	return streamCardFromToken(r.URL.Query().Get(streamTokenParam), sessionID, h.JWTSecret)
}

// loadTranscodeServeSession resolves the playback Session for the transcode
// manifest/segment serve routes while keeping stream-token verification off the
// hot path. The overwhelmingly common case is a live in-memory session, which
// needs no token at all, so the cheap GetSession lookup runs first and the
// (HMAC + JSON) token decode is performed only on a not-found miss where a
// reconstruct is actually required. On that miss it delegates to the shared
// LoadOrReconstructSession front door so reconstruct/ownership semantics stay
// identical. The returned card (nil on the live-session path) is the decoded
// recipe the caller's own reconstruct branch consumes.
func (h *PlaybackHandler) loadTranscodeServeSession(r *http.Request, sessionID string) (*playback.Session, playback.SessionLoadStatus, *playback.RecipeCard) {
	requestUserID := apimw.GetUserID(r.Context())
	session, err := h.sessionMgr.GetSession(sessionID)
	if err == nil {
		// Live session: enforce the same ownership rule as LoadOrReconstructSession
		// (a zero caller is allowed; a non-zero mismatch is refused). No token
		// verification on this hot path.
		if requestUserID != 0 && session.UserID != requestUserID {
			return nil, playback.SessionForbidden, nil
		}
		return session, playback.SessionLoaded, nil
	}
	if !errors.Is(err, playback.ErrSessionNotFound) {
		return nil, playback.SessionLoadFailed, nil
	}
	// Genuine miss (e.g. after a restart): now — and only now — pay for the token
	// decode so the recipe is available for reconstruction.
	card := h.streamCardFromQuery(r, sessionID)
	session, status := h.tm.LoadOrReconstructSession(r.Context(), h.sessionMgr.GetSession, sessionID, requestUserID, card)
	return session, status, card
}

// streamCardFromToken verifies a stream token and decodes its reconstruction
// recipe, returning nil when the token is absent, unparseable/expired, or bound
// to a different session id. Shared by the native serve handlers (PlaybackHandler
// and StreamHandler).
func streamCardFromToken(tokenStr, sessionID, secret string) *playback.RecipeCard {
	if tokenStr == "" || secret == "" {
		return nil
	}
	claims, err := streamtoken.Verify(tokenStr, secret)
	if err != nil || claims.SessionID != sessionID {
		return nil
	}
	card := playback.RecipeCardFromClaims(claims)
	return &card
}

// appendStreamToken adds the ?st=<token> parameter to a native serve URL.
func appendStreamToken(rawURL, token string) string {
	if token == "" {
		return rawURL
	}
	sep := "?"
	if strings.ContainsRune(rawURL, '?') {
		sep = "&"
	}
	return rawURL + sep + streamTokenParam + "=" + token
}

// playbackStreamURL builds the native serve URL for a session and appends an
// identity stream token so a direct-play/remux session survives a restart (the
// client re-supplies its byte position). Transcode sessions receive their
// full-recipe manifest URL from HandleStartTranscode instead; the URL here is an
// informational placeholder the client replaces with that manifest.
func (h *PlaybackHandler) playbackStreamURL(s *playback.Session) string {
	if s == nil {
		return ""
	}
	if s.PlayMethod == playback.PlayTranscode {
		return fmt.Sprintf("/playback/transcode/%s/master.m3u8", s.ID)
	}
	card := identityRecipeCard(s)
	return appendStreamToken(fmt.Sprintf("/stream/%s", s.ID), h.signSessionToken(card))
}

// identityRecipeCard builds the identity-only recipe for a direct-play or remux
// session: reconstruction needs only ownership plus the audio selection, since
// the bytes are served by HTTP Range / a re-spawned remux pipe at the
// client-supplied position.
func identityRecipeCard(s *playback.Session) playback.RecipeCard {
	switch s.PlayMethod {
	case playback.PlayRemux:
		return playback.NewRemuxRecipeCard(s.ID, s.UserID, s.ProfileID, s.MediaFileID, s.TranscodeAudio, s.AudioTrackIndex)
	default:
		return playback.NewDirectRecipeCard(s.ID, s.UserID, s.ProfileID, s.MediaFileID)
	}
}

func fileBitrateKbps(file *models.MediaFile) int {
	if file == nil || file.Bitrate <= 0 {
		return 0
	}
	return file.Bitrate
}

func buildPlaybackInfo(session *playback.Session, file *models.MediaFile) *playbackInfoResult {
	if session == nil {
		return nil
	}

	info := &playbackInfoResult{
		TranscodeAudio: session.TranscodeAudio,
	}

	switch session.PlayMethod {
	case playback.PlayTranscode:
		info.StreamType = "hls"
		if strings.EqualFold(session.TargetVideoCodec, "copy") || session.TargetVideoCodec == "" {
			info.VideoCodec = sourceVideoCodec(file)
		} else {
			info.VideoCodec = session.TargetVideoCodec
		}
		if session.TranscodeAudio {
			info.AudioCodec = "aac"
		} else if strings.EqualFold(session.TargetAudioCodec, "copy") || session.TargetAudioCodec == "" {
			info.AudioCodec = sourceAudioCodec(file)
		} else {
			info.AudioCodec = session.TargetAudioCodec
		}
	case playback.PlayRemux, playback.PlayDirect:
		info.StreamType = "progressive"
		info.VideoCodec = sourceVideoCodec(file)
		if session.TranscodeAudio {
			info.AudioCodec = "aac"
		} else {
			info.AudioCodec = sourceAudioCodec(file)
		}
	default:
		info.StreamType = "progressive"
		info.VideoCodec = sourceVideoCodec(file)
		info.AudioCodec = sourceAudioCodec(file)
	}

	return info
}

func requestedMediaFileID(session *playback.Session) int {
	if session == nil {
		return 0
	}
	if session.RequestedMediaFileID > 0 {
		return session.RequestedMediaFileID
	}
	return session.MediaFileID
}

func (h *PlaybackHandler) loadFileByPreferredID(
	ctx context.Context,
	preferredID int,
	fallbackID int,
) (*models.MediaFile, error) {
	if h.fileResolver == nil {
		return nil, fmt.Errorf("file resolver not configured")
	}
	if preferredID > 0 {
		file, err := h.fileResolver.GetByID(ctx, preferredID)
		if err == nil && file != nil {
			return file, nil
		}
		if err != nil && (fallbackID == 0 || fallbackID == preferredID) {
			return nil, err
		}
	}
	if fallbackID > 0 && fallbackID != preferredID {
		return h.fileResolver.GetByID(ctx, fallbackID)
	}
	return nil, nil
}

func sourceVideoCodec(file *models.MediaFile) string {
	if file == nil {
		return ""
	}
	if len(file.VideoTracks) > 0 && file.VideoTracks[0].Codec != "" {
		return file.VideoTracks[0].Codec
	}
	return file.CodecVideo
}

func sourceAudioCodec(file *models.MediaFile) string {
	if file == nil {
		return ""
	}
	if len(file.AudioTracks) > 0 && file.AudioTracks[0].Codec != "" {
		return file.AudioTracks[0].Codec
	}
	return file.CodecAudio
}

func directPlayAudioTrackIndex(file *models.MediaFile) int {
	if file == nil || len(file.AudioTracks) == 0 {
		return 0
	}
	for i, track := range file.AudioTracks {
		if track.Default {
			return i
		}
	}
	return 0
}

func clientSupportsAudioCodec(req startPlaybackRequest, codec string) bool {
	if codec == "" {
		return true
	}
	if len(req.CodecsAudio) == 0 {
		return playback.BrowserSupportsAudioCodec(codec)
	}
	for _, supported := range req.CodecsAudio {
		if strings.EqualFold(supported, codec) {
			return true
		}
	}
	if req.AudioPassthrough != nil {
		for _, supported := range req.AudioPassthrough.PassthroughCodecs {
			if strings.EqualFold(supported, codec) {
				return true
			}
		}
	}
	return false
}

func adjustPlaybackForSelectedAudio(
	file *models.MediaFile,
	req startPlaybackRequest,
	method playback.PlayMethod,
	transcodeAudio bool,
	audioTrackIndex int,
	preserveDirectAudioSelection bool,
) (playback.PlayMethod, bool) {
	if file == nil || len(file.AudioTracks) == 0 || audioTrackIndex < 0 || audioTrackIndex >= len(file.AudioTracks) {
		return method, transcodeAudio
	}

	selectedTrack := file.AudioTracks[audioTrackIndex]
	audioSupported := clientSupportsAudioCodec(req, selectedTrack.Codec)

	switch method {
	case playback.PlayDirect:
		if preserveDirectAudioSelection {
			return playback.PlayDirect, false
		}
		// Direct play cannot force the browser onto a non-default audio stream.
		// Promote to remux so ffmpeg can map the selected track explicitly.
		if audioTrackIndex != directPlayAudioTrackIndex(file) {
			return playback.PlayRemux, !audioSupported
		}
		if !audioSupported {
			return playback.PlayRemux, true
		}
		return method, false
	case playback.PlayRemux:
		return method, !audioSupported
	default:
		return method, transcodeAudio
	}
}

func normalizeAudioTrackIndex(file *models.MediaFile, audioTrackIndex int) int {
	if file == nil || len(file.AudioTracks) == 0 {
		return 0
	}
	if audioTrackIndex >= 0 && audioTrackIndex < len(file.AudioTracks) {
		return audioTrackIndex
	}
	return directPlayAudioTrackIndex(file)
}

func playbackAdminSettingsFromRequest(ctx context.Context, repo PlaybackSettingsReader, transcodeEnabled bool) playback.AdminSettings {
	settings := playback.AdminSettings{
		TranscodeEnabled: transcodeEnabled,
	}
	if repo != nil {
		if v, _ := repo.Get(ctx, "allow_4k_transcode"); v == "true" {
			settings.Allow4KTranscode = true
		}
	}
	return settings
}

func resolvePlaybackMethodForFile(
	file *models.MediaFile,
	req startPlaybackRequest,
	audioTrackIndex int,
	adminSettings playback.AdminSettings,
) (playback.PlayMethod, bool) {
	if file == nil {
		return "", false
	}

	caps := playback.ClientCapabilities{
		CodecsVideo:   req.CodecsVideo,
		CodecsAudio:   req.CodecsAudio,
		Containers:    req.Containers,
		MaxResolution: req.MaxResolution,
		HDR:           req.HDR,
	}
	if req.AudioPassthrough != nil {
		caps.AudioPassthroughCodecs = req.AudioPassthrough.PassthroughCodecs
	}
	decision := playback.Resolve(file, caps, adminSettings)
	return adjustPlaybackForSelectedAudio(file, req, decision.Method, decision.TranscodeAudio, audioTrackIndex, false)
}

func (h *PlaybackHandler) resolveCapabilityPlaybackSelection(
	ctx context.Context,
	req startPlaybackRequest,
	requestedFile *models.MediaFile,
	audioTrackIndex int,
) (*models.MediaFile, playback.PlayMethod, bool, int) {
	if requestedFile == nil {
		return requestedFile, "", false, 0
	}

	audioTrackIndex = normalizeAudioTrackIndex(requestedFile, audioTrackIndex)
	adminSettings := playbackAdminSettingsFromRequest(ctx, h.SettingsRepo, h.playbackConfig().TranscodeEnabled)
	method, transcodeAudio := resolvePlaybackMethodForFile(requestedFile, req, audioTrackIndex, adminSettings)

	if requestedFile.Resolution == "2160p" &&
		method == playback.PlayTranscode &&
		!adminSettings.Allow4KTranscode &&
		h.FileVersionFetcher != nil {
		alt, err := h.findAlternateFile(ctx, requestedFile)
		if err == nil && alt != nil {
			effectiveFile := h.ensurePlaybackProbe(ctx, alt)
			audioTrackIndex = normalizeAudioTrackIndex(effectiveFile, audioTrackIndex)
			method, transcodeAudio = resolvePlaybackMethodForFile(effectiveFile, req, audioTrackIndex, adminSettings)
			return effectiveFile, method, transcodeAudio, audioTrackIndex
		}
	}

	return requestedFile, method, transcodeAudio, audioTrackIndex
}

func (h *PlaybackHandler) resolveSeriesID(ctx context.Context, file *models.MediaFile) string {
	if file.EpisodeID == "" || h.EpisodeLookup == nil {
		return ""
	}
	ep, err := h.EpisodeLookup.GetByID(ctx, file.EpisodeID)
	if err != nil || ep == nil {
		return ""
	}
	return ep.SeriesID
}

// resolveOriginalLanguage fetches the original language for a media file's content item.
// For episodes, it looks up the parent series. Returns empty string if unavailable.
func (h *PlaybackHandler) resolveOriginalLanguage(ctx context.Context, file *models.MediaFile) string {
	if h.OriginalLangLookup == nil {
		return ""
	}
	contentID := file.ContentID
	if file.EpisodeID != "" {
		contentID = h.resolveSeriesID(ctx, file)
	}
	if contentID == "" {
		return ""
	}
	lang, err := h.OriginalLangLookup.GetOriginalLanguage(ctx, contentID)
	if err != nil {
		return ""
	}
	return lang
}

func (h *PlaybackHandler) restoreSessionProgress(
	ctx context.Context,
	session *playback.Session,
	file *models.MediaFile,
) {
	if h.StoreProvider == nil || session == nil || file == nil {
		return
	}

	targetID := playbackProgressTarget(file)
	if targetID == "" {
		return
	}

	store, err := h.StoreProvider.ForUser(ctx, session.UserID)
	if err != nil {
		slog.Error("failed to get user store", "user_id", session.UserID, "error", err)
		return
	}

	progress, err := store.GetProgress(ctx, session.ProfileID, targetID)
	if err != nil {
		slog.Error("failed to load progress", "target", targetID, "error", err)
		return
	}

	if progress == nil || progress.Completed || progress.PositionSeconds <= 0 {
		return
	}

	if err := h.sessionMgr.UpdateProgress(session.ID, progress.PositionSeconds, false); err != nil {
		slog.Error("failed to restore progress", "session", session.ID, "error", err)
		return
	}

	session.Position = progress.PositionSeconds
	session.IsPaused = false
}

// --- Persistence helpers ---

// persistProgress saves the current playback position to the UserStore.
// It resolves the mediaFileID to a mediaItemID via the file resolver.
// Errors are logged but do not fail the HTTP request.
func (h *PlaybackHandler) persistProgress(ctx context.Context, session *playback.Session) {
	if h.StoreProvider == nil || h.fileResolver == nil {
		return
	}
	if session == nil || session.DisableProgressPersistence {
		return
	}

	file, err := h.loadFileByPreferredID(ctx, requestedMediaFileID(session), session.MediaFileID)
	targetID := playbackProgressTarget(file)
	if err != nil || targetID == "" {
		return // file not found or not yet matched to a media item
	}

	store, err := h.StoreProvider.ForUser(ctx, session.UserID)
	if err != nil {
		slog.Error("failed to get user store", "user_id", session.UserID, "error", err)
		return
	}

	duration := float64(file.Duration)
	if err := store.UpdateProgress(ctx, session.ProfileID, targetID, session.Position, duration, h.playbackThresholds(ctx)); err != nil {
		slog.Error("failed to persist progress", "session", session.ID, "error", err)
	} else {
		triggerProfileRefresh(ctx, h.profileStaler, h.profileRefreshRequester, session.UserID, session.ProfileID)
	}

	if err := store.UpdateProgressHints(ctx, session.ProfileID, targetID, userstore.VersionHints{
		FileID:     file.ID,
		Resolution: file.Resolution,
		HDR:        file.HDR,
		CodecVideo: file.CodecVideo,
		EditionKey: file.EditionKey,
	}); err != nil {
		slog.Error("failed to persist version hints", "session", session.ID, "error", err)
	}
}

// persistStopAndHistory saves the final position and adds a watch history entry
// when a playback session is stopped. Errors are logged but do not fail the
// HTTP request.
func (h *PlaybackHandler) persistStopAndHistory(ctx context.Context, session *playback.Session) watchstate.PlaybackStopResult {
	if h.StoreProvider == nil || h.fileResolver == nil {
		return watchstate.PlaybackStopResult{}
	}
	if session == nil || session.DisableProgressPersistence || session.Position <= 0 {
		return watchstate.PlaybackStopResult{}
	}

	file, err := h.loadFileByPreferredID(ctx, requestedMediaFileID(session), session.MediaFileID)
	targetID := playbackProgressTarget(file)
	if err != nil || targetID == "" {
		return watchstate.PlaybackStopResult{}
	}

	duration := float64(file.Duration)
	thresholds := h.playbackThresholds(ctx)
	watchSvc := watchstate.NewService(h.StoreProvider).
		WithStableIdentityResolver(h.StableIdentityResolver).
		WithCompletionObserver(h.CompletionObserver)
	stoppedAt := time.Now().UTC()
	result, err := watchSvc.RecordPlaybackStop(ctx, session.UserID, session.ProfileID, targetID, duration, session.Position, stoppedAt, userstore.VersionHints{
		FileID:     file.ID,
		Resolution: file.Resolution,
		HDR:        file.HDR,
		CodecVideo: file.CodecVideo,
		EditionKey: file.EditionKey,
	}, thresholds)
	if err != nil {
		slog.Error("failed to persist playback stop", "session", session.ID, "error", err)
	} else {
		triggerProfileRefresh(ctx, h.profileStaler, h.profileRefreshRequester, session.UserID, session.ProfileID)
	}
	return result
}

func (h *PlaybackHandler) scrobbleEventForSession(ctx context.Context, session *playback.Session, mediaItemID string, duration, position float64) watchsync.ScrobbleEvent {
	event := watchsync.ScrobbleEvent{
		PlaybackSessionID: session.ID,
		UserID:            session.UserID,
		ProfileID:         session.ProfileID,
		MediaItemID:       mediaItemID,
		PositionSeconds:   position,
		DurationSeconds:   duration,
		OccurredAt:        time.Now().UTC(),
	}
	if h.StableIdentityResolver == nil {
		event.Kind = "movie"
		return event
	}
	identity := h.StableIdentityResolver.ResolveHistoryIdentity(ctx, mediaItemID)
	event.Kind = identity.StableType
	if event.Kind == "" {
		event.Kind = "movie"
	}
	event.SeasonNumber = intPtrValue(identity.Season)
	event.EpisodeNumber = intPtrValue(identity.Episode)
	if identity.ProviderIDs != nil {
		event.IMDbID = identity.ProviderIDs["imdb"]
		event.TMDBID = identity.ProviderIDs["tmdb"]
		event.TVDBID = identity.ProviderIDs["tvdb"]
	}
	if identity.SeriesProviderIDs != nil {
		event.SeriesIMDbID = identity.SeriesProviderIDs["imdb"]
		event.SeriesTMDBID = identity.SeriesProviderIDs["tmdb"]
		event.SeriesTVDBID = identity.SeriesProviderIDs["tvdb"]
	}
	return event
}

func intPtrValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func (h *PlaybackHandler) buildAdminHistoryEntry(
	ctx context.Context,
	session *playback.Session,
) (*AdminPlaybackHistoryEntry, error) {
	if h.AdminStore == nil || h.fileResolver == nil || session == nil {
		return nil, nil
	}

	file, err := h.loadFileByPreferredID(ctx, requestedMediaFileID(session), session.MediaFileID)
	if err != nil {
		return nil, fmt.Errorf("loading media file: %w", err)
	}

	targetID := playbackProgressTarget(file)
	profileName := session.ProfileID
	if h.StoreProvider != nil {
		store, storeErr := h.StoreProvider.ForUser(ctx, session.UserID)
		if storeErr != nil {
			slog.Error("failed to get user store for admin history", "session", session.ID, "error", storeErr)
		} else if store != nil {
			profile, profileErr := store.GetProfile(ctx, session.ProfileID)
			if profileErr != nil {
				slog.Error("failed to load profile for admin history", "session", session.ID, "error", profileErr)
			} else if profile != nil && strings.TrimSpace(profile.Name) != "" {
				profileName = profile.Name
			}
		}
	}

	var durationPtr *float64
	completed := false
	if file != nil {
		duration := float64(file.Duration)
		durationPtr = &duration
		if duration > 0 && session.Position/duration > userstore.WatchedFraction(h.playbackThresholds(ctx).WatchedPct) {
			completed = true
		}
	}

	entry := &AdminPlaybackHistoryEntry{
		SessionID:       session.ID,
		UserID:          session.UserID,
		ProfileID:       session.ProfileID,
		ProfileName:     profileName,
		MediaItemID:     targetID,
		MediaFileID:     requestedMediaFileID(session),
		PlayMethod:      string(semanticPlayMethod(session)),
		StartedAt:       session.StartedAt.UTC().Format(time.RFC3339Nano),
		EndedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		WatchedSeconds:  session.Position,
		DurationSeconds: durationPtr,
		Completed:       completed,
		ClientIP:        clientip.FromContext(ctx),
	}
	return entry, nil
}

func (h *PlaybackHandler) syncSessionsNow(ctx context.Context, reason string) {
	if h.SessionSyncer == nil {
		return
	}
	if err := h.SessionSyncer.SyncNow(ctx); err != nil {
		slog.Error("failed to sync sessions", "reason", reason, "error", err)
	}
}

func (h *PlaybackHandler) touchSessionActivity(sessionID string) {
	if h == nil || sessionID == "" {
		return
	}
	if err := h.sessionMgr.TouchActivity(sessionID); err != nil && !errors.Is(err, playback.ErrSessionNotFound) {
		slog.Warn("failed to refresh playback activity", "session", sessionID, "error", err, "playback_session_id", sessionID)
	}
}

func (h *PlaybackHandler) finalizeSessionStop(ctx context.Context, session *playback.Session, syncNow bool, syncReason string, userInitiated bool) {
	if h == nil || session == nil || session.ID == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	stopResult := h.persistStopAndHistory(ctx, session)
	if h.WatchScrobbler != nil && stopResult.MediaItemID != "" {
		event := h.scrobbleEventForSession(ctx, session, stopResult.MediaItemID, stopResult.DurationSeconds, stopResult.FinalPositionSeconds)
		event.HistoryID = stopResult.HistoryID
		event.Completed = stopResult.Completed
		if stopResult.Completed {
			if err := h.WatchScrobbler.ScrobbleStop(ctx, event); err != nil {
				slog.Warn("failed to queue watch provider stop scrobble", "session", session.ID, "error", err)
			}
		} else if !stopResult.SkippedBelowMinResume {
			if err := h.WatchScrobbler.ScrobblePause(ctx, event); err != nil {
				slog.Warn("failed to queue watch provider pause scrobble", "session", session.ID, "error", err)
			}
		}
	}
	if entry, buildErr := h.buildAdminHistoryEntry(ctx, session); buildErr != nil {
		slog.Error("failed to build admin history", "session", session.ID, "error", buildErr)
	} else if entry != nil && h.AdminStore != nil {
		if err := h.AdminStore.RecordHistory(ctx, *entry); err != nil {
			slog.Error("failed to record admin history", "session", session.ID, "error", err)
		}
	}

	if h.AdminStore != nil {
		if err := h.AdminStore.DeleteSession(ctx, session.ID); err != nil {
			slog.Error("failed to delete synced session", "session", session.ID, "error", err)
		}
	}

	h.tm.CloseTranscodeSession(session.ID, session.TranscodeNodeURL)
	if syncNow {
		h.syncSessionsNow(ctx, syncReason)
	}
}

func (h *PlaybackHandler) finalizeSessionAbort(ctx context.Context, session *playback.Session, syncNow bool, syncReason string) {
	if h == nil || session == nil || session.ID == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if h.WatchScrobbler != nil && h.fileResolver != nil {
		if file, err := h.loadFileByPreferredID(ctx, requestedMediaFileID(session), session.MediaFileID); err == nil && file != nil {
			targetID := playbackProgressTarget(file)
			if targetID != "" {
				event := h.scrobbleEventForSession(ctx, session, targetID, float64(file.Duration), session.Position)
				if err := h.WatchScrobbler.ScrobblePause(ctx, event); err != nil {
					slog.Warn("failed to queue watch provider abort scrobble", "session", session.ID, "error", err)
				}
			}
		}
	}

	if h.AdminStore != nil {
		if err := h.AdminStore.DeleteSession(ctx, session.ID); err != nil {
			slog.Error("failed to delete synced session", "session", session.ID, "error", err)
		}
	}

	// Abort is a connection drop / non-terminal teardown — keep the recipe card
	// so the client can reconstruct on reconnect.
	h.tm.CloseTranscodeSession(session.ID, session.TranscodeNodeURL)
	if syncNow {
		h.syncSessionsNow(ctx, syncReason)
	}
}

func (h *PlaybackHandler) handleExpiredSession(session *playback.Session) {
	if h == nil || session == nil {
		return
	}
	sessionCopy := *session
	go func() {
		slog.Info("expired inactive playback session", "session", sessionCopy.ID, "playback_session_id", sessionCopy.ID)
		// Expiry is a liveness reap, not a user stop — keep the recipe card so a
		// resume reconstructs under the same id (the card's own TTL reaps it if
		// the session is truly abandoned).
		h.finalizeSessionStop(context.Background(), &sessionCopy, false, "", false)
	}()
}

func playbackProgressTarget(file *models.MediaFile) string {
	if file == nil {
		return ""
	}
	if file.EpisodeID != "" {
		return file.EpisodeID
	}
	return file.ContentID
}

func (h *PlaybackHandler) persistSeriesPlaybackPreference(
	ctx context.Context,
	userID int,
	profileID string,
	file *models.MediaFile,
) {
	if h.StoreProvider == nil || file == nil {
		return
	}

	seriesID := h.resolveSeriesID(ctx, file)
	if seriesID == "" {
		return
	}

	store, err := h.StoreProvider.ForUser(ctx, userID)
	if err != nil {
		slog.Error("failed to access user store for series playback preference", "user_id", userID, "error", err)
		return
	}

	if err := store.SetSeriesPlaybackPreference(ctx, userstore.SeriesPlaybackPreference{
		ProfileID:  profileID,
		SeriesID:   seriesID,
		Resolution: file.Resolution,
		HDR:        file.HDR,
		CodecVideo: file.CodecVideo,
	}); err != nil {
		slog.Error("failed to persist series playback preference", "series_id", seriesID, "profile_id", profileID, "error", err)
	}
}

func (h *PlaybackHandler) persistAudioPreference(
	ctx context.Context,
	userID int,
	profileID string,
	file *models.MediaFile,
	trackIndex int,
) {
	if h.StoreProvider == nil || file == nil || trackIndex < 0 || trackIndex >= len(file.AudioTracks) {
		return
	}

	seriesID := h.resolveSeriesID(ctx, file)
	if seriesID == "" {
		return
	}

	store, err := h.StoreProvider.ForUser(ctx, userID)
	if err != nil {
		slog.Error("failed to access user store for audio preference", "user_id", userID, "error", err)
		return
	}

	track := file.AudioTracks[trackIndex]
	if err := store.SetAudioPreference(ctx, userstore.AudioPreference{
		ProfileID:       profileID,
		SeriesID:        seriesID,
		AudioTrackIndex: trackIndex,
		AudioLanguage:   track.Language,
		TrackSignature:  playback.AudioTrackSignatureFromTrack(track),
	}); err != nil {
		slog.Error("failed to persist audio preference", "series_id", seriesID, "profile_id", profileID, "error", err)
	}
}

// --- Handler methods ---

// HandleStartPlayback handles POST /playback/start.
func (h *PlaybackHandler) HandleStartPlayback(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var req startPlaybackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.FileID == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "File ID is required")
		return
	}
	profileID := apimw.GetProfileID(r.Context())
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "X-Profile-Id header is required")
		return
	}
	if req.ProfileID != "" && req.ProfileID != profileID {
		writeError(w, http.StatusBadRequest, "bad_request", "profile_id must match X-Profile-Id")
		return
	}
	file, err := h.loadAuthorizedFile(r, req.FileID)
	if err != nil {
		switch {
		case errors.Is(err, catalog.ErrItemNotFound), errors.Is(err, catalog.ErrEpisodeNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Media file not found")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to authorize media file")
		}
		return
	}
	file = h.ensurePlaybackProbe(r.Context(), file)

	// Determine audio track.
	audioTrackIndex := 0
	if req.AudioTrackIndex != nil && *req.AudioTrackIndex >= 0 {
		audioTrackIndex = *req.AudioTrackIndex
	} else if file != nil && len(file.AudioTracks) > 0 && h.StoreProvider != nil {
		var seriesPref *playback.AudioTrackPreference
		var preferredLang string
		store, storeErr := h.StoreProvider.ForUser(r.Context(), userID)
		if storeErr == nil {
			seriesID := h.resolveSeriesID(r.Context(), file)
			if seriesID != "" {
				if ap, apErr := store.GetAudioPreference(r.Context(), profileID, seriesID); apErr == nil && ap != nil {
					seriesPref = &playback.AudioTrackPreference{
						AudioTrackIndex: ap.AudioTrackIndex,
						AudioLanguage:   ap.AudioLanguage,
						TrackSignature:  ap.TrackSignature,
					}
				}
			}
			if seriesPref != nil && seriesPref.AudioLanguage == playback.OriginalLanguageSentinel {
				seriesPref.AudioLanguage = h.resolveOriginalLanguage(r.Context(), file)
			}
			if profile, profErr := store.GetProfile(r.Context(), profileID); profErr == nil && profile != nil {
				preferredLang = profile.Language
			}

			// Resolve library override (if no series sticky pref exists).
			var libraryAudioLang string
			if seriesPref == nil {
				if lp, lpErr := store.GetLibraryPlaybackPreference(r.Context(), profileID, file.MediaFolderID); lpErr == nil && lp != nil && lp.AudioLanguage != "" {
					libraryAudioLang = lp.AudioLanguage
				}
			}

			// Resolve "original" sentinel at each preference level.
			needsOriginalLang := preferredLang == playback.OriginalLanguageSentinel ||
				libraryAudioLang == playback.OriginalLanguageSentinel
			if needsOriginalLang {
				originalLang := h.resolveOriginalLanguage(r.Context(), file)
				if preferredLang == playback.OriginalLanguageSentinel {
					preferredLang = originalLang
				}
				if libraryAudioLang == playback.OriginalLanguageSentinel {
					libraryAudioLang = originalLang
				}
			}

			// Apply library language override (skip if resolved to empty).
			if libraryAudioLang != "" {
				preferredLang = libraryAudioLang
			}
		}
		audioTrackIndex = playback.SelectAudioTrack(file.AudioTracks, preferredLang, seriesPref)
	}

	requestedFile := file
	effectiveFile := requestedFile
	method := playback.PlayMethod(req.PlayMethod)
	transcodeAudio := false

	// If the client sent codec capabilities and no explicit play method,
	// use the resolver to determine the best play strategy.
	if method == "" && h.fileResolver != nil && len(req.CodecsVideo) > 0 {
		effectiveFile, method, transcodeAudio, audioTrackIndex = h.resolveCapabilityPlaybackSelection(
			r.Context(),
			req,
			requestedFile,
			audioTrackIndex,
		)
	}

	if method == "" {
		method = playback.PlayDirect
	}
	audioTrackIndex = normalizeAudioTrackIndex(effectiveFile, audioTrackIndex)
	preserveDirectAudioSelection := method == playback.PlayDirect &&
		strings.EqualFold(req.PlayMethod, string(playback.PlayDirect)) &&
		req.PreserveDirectAudioSelection
	method, transcodeAudio = adjustPlaybackForSelectedAudio(
		effectiveFile,
		req,
		method,
		transcodeAudio,
		audioTrackIndex,
		preserveDirectAudioSelection,
	)
	if requestedFile != nil && effectiveFile != nil && requestedFile.ID != effectiveFile.ID {
		if err := preflightPlaybackFile(r.Context(), requestedFile, h.MissingMarker, h.EventsHub); err != nil && !isPlaybackFileMissing(err) {
			slog.Warn("requested playback file preflight failed; continuing with alternate file",
				"requested_file_id", requestedFile.ID,
				"effective_file_id", effectiveFile.ID,
				"error", err,
			)
		}
	}
	if err := preflightPlaybackFile(r.Context(), effectiveFile, h.MissingMarker, h.EventsHub); err != nil {
		writePlaybackFilePreflightError(w, err)
		return
	}

	clientInfo := playbackClientInfoFromRequest(r)
	sessionCtx := playback.WithClientInfo(r.Context(), clientInfo)
	var session *playback.Session
	if starter, ok := h.sessionMgr.(sessionStarterWithFilesContext); ok {
		session, err = starter.StartSessionWithFilesContext(
			sessionCtx,
			userID,
			profileID,
			effectiveFile.ID,
			req.FileID,
			method,
			transcodeAudio,
		)
	} else {
		session, err = h.sessionMgr.StartSessionWithFiles(
			userID,
			profileID,
			effectiveFile.ID,
			req.FileID,
			method,
			transcodeAudio,
		)
	}
	if err != nil {
		if errors.Is(err, playback.ErrTooManyStreams) {
			writeError(w, http.StatusTooManyRequests, "too_many_streams", "Too many concurrent streams")
			return
		}
		if errors.Is(err, playback.ErrTooManyTranscodes) {
			writeError(w, http.StatusTooManyRequests, "too_many_transcodes", "Too many concurrent transcodes")
			return
		}
		if errors.Is(err, playback.ErrPlaybackNotAllowed) {
			writeError(w, http.StatusForbidden, "playback_not_allowed", "Playback denied by server policy")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to start playback session")
		return
	}
	setPlaybackSessionLogContext(r, session.ID)
	if req.DisableProgressPersistence {
		if err := h.sessionMgr.SetProgressPersistenceDisabled(session.ID, true); err != nil {
			slog.Error("failed to disable progress persistence", "session", session.ID, "error", err)
		} else {
			session.DisableProgressPersistence = true
		}
	}

	if err := h.sessionMgr.UpdateAudioTrack(session.ID, audioTrackIndex, session.PlayMethod); err != nil {
		slog.Error("failed to set audio track", "session", session.ID, "error", err)
	}
	targetAudioCodec := ""
	if session.TranscodeAudio {
		targetAudioCodec = "aac"
	}
	streamBitrateKbps := 0
	if effectiveFile != nil {
		streamBitrateKbps = effectiveFile.Bitrate
	}
	if err := h.sessionMgr.UpdateStreamState(session.ID, playback.SessionStreamState{
		PlayMethod:        session.PlayMethod,
		BasePlayMethod:    session.BasePlayMethod,
		AudioTrackIndex:   audioTrackIndex,
		TranscodeAudio:    session.TranscodeAudio,
		ClientIP:          clientip.FromContext(r.Context()),
		ClientName:        clientInfo.Name,
		ClientVersion:     clientInfo.Version,
		ClientUserAgent:   clientInfo.UserAgent,
		StreamBitrateKbps: streamBitrateKbps,
		TargetAudioCodec:  targetAudioCodec,
	}); err != nil {
		slog.Error("failed to set stream state", "session", session.ID, "error", err)
	}
	session.AudioTrackIndex = audioTrackIndex
	session.ClientIP = clientip.FromContext(r.Context())
	session.StreamBitrateKbps = streamBitrateKbps
	session.TargetAudioCodec = targetAudioCodec
	h.persistSeriesPlaybackPreference(r.Context(), userID, profileID, effectiveFile)

	if req.StartPosition != nil {
		if err := h.sessionMgr.UpdateProgress(session.ID, *req.StartPosition, false); err != nil {
			slog.Error("failed to set explicit start position", "session", session.ID, "error", err)
		} else {
			session.Position = *req.StartPosition
			session.IsPaused = false
		}
	} else {
		h.restoreSessionProgress(r.Context(), session, file)
	}
	if !session.DisableProgressPersistence && h.WatchScrobbler != nil && effectiveFile != nil {
		targetID := playbackProgressTarget(effectiveFile)
		if targetID != "" {
			event := h.scrobbleEventForSession(r.Context(), session, targetID, float64(effectiveFile.Duration), session.Position)
			if err := h.WatchScrobbler.ScrobbleStart(r.Context(), event); err != nil {
				slog.Warn("failed to queue watch provider start scrobble", "session", session.ID, "error", err)
			}
		}
	}
	if h.ChapterThumbnailQueuer != nil && effectiveFile != nil {
		slog.Info(
			"queueing chapter thumbnails",
			"source",
			"playback_start",
			"content_id",
			effectiveFile.ContentID,
			"file_id",
			effectiveFile.ID,
			"target_seconds",
			session.Position,
		)
		h.ChapterThumbnailQueuer.QueuePriorityFileAtPosition(
			r.Context(),
			effectiveFile.ID,
			session.Position,
		)
	}
	h.maybeQueueLazyPlaybackMarkers(r.Context(), session, effectiveFile)

	// Direct-play and remux sessions reconstruct from the identity stream token
	// carried on their serve URL (see playbackStreamURL); there is no server-side
	// card to persist. Transcode sessions receive their full-recipe token from
	// HandleStartTranscode.
	resp := h.toPlaybackSessionResponse(session)
	resp.DurationSeconds = fileDurationSeconds(effectiveFile)
	resp.PlaybackInfo = buildPlaybackInfo(session, effectiveFile)

	var downloadedSubs []subtitles.DownloadedSubtitle
	if h.SubtitleRepo != nil && effectiveFile != nil {
		downloadedSubs, _ = h.SubtitleRepo.ListDownloadedSubtitles(r.Context(), effectiveFile.ID)
	}
	resp.SubtitleURLs = buildSubtitleURLs(session.ID, effectiveFile, downloadedSubs)

	// If stream nodes are available, generate proxy-based stream URLs.
	// Remux and transcode both use HLS via a transcode node, so the planner
	// picks the transcode node and its group's proxy together.
	if h.NodePlanner != nil && h.JWTSecret != "" {
		needsTranscode := session.PlayMethod == playback.PlayTranscode || session.PlayMethod == playback.PlayRemux
		plan := h.NodePlanner.PlanSession(session.ID, "", needsTranscode, fileBitrateKbps(effectiveFile))
		proxyNode := plan.ProxyNode
		if proxyNode != nil && (!needsTranscode || plan.TranscodeNode != nil) {
			tokenClaims := streamtoken.Claims{
				SessionID:   session.ID,
				PlayMethod:  string(session.PlayMethod),
				UserID:      session.UserID,
				ProfileID:   session.ProfileID,
				MediaFileID: session.MediaFileID,
			}

			// Resolve media path if possible.
			if effectiveFile != nil {
				tokenClaims.MediaPath = effectiveFile.FilePath
			}

			tokenClaims.TranscodeAudio = session.TranscodeAudio
			tokenClaims.AudioTrackIndex = session.AudioTrackIndex

			if plan.TranscodeNode != nil {
				tokenClaims.TranscodeNode = plan.TranscodeNode.URL
				_ = h.sessionMgr.SetTranscodeNodeURL(session.ID, plan.TranscodeNode.URL)
			}

			token, signErr := streamtoken.Sign(tokenClaims, h.JWTSecret, playback.MaxTokenTTL)
			if signErr == nil {
				switch session.PlayMethod {
				case playback.PlayDirect:
					resp.StreamURL = proxyNode.URL + "/stream/direct/" + token
				case playback.PlayRemux, playback.PlayTranscode:
					resp.StreamURL = proxyNode.URL + "/stream/transcode/" + token + "/master.m3u8"
				}

				// Update subtitle URLs to use proxy for embedded subs only.
				// External and downloaded subs stay on the API server since
				// the proxy doesn't have access to those files.
				embeddedOffset := 0
				if file != nil {
					embeddedOffset = len(file.ExternalSubtitles)
				}
				for i := range resp.SubtitleURLs {
					if resp.SubtitleURLs[i].Source == "embedded" {
						// Pass the ffmpeg-relative subtitle stream index to the proxy.
						embeddedIdx := resp.SubtitleURLs[i].Index - embeddedOffset
						proxySubtitleURL := proxyNode.URL + "/stream/subtitles/" + token + "/" + strconv.Itoa(embeddedIdx)
						resp.SubtitleURLs[i].URL = proxySubtitleURL + subtitleURLExt(resp.SubtitleURLs[i].Codec)
						if resp.SubtitleURLs[i].FontBundleURL != "" {
							resp.SubtitleURLs[i].FontBundleURL = proxySubtitleURL + "/fonts"
						}
					}
				}
			}
		}
	}

	h.syncSessionsNow(r.Context(), "start")
	writeJSON(w, http.StatusCreated, resp)
}

func playbackClientInfoFromRequest(r *http.Request) playback.ClientInfo {
	if r == nil {
		return playback.ClientInfo{}
	}
	return playback.ClientInfo{
		Name:      strings.TrimSpace(r.Header.Get("X-Silo-Client")),
		Version:   strings.TrimSpace(r.Header.Get("X-Silo-Client-Version")),
		UserAgent: r.UserAgent(),
	}
}

// subtitleURLExt returns the URL file extension for a subtitle codec.
// ASS/SSA tracks get ".ass" so the frontend can request raw ASS data for
// client-side rendering (JASSUB); PGS tracks get ".sup" for client-side
// bitmap rendering (libpgs); all other text formats get ".vtt".
func subtitleURLExt(codec string) string {
	switch {
	case playback.IsASS(codec):
		return ".ass"
	case playback.IsPGS(codec):
		return ".sup"
	}
	return ".vtt"
}

func buildSubtitleURLs(sessionID string, file *models.MediaFile, downloaded []subtitles.DownloadedSubtitle) []subtitleURL {
	if file == nil {
		return nil
	}

	urls := make([]subtitleURL, 0, len(file.ExternalSubtitles)+len(file.SubtitleTracks)+len(downloaded))

	for i, sub := range file.ExternalSubtitles {
		urls = append(urls, subtitleURL{
			Index:           i,
			Language:        sub.Language,
			Codec:           sub.Format,
			Label:           firstNonEmptyString(sub.Title, sub.EmbeddedTitle, filepath.Base(sub.Path), sub.Language),
			Source:          "external",
			Forced:          sub.Forced,
			HearingImpaired: sub.HearingImpaired,
			URL:             fmt.Sprintf("/stream/%s/subtitles/%d%s", sessionID, i, subtitleURLExt(sub.Format)),
		})
	}

	embeddedOffset := len(file.ExternalSubtitles)
	for i, track := range file.SubtitleTracks {
		// PGS bitmap tracks are deliverable as .sup streams for
		// client-side rendering; DVD/DVB bitmap tracks still have no
		// non-burn-in delivery path, so they stay hidden.
		if playback.NeedsBurnIn(track.Codec) && !playback.IsPGS(track.Codec) {
			continue
		}

		urls = append(urls, subtitleURL{
			Index:           embeddedOffset + i,
			Language:        track.Language,
			Codec:           track.Codec,
			Label:           firstNonEmptyString(track.Title, track.EmbeddedTitle, track.Language),
			Source:          "embedded",
			Forced:          track.Forced,
			HearingImpaired: track.HearingImpaired,
			URL:             fmt.Sprintf("/stream/%s/subtitles/%d%s", sessionID, embeddedOffset+i, subtitleURLExt(track.Codec)),
			FontBundleURL:   subtitleFontBundleURL(sessionID, embeddedOffset+i, track.Codec),
		})
	}

	downloadedOffset := embeddedOffset + len(file.SubtitleTracks)
	for i, dl := range downloaded {
		urls = append(urls, subtitleURL{
			Index:           downloadedOffset + i,
			Language:        dl.Language,
			Codec:           string(dl.Format),
			Label:           dl.ReleaseName + " (" + dl.Provider + ")",
			Source:          "downloaded",
			HearingImpaired: dl.HearingImpaired,
			URL:             fmt.Sprintf("/stream/%s/subtitles/%d%s", sessionID, downloadedOffset+i, subtitleURLExt(string(dl.Format))),
		})
	}

	return urls
}

func subtitleFontBundleURL(sessionID string, trackIndex int, codec string) string {
	if !playback.IsASS(codec) {
		return ""
	}
	return fmt.Sprintf("/stream/%s/subtitles/%d/fonts", sessionID, trackIndex)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// HandleUpdateProgress handles POST /playback/{session_id}/progress.
func (h *PlaybackHandler) HandleUpdateProgress(w http.ResponseWriter, r *http.Request) {
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
		if errors.Is(err, playback.ErrSessionNotFound) {
			writePlaybackSessionNotFound(w)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load playback session")
		return
	}
	if session.UserID != userID {
		writeError(w, http.StatusForbidden, "forbidden", "Session belongs to another user")
		return
	}
	wasPaused := session.IsPaused

	var req progressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	err = h.sessionMgr.UpdateProgress(sessionID, req.Position, req.IsPaused)
	if err != nil {
		if errors.Is(err, playback.ErrSessionNotFound) {
			writePlaybackSessionNotFound(w)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update progress")
		return
	}
	h.syncSessionsNow(r.Context(), "progress")

	// Persist progress to UserStore (best-effort).
	if sess, getErr := h.sessionMgr.GetSession(sessionID); getErr == nil {
		h.persistProgress(r.Context(), sess)
		if !sess.DisableProgressPersistence && h.WatchScrobbler != nil && wasPaused != sess.IsPaused {
			if file, loadErr := h.loadFileByPreferredID(r.Context(), requestedMediaFileID(sess), sess.MediaFileID); loadErr == nil && file != nil {
				targetID := playbackProgressTarget(file)
				if targetID != "" {
					event := h.scrobbleEventForSession(r.Context(), sess, targetID, float64(file.Duration), sess.Position)
					if sess.IsPaused {
						if err := h.WatchScrobbler.ScrobblePause(r.Context(), event); err != nil {
							slog.Warn("failed to queue watch provider pause scrobble", "session", sessionID, "error", err)
						}
					} else if err := h.WatchScrobbler.ScrobbleStart(r.Context(), event); err != nil {
						slog.Warn("failed to queue watch provider resume scrobble", "session", sessionID, "error", err)
					}
				}
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleStopPlayback handles DELETE /playback/{session_id}.
func (h *PlaybackHandler) HandleStopPlayback(w http.ResponseWriter, r *http.Request) {
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
		if errors.Is(err, playback.ErrSessionNotFound) {
			writePlaybackSessionNotFound(w)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load playback session")
		return
	}
	if session.UserID != userID {
		writeError(w, http.StatusForbidden, "forbidden", "Session belongs to another user")
		return
	}

	err = h.stopPlaybackSession(r.Context(), session, true)
	if err != nil {
		if errors.Is(err, playback.ErrSessionNotFound) {
			writePlaybackSessionNotFound(w)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to stop playback session")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleChangeAudioTrack handles PATCH /playback/{session_id}/audio.
func (h *PlaybackHandler) HandleChangeAudioTrack(w http.ResponseWriter, r *http.Request) {
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
		if errors.Is(err, playback.ErrSessionNotFound) {
			writePlaybackSessionNotFound(w)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load playback session")
		return
	}
	if session.UserID != userID {
		writeError(w, http.StatusForbidden, "forbidden", "Session belongs to another user")
		return
	}

	var req changeAudioRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	// Load file to validate track index.
	if h.fileResolver == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "File resolver not configured")
		return
	}
	file, err := h.fileResolver.GetByID(r.Context(), session.MediaFileID)
	if err != nil || file == nil {
		writeError(w, http.StatusNotFound, "not_found", "Media file not found")
		return
	}
	if req.AudioTrackIndex < 0 || req.AudioTrackIndex >= len(file.AudioTracks) {
		writeError(w, http.StatusBadRequest, "bad_request", "Audio track index out of range")
		return
	}

	baseMethod := semanticPlayMethod(session)
	newMethod := baseMethod
	transcodeAudio := session.TranscodeAudio

	newTrack := file.AudioTracks[req.AudioTrackIndex]
	audioCodecNeedsTranscode := !playback.BrowserSupportsAudioCodec(newTrack.Codec)

	if baseMethod == playback.PlayDirect {
		newMethod = playback.PlayRemux
		transcodeAudio = audioCodecNeedsTranscode
	} else if baseMethod == playback.PlayRemux {
		transcodeAudio = audioCodecNeedsTranscode
	} else if baseMethod == playback.PlayTranscode {
		transcodeAudio = true
	}

	targetResolution := ""
	targetVideoCodec := ""
	targetAudioCodec := ""
	targetBitrateKbps := 0
	streamBitrateKbps := session.StreamBitrateKbps
	if session.PlayMethod == playback.PlayTranscode {
		targetResolution = session.TargetResolution
		targetVideoCodec = session.TargetVideoCodec
		targetBitrateKbps = session.TargetBitrateKbps
		if newMethod == playback.PlayTranscode || transcodeAudio {
			targetAudioCodec = "aac"
		} else {
			targetAudioCodec = "copy"
		}
	} else if transcodeAudio {
		targetAudioCodec = "aac"
	}
	slog.Info("audio switch computed playback state",
		"playback_session_id", sessionID,
		"previous_base_play_method", baseMethod,
		"new_base_play_method", newMethod,
		"transport_play_method", session.PlayMethod,
		"audio_track_index", req.AudioTrackIndex,
		"audio_codec", newTrack.Codec,
		"transcode_audio", transcodeAudio,
	)
	if err := h.sessionMgr.UpdateAudioTrack(sessionID, req.AudioTrackIndex, newMethod); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update audio track")
		return
	}
	if err := h.sessionMgr.UpdateStreamState(sessionID, playback.SessionStreamState{
		PlayMethod:        session.PlayMethod,
		BasePlayMethod:    newMethod,
		AudioTrackIndex:   req.AudioTrackIndex,
		TranscodeAudio:    transcodeAudio,
		ClientIP:          session.ClientIP,
		StreamBitrateKbps: streamBitrateKbps,
		TargetResolution:  targetResolution,
		TargetVideoCodec:  targetVideoCodec,
		TargetAudioCodec:  targetAudioCodec,
		TargetBitrateKbps: targetBitrateKbps,
		// Carry the byte-affecting recipe forward: an audio switch changes only the
		// audio selection, so subtitles and cadence must survive the state update
		// (UpdateStreamState overwrites these fields unconditionally).
		SubtitleTrackIndex: session.SubtitleTrackIndex,
		SubtitleBurnIn:     session.SubtitleBurnIn,
		SegmentDuration:    session.SegmentDuration,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update stream state")
		return
	}

	// Handle transcode restart.
	if session.PlayMethod == playback.PlayTranscode {
		if ts := h.tm.GetTranscodeSession(sessionID); ts != nil {
			ts.SetAudioTrackIndex(req.AudioTrackIndex)
			seekSeconds := req.Position
			startSegment := computeStartSegment(seekSeconds, ts.Opts().SegmentDuration)
			// Throttler + exit monitor re-arm via the session's restart hook.
			if restartErr := h.tm.RestartSessionLocked(context.WithoutCancel(r.Context()), sessionID, ts, seekSeconds, startSegment); restartErr != nil {
				slog.Error("failed to restart transcode for audio switch", "session", sessionID, "error", restartErr)
			}
		}
	}

	updatedSession := *session
	updatedSession.AudioTrackIndex = req.AudioTrackIndex
	updatedSession.BasePlayMethod = newMethod
	if session.PlayMethod != playback.PlayTranscode || newMethod == playback.PlayTranscode {
		updatedSession.PlayMethod = newMethod
	}
	updatedSession.TranscodeAudio = transcodeAudio
	updatedSession.TargetResolution = targetResolution
	updatedSession.TargetVideoCodec = targetVideoCodec
	updatedSession.TargetAudioCodec = targetAudioCodec
	updatedSession.TargetBitrateKbps = targetBitrateKbps

	// The switched recipe travels in the freshly minted stream token on the new
	// serve URL below, so a post-restart reconstruct resumes with the switched
	// audio/method. For transcode the full-recipe manifest URL is rebuilt further
	// down (proxy or local); for direct/remux the identity token on StreamURL
	// carries the new audio selection.
	h.persistAudioPreference(r.Context(), userID, session.ProfileID, file, req.AudioTrackIndex)

	// For a local transcode, playbackStreamURL returns the bare manifest URL
	// without the full-recipe ?st= token, so a post-restart reconstruct would
	// fall back to the stale pre-switch token. Rebuild the signed manifest URL
	// from the live transcode opts, mirroring HandleStartTranscode. The proxy
	// branch below overrides this when a node plan picks a proxy/transcode node.
	streamURL := h.playbackStreamURL(&updatedSession)
	if updatedSession.PlayMethod == playback.PlayTranscode {
		if ts := h.tm.GetTranscodeSession(sessionID); ts != nil {
			card := playback.NewRecipeCard(updatedSession.UserID, updatedSession.ProfileID, updatedSession.MediaFileID, updatedSession.TranscodeNodeURL, ts.Opts())
			streamURL = appendStreamToken(
				fmt.Sprintf("/playback/transcode/%s/master.m3u8", sessionID),
				h.signSessionToken(card),
			)
		}
	}

	resp := changeAudioResponse{
		AudioTrackIndex: req.AudioTrackIndex,
		PlayMethod:      string(newMethod),
		StreamURL:       streamURL,
		SwitchMode:      "reload",
		PlaybackInfo:    buildPlaybackInfo(&updatedSession, file),
	}

	if h.NodePlanner != nil && h.JWTSecret != "" {
		needsTranscode := updatedSession.PlayMethod == playback.PlayTranscode
		estKbps := updatedSession.TargetBitrateKbps
		if estKbps <= 0 {
			estKbps = fileBitrateKbps(file)
		}
		plan := h.NodePlanner.PlanSession(sessionID, session.TranscodeNodeURL, needsTranscode, estKbps)
		if proxyNode := plan.ProxyNode; proxyNode != nil && (!needsTranscode || plan.TranscodeNode != nil) {
			// Remote (offloaded) transcode: the API server owns no local
			// TranscodeSession (the LOCAL restart block above was a no-op), so
			// the node's ffmpeg is still serving the OLD audio track. POST a
			// fresh /transcode/start with the new AudioTrackIndex — the node
			// tears down the existing session for this ID and restarts ffmpeg
			// (handleStart in internal/transcodenode/server.go), which IS the
			// remote restart mechanism — then mint the replacement proxy URL
			// from a FULL recipe card so a later node restart reconstructs with
			// the switched audio (the lean identity-only claims used for remux
			// below omit the byte-affecting encode fields and would 404).
			isOffloaded := strings.TrimSpace(session.TranscodeNodeURL) != ""
			if needsTranscode && plan.TranscodeNode != nil && isOffloaded {
				nodeURL := plan.TranscodeNode.URL
				_ = h.sessionMgr.SetTranscodeNodeURL(sessionID, nodeURL)

				seekSeconds := req.Position
				// Restart from the FULL live recipe, not a partial re-derivation.
				// An audio switch alters only audio selection — subtitle burn-in and
				// the segment cadence must be preserved, or the node re-encodes a
				// different byte stream (subtitles silently dropped, wrong cadence)
				// and signs that altered recipe into the new token. The session
				// retains these from the original start (finalizeTranscodeStart) or a
				// post-restart reconstruct, so recover them here. Embed a concrete
				// segment duration (not 0): the node's recipe token treats
				// SegmentDuration<=0 as "incomplete" and would 404 on a node restart.
				segmentDuration := session.SegmentDuration
				if segmentDuration <= 0 {
					segmentDuration = playback.DefaultSegmentDuration
				}
				subtitleTrackIndex := session.SubtitleTrackIndex
				subtitleBurnIn := session.SubtitleBurnIn
				startSegment := computeStartSegment(seekSeconds, segmentDuration)

				// Derive the encode recipe the same way HandleStartTranscode
				// does — from the durable session target fields plus the file —
				// changing only the audio track. SourceVideoCodec/TotalDuration
				// come from the file; the resolution/codec/bitrate targets and
				// hwaccel come from the session's persisted stream state.
				nodeReq := transcodenode.TranscodeStartRequest{
					SessionID:          sessionID,
					InputPath:          file.FilePath,
					SourceVideoCodec:   file.CodecVideo,
					SeekSeconds:        seekSeconds,
					StartSegmentNumber: startSegment,
					TargetResolution:   updatedSession.TargetResolution,
					TargetCodecVideo:   updatedSession.TargetVideoCodec,
					TargetCodecAudio:   updatedSession.TargetAudioCodec,
					TargetBitrateKbps:  updatedSession.TargetBitrateKbps,
					SegmentDuration:    segmentDuration,
					HWAccel:            session.TranscodeHWAccel,
					AudioTrackIndex:    req.AudioTrackIndex,
					SubtitleTrackIndex: subtitleTrackIndex,
					SubtitleBurnIn:     subtitleBurnIn,
					TotalDuration:      float64(file.Duration),
				}
				if strings.TrimSpace(nodeReq.HWAccel) == "" {
					nodeReq.HWAccel = h.playbackConfig().HWAccel
				}

				body, _ := json.Marshal(nodeReq)
				ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
				defer cancel()
				httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, nodeURL+"/transcode/start", bytes.NewReader(body))
				if reqErr != nil {
					slog.Error("failed to build remote transcode restart for audio switch", "session", sessionID, "node", nodeURL, "error", reqErr)
					writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build transcode request")
					return
				}
				httpReq.Header.Set("Content-Type", "application/json")
				httpReq.Header.Set("Authorization", "Bearer "+h.JWTSecret)

				nodeResp, doErr := http.DefaultClient.Do(httpReq)
				if doErr != nil {
					slog.Error("remote transcode restart for audio switch failed", "session", sessionID, "node", nodeURL, "error", doErr)
					writeError(w, http.StatusBadGateway, "transcode_node_unavailable", "Transcode node is unavailable")
					return
				}
				defer nodeResp.Body.Close()
				if nodeResp.StatusCode != http.StatusAccepted {
					slog.Error("remote transcode restart for audio switch rejected", "session", sessionID, "node", nodeURL, "status", nodeResp.StatusCode)
					writeError(w, http.StatusBadGateway, "transcode_start_failed", "Transcode node rejected the request")
					return
				}

				var startResp transcodenode.TranscodeStartResponse
				if decErr := json.NewDecoder(nodeResp.Body).Decode(&startResp); decErr != nil {
					slog.Warn("remote transcode restart response decode failed", "session", sessionID, "node", nodeURL, "error", decErr)
				}
				effectiveHWAccel := strings.TrimSpace(startResp.HWAccel)
				if effectiveHWAccel == "" {
					effectiveHWAccel = strings.TrimSpace(nodeReq.HWAccel)
				}

				card := playback.NewRecipeCard(updatedSession.UserID, updatedSession.ProfileID, updatedSession.MediaFileID, nodeURL, playback.TranscodeOpts{
					InputPath:          nodeReq.InputPath,
					SessionID:          nodeReq.SessionID,
					SourceVideoCodec:   nodeReq.SourceVideoCodec,
					SeekSeconds:        nodeReq.SeekSeconds,
					StartSegmentNumber: nodeReq.StartSegmentNumber,
					TargetResolution:   nodeReq.TargetResolution,
					TargetCodecVideo:   nodeReq.TargetCodecVideo,
					TargetCodecAudio:   nodeReq.TargetCodecAudio,
					TargetBitrateKbps:  nodeReq.TargetBitrateKbps,
					SegmentDuration:    nodeReq.SegmentDuration,
					HWAccel:            effectiveHWAccel,
					AudioTrackIndex:    nodeReq.AudioTrackIndex,
					SubtitleTrackIndex: nodeReq.SubtitleTrackIndex,
					SubtitleBurnIn:     nodeReq.SubtitleBurnIn,
					TotalDuration:      nodeReq.TotalDuration,
				})
				resp.StreamURL = h.buildProxyManifestURL(card, proxyNode)
			} else {
				// Remux, or a non-offloaded (locally served) transcode: no remote
				// node ffmpeg to restart, so carry the new audio selection on the
				// identity claims of the proxy serve URL, exactly as before. A
				// local transcode reconstructs from the API server's own state, so
				// the lean token is sufficient here.
				tokenClaims := streamtoken.Claims{
					SessionID:       sessionID,
					PlayMethod:      string(updatedSession.PlayMethod),
					MediaPath:       file.FilePath,
					TranscodeAudio:  updatedSession.TranscodeAudio,
					AudioTrackIndex: req.AudioTrackIndex,
					UserID:          updatedSession.UserID,
					ProfileID:       updatedSession.ProfileID,
					MediaFileID:     updatedSession.MediaFileID,
				}
				if plan.TranscodeNode != nil {
					tokenClaims.TranscodeNode = plan.TranscodeNode.URL
					_ = h.sessionMgr.SetTranscodeNodeURL(sessionID, plan.TranscodeNode.URL)
				}
				if token, signErr := streamtoken.Sign(tokenClaims, h.JWTSecret, playback.MaxTokenTTL); signErr == nil {
					switch updatedSession.PlayMethod {
					case playback.PlayRemux:
						resp.StreamURL = proxyNode.URL + "/stream/remux/" + token
					case playback.PlayTranscode:
						resp.StreamURL = proxyNode.URL + "/stream/transcode/" + token + "/master.m3u8"
					}
				}
			}
		}
	}

	h.syncSessionsNow(r.Context(), "audio_change")
	writeJSON(w, http.StatusOK, resp)
}

func (h *PlaybackHandler) loadAuthorizedFile(r *http.Request, fileID int) (*models.MediaFile, error) {
	if h.fileResolver == nil || h.ItemAccess == nil {
		return nil, fmt.Errorf("playback authorization dependencies not configured")
	}
	file, err := h.fileResolver.GetByID(r.Context(), fileID)
	if err != nil {
		return nil, mapMediaFileLookupError(err)
	}
	if file == nil || file.MissingSince != nil {
		return nil, catalog.ErrItemNotFound
	}

	filter := requestAccessFilter(r)
	switch {
	case file.EpisodeID != "":
		if h.EpisodeLookup == nil {
			return nil, fmt.Errorf("episode lookup not configured")
		}
		episode, err := h.EpisodeLookup.GetByID(r.Context(), file.EpisodeID)
		if err != nil {
			return nil, err
		}
		if episode == nil {
			return nil, catalog.ErrEpisodeNotFound
		}
		if err := h.ItemAccess.EnsureAccessible(r.Context(), episode.SeriesID, filter); err != nil {
			return nil, err
		}
	case file.ContentID != "":
		if err := h.ItemAccess.EnsureAccessible(r.Context(), file.ContentID, filter); err != nil {
			return nil, err
		}
	default:
		return nil, catalog.ErrItemNotFound
	}

	if !catalog.FileAllowedByAccess(file, filter) {
		return nil, catalog.ErrItemNotFound
	}

	return file, nil
}

// computeStartSegment returns the HLS segment number corresponding to a seek
// position given the segment duration. Both remote and local transcode paths
// use this to align ffmpeg output filenames with the VOD manifest.
func computeStartSegment(seekSeconds float64, segmentDuration int) int {
	if segmentDuration <= 0 {
		segmentDuration = 2
	}
	if seekSeconds <= 0 {
		return 0
	}
	return int(seekSeconds / float64(segmentDuration))
}

// transcodeStartState holds the common parameters needed to finalize a
// transcode start (update session state, log, and sync) for both remote
// and local paths.
type transcodeStartState struct {
	req            transcodeStartRequest
	file           *models.MediaFile
	session        *playback.Session
	switchedFileID *int
	hwAccel        string
}

// finalizeTranscodeStart updates the playback session state after a transcode
// has been started (either locally or on a remote node).
func (h *PlaybackHandler) finalizeTranscodeStart(r *http.Request, st transcodeStartState) {
	if st.switchedFileID != nil {
		if err := h.sessionMgr.SetEffectiveMediaFileID(st.req.SessionID, *st.switchedFileID); err != nil {
			slog.Error("failed to update effective media file", "session", st.req.SessionID, "error", err, "playback_session_id", st.req.SessionID)
		}
	}

	streamBitrateKbps := st.req.TargetBitrateKbps
	if streamBitrateKbps <= 0 {
		streamBitrateKbps = st.file.Bitrate
	}
	transcodeAudio := st.req.TargetCodecAudio != "" && !strings.EqualFold(st.req.TargetCodecAudio, "copy")
	baseMethod := semanticPlayMethod(st.session)

	slog.Info("transcode start preserved base playback state",
		"playback_session_id", st.req.SessionID,
		"base_play_method", baseMethod,
		"transport_play_method", playback.PlayTranscode,
		"audio_track_index", st.session.AudioTrackIndex,
		"target_codec_video", st.req.TargetCodecVideo,
		"target_codec_audio", st.req.TargetCodecAudio,
		"copy_video_original", strings.EqualFold(st.req.TargetCodecVideo, "copy"),
		"transcode_audio", transcodeAudio,
	)

	// Persist the byte-affecting recipe (subtitles + segment cadence) so a later
	// offloaded audio switch can rebuild the exact same stream. The session is the
	// only recovery source for offloaded transcodes (no local ts.Opts()). Normalize
	// the cadence to a concrete value so the restart never falls back to 0.
	segmentDuration := st.req.SegmentDuration
	if segmentDuration <= 0 {
		segmentDuration = playback.DefaultSegmentDuration
	}

	if err := h.sessionMgr.UpdateStreamState(st.req.SessionID, playback.SessionStreamState{
		PlayMethod:         playback.PlayTranscode,
		BasePlayMethod:     baseMethod,
		AudioTrackIndex:    st.session.AudioTrackIndex,
		TranscodeAudio:     transcodeAudio,
		ClientIP:           st.session.ClientIP,
		StreamBitrateKbps:  streamBitrateKbps,
		TargetResolution:   st.req.TargetResolution,
		TargetVideoCodec:   st.req.TargetCodecVideo,
		TargetAudioCodec:   st.req.TargetCodecAudio,
		TargetBitrateKbps:  st.req.TargetBitrateKbps,
		TranscodeHWAccel:   st.hwAccel,
		SubtitleTrackIndex: st.req.SubtitleTrackIndex,
		SubtitleBurnIn:     st.req.SubtitleBurnIn,
		SegmentDuration:    segmentDuration,
	}); err != nil {
		slog.Error("failed to update transcode stream state", "session", st.req.SessionID, "error", err, "playback_session_id", st.req.SessionID)
	}

	h.syncSessionsNow(r.Context(), "transcode_start")
}

// HandleStartTranscode handles POST /playback/transcode/start.
func (h *PlaybackHandler) HandleStartTranscode(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var req transcodeStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.SessionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "session_id is required")
		return
	}
	setPlaybackSessionLogContext(r, req.SessionID)

	session, err := h.sessionMgr.GetSession(req.SessionID)
	if err != nil {
		if errors.Is(err, playback.ErrSessionNotFound) {
			writePlaybackSessionNotFound(w)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load playback session")
		return
	}
	if session.UserID != userID {
		writeError(w, http.StatusForbidden, "forbidden", "Session belongs to another user")
		return
	}
	// Close any existing transcode so a new one can start at different quality.
	// Check both local sessions AND remote node assignments — without the
	// remote check, switching quality on a transcode node never sends DELETE,
	// leaving the old ffmpeg running and its segments on disk.
	if h.tm.GetTranscodeSession(req.SessionID) != nil || session.TranscodeNodeURL != "" {
		// Restarting the transcode under the SAME session id (quality/seek
		// change) — keep the card; it is re-saved with the new opts below.
		h.tm.CloseTranscodeSession(req.SessionID, session.TranscodeNodeURL)
	}
	abortCurrentSession := func(reason string, cause error) {
		if abortErr := h.abortPlaybackSession(r.Context(), session); abortErr != nil && !errors.Is(abortErr, playback.ErrSessionNotFound) {
			slog.Error("failed to abort playback session",
				"session", req.SessionID,
				"reason", reason,
				"cause", cause,
				"error", abortErr,
				"playback_session_id", req.SessionID,
			)
		}
	}

	file, err := h.fileResolver.GetByID(r.Context(), session.MediaFileID)
	if err != nil {
		if isPlaybackFileLookupMissing(err) {
			abortCurrentSession("load_media_file", err)
			writeError(w, http.StatusNotFound, "not_found", "Media file not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load media file")
		return
	}
	if file == nil {
		abortCurrentSession("load_media_file", nil)
		writeError(w, http.StatusNotFound, "not_found", "Media file not found")
		return
	}
	file = h.ensurePlaybackProbe(r.Context(), file)
	requestedFile := file

	// Resume and seek-start requests generally cannot safely stream-copy video
	// into HLS output. Arbitrary HEVC seek points often land on non-keyframes,
	// which can leave Chromium stuck on a frozen frame while audio continues
	// advancing. MPEG-2 compatibility HLS is allowed to keep copy-video so Apple
	// devices can avoid a full video transcode for those files.
	if req.SeekSeconds > 0 && strings.EqualFold(req.TargetCodecVideo, "copy") && !playback.IsMPEG2VideoCodec(file.CodecVideo) {
		slog.Info("forcing video transcode for seeked copy request",
			"playback_session_id", req.SessionID,
			"seek_seconds", req.SeekSeconds,
			"source_video_codec", file.CodecVideo,
			"requested_target_codec_video", req.TargetCodecVideo,
			"effective_target_codec_video", "h264",
		)
		req.TargetCodecVideo = "h264"
	}

	// 4K transcode guard: if source is 4K and allow_4k_transcode is disabled,
	// switch to an alternate non-4K file version for transcoding.
	// Skip the guard when target_codec_video is "copy" — no actual video
	// encoding happens, so the 4K cost concern doesn't apply.
	var switchedFileID *int
	videoCopy := strings.EqualFold(req.TargetCodecVideo, "copy")
	if file.Resolution == "2160p" && h.SettingsRepo != nil && !videoCopy {
		allow4K, _ := h.SettingsRepo.Get(r.Context(), "allow_4k_transcode")
		if allow4K != "true" {
			alt, altErr := h.findAlternateFile(r.Context(), file)
			if altErr != nil || alt == nil {
				writeError(w, http.StatusUnprocessableEntity, "no_alternate_version",
					"No lower resolution version available for transcoding")
				return
			}
			file = alt
			file = h.ensurePlaybackProbe(r.Context(), file)
			switchedFileID = &alt.ID
		}
	}
	if requestedFile != nil && file != nil && requestedFile.ID != file.ID {
		if err := preflightPlaybackFile(r.Context(), requestedFile, h.MissingMarker, h.EventsHub); err != nil && !isPlaybackFileMissing(err) {
			slog.Warn("requested transcode file preflight failed; continuing with alternate file",
				"requested_file_id", requestedFile.ID,
				"effective_file_id", file.ID,
				"error", err,
			)
		}
	}
	if err := preflightPlaybackFile(r.Context(), file, h.MissingMarker, h.EventsHub); err != nil {
		if isPlaybackFileMissing(err) {
			abortCurrentSession("preflight_file", err)
		}
		writePlaybackFilePreflightError(w, err)
		return
	}

	// Determine whether to run locally or forward to a remote transcode node.
	var plan nodepool.Plan
	if h.NodePlanner != nil {
		estKbps := req.TargetBitrateKbps
		if estKbps <= 0 {
			estKbps = fileBitrateKbps(file)
		}
		plan = h.NodePlanner.PlanSession(req.SessionID, session.TranscodeNodeURL, true, estKbps)
	}
	tcNode := plan.TranscodeNode

	if tcNode != nil {
		// Remote transcode: forward to the assigned node.
		if err := h.sessionMgr.SetTranscodeNodeURL(req.SessionID, tcNode.URL); err != nil {
			slog.Error("set transcode node URL", "error", err, "session", req.SessionID, "playback_session_id", req.SessionID)
		}

		nodeReq := transcodenode.TranscodeStartRequest{
			SessionID:          req.SessionID,
			InputPath:          file.FilePath,
			SourceVideoCodec:   file.CodecVideo,
			SeekSeconds:        req.SeekSeconds,
			StartSegmentNumber: computeStartSegment(req.SeekSeconds, req.SegmentDuration),
			TargetResolution:   req.TargetResolution,
			TargetCodecVideo:   req.TargetCodecVideo,
			TargetCodecAudio:   req.TargetCodecAudio,
			TargetBitrateKbps:  req.TargetBitrateKbps,
			SegmentDuration:    req.SegmentDuration,
			HWAccel:            h.playbackConfig().HWAccel,
			AudioTrackIndex:    session.AudioTrackIndex,
			SubtitleTrackIndex: req.SubtitleTrackIndex,
			SubtitleBurnIn:     req.SubtitleBurnIn,
			TotalDuration:      float64(file.Duration),
		}

		body, _ := json.Marshal(nodeReq)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tcNode.URL+"/transcode/start", bytes.NewReader(body))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build transcode request")
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+h.JWTSecret)

		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			slog.Error("remote transcode start failed", "error", err, "node", tcNode.URL, "session", req.SessionID, "playback_session_id", req.SessionID)
			writeError(w, http.StatusBadGateway, "transcode_node_unavailable", "Transcode node is unavailable")
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusAccepted {
			slog.Error("remote transcode start rejected", "status", resp.StatusCode, "node", tcNode.URL)
			writeError(w, http.StatusBadGateway, "transcode_start_failed", "Transcode node rejected the request")
			return
		}
		var nodeResp transcodenode.TranscodeStartResponse
		if err := json.NewDecoder(resp.Body).Decode(&nodeResp); err != nil {
			slog.Warn("remote transcode start response decode failed",
				"error", err,
				"node", tcNode.URL,
				"session", req.SessionID,
				"playback_session_id", req.SessionID,
			)
		}
		effectiveHWAccel := strings.TrimSpace(nodeResp.HWAccel)
		if effectiveHWAccel == "" {
			effectiveHWAccel = strings.TrimSpace(nodeReq.HWAccel)
		}

		// The remote transcode's full recipe rides the proxy manifest token so the
		// integrated server can re-bind and re-proxy the session after a restart
		// (and a node could someday self-reconstruct from it). Node-side segment
		// reconstruction is a follow-up (see spec multi-node section).
		card := playback.NewRecipeCard(session.UserID, session.ProfileID, session.MediaFileID, tcNode.URL, playback.TranscodeOpts{
			InputPath:          nodeReq.InputPath,
			SessionID:          nodeReq.SessionID,
			SourceVideoCodec:   nodeReq.SourceVideoCodec,
			SeekSeconds:        nodeReq.SeekSeconds,
			StartSegmentNumber: nodeReq.StartSegmentNumber,
			TargetResolution:   nodeReq.TargetResolution,
			TargetCodecVideo:   nodeReq.TargetCodecVideo,
			TargetCodecAudio:   nodeReq.TargetCodecAudio,
			TargetBitrateKbps:  nodeReq.TargetBitrateKbps,
			SegmentDuration:    nodeReq.SegmentDuration,
			HWAccel:            effectiveHWAccel,
			AudioTrackIndex:    nodeReq.AudioTrackIndex,
			SubtitleTrackIndex: nodeReq.SubtitleTrackIndex,
			SubtitleBurnIn:     nodeReq.SubtitleBurnIn,
			TotalDuration:      nodeReq.TotalDuration,
		})
		manifestURL := h.buildProxyManifestURL(card, plan.ProxyNode)
		h.finalizeTranscodeStart(r, transcodeStartState{
			req:            req,
			file:           file,
			session:        session,
			switchedFileID: switchedFileID,
			hwAccel:        effectiveHWAccel,
		})
		writeJSON(w, http.StatusAccepted, buildTranscodeStartResponse(req, file, switchedFileID, manifestURL))
		return
	}

	// Local transcode (integrated mode — no transcode nodes available).
	// In distributed mode admins can disable this fallback so the API server
	// never transcodes when no eligible node exists.
	if h.NodePlanner != nil && !nodepool.LocalTranscodeFallbackAllowed(r.Context(), h.SettingsRepo) {
		writeError(w, http.StatusServiceUnavailable, "no_transcode_node",
			"No transcode node is available and local transcode fallback is disabled")
		return
	}
	// Snapshot once so the directory, ffmpeg path, and hwaccel of this
	// session stay consistent even if the config reloads mid-start.
	playbackCfg := h.playbackConfig()
	if err := os.MkdirAll(playbackCfg.TranscodeDir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to prepare transcode directory")
		return
	}

	// Hold the per-session lifecycle lock across teardown → spawn → register so a
	// concurrent reconstruct (or another fresh start) cannot run a second ffmpeg
	// writer against this session's output directory. The in-lock close tears down
	// any session a reconstruct rebuilt between the earlier close and here so the
	// fresh ffmpeg is the sole writer.
	unlock := h.tm.LockSessionLifecycle(req.SessionID)
	h.tm.CloseTranscodeSession(req.SessionID, "")
	transcodeSession, err := playback.StartTranscode(context.WithoutCancel(r.Context()), playback.TranscodeOpts{
		InputPath:          file.FilePath,
		OutputDir:          filepath.Join(playbackCfg.TranscodeDir, req.SessionID),
		SessionID:          req.SessionID,
		SourceVideoCodec:   file.CodecVideo,
		SeekSeconds:        req.SeekSeconds,
		StartSegmentNumber: computeStartSegment(req.SeekSeconds, req.SegmentDuration),
		TargetResolution:   req.TargetResolution,
		TargetCodecVideo:   req.TargetCodecVideo,
		TargetCodecAudio:   req.TargetCodecAudio,
		TargetBitrateKbps:  req.TargetBitrateKbps,
		SegmentDuration:    req.SegmentDuration,
		FFmpegPath:         playbackCfg.FFmpegPath,
		HWAccel:            playbackCfg.HWAccel,
		HWDevice:           playbackCfg.HWDevice,
		AudioTrackIndex:    session.AudioTrackIndex,
		SubtitleTrackIndex: req.SubtitleTrackIndex,
		SubtitleBurnIn:     req.SubtitleBurnIn,
		TotalDuration:      float64(file.Duration),
		FastStart:          true,
		NodeType:           "integrated",
		ExecutionMode:      "integrated",
		FFmpegLogSink:      h.FFmpegLogSink,
	})
	if err != nil {
		unlock()
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to start transcode session")
		return
	}

	h.tm.RegisterTranscodeSession(req.SessionID, transcodeSession)
	unlock()

	// Re-arm the throttler and exit monitor after every Restart of this
	// handler-created session, regardless of which code path triggers it
	// (web segment recovery or an audio switch). Sessions created by
	// jellycompat's own StartTranscode path never had throttler/exit-monitor
	// wiring, so they are unaffected.
	transcodeSession.SetRestartHook(func(ctx context.Context) {
		h.maybeStartThrottler(ctx, transcodeSession)
		h.tm.MonitorLocalTranscodeExit(req.SessionID, transcodeSession)
	})

	h.maybeStartThrottler(r.Context(), transcodeSession)
	h.tm.MonitorLocalTranscodeExit(req.SessionID, transcodeSession)

	// The full reconstruction recipe rides the manifest token so this local
	// transcode can be rebuilt after a server restart (the client re-presents the
	// token on its next manifest/segment request). The token is carried as a
	// query parameter; the manifest rewriter propagates it onto every segment URI.
	card := playback.NewRecipeCard(session.UserID, session.ProfileID, session.MediaFileID, "", transcodeSession.Opts())
	manifestURL := appendStreamToken(
		fmt.Sprintf("/playback/transcode/%s/master.m3u8", req.SessionID),
		h.signSessionToken(card),
	)
	h.finalizeTranscodeStart(r, transcodeStartState{
		req:            req,
		file:           file,
		session:        session,
		switchedFileID: switchedFileID,
		hwAccel:        transcodeSession.Opts().HWAccel,
	})
	writeJSON(w, http.StatusAccepted, buildTranscodeStartResponse(req, file, switchedFileID, manifestURL))
}

// HandleGetTranscodeManifest handles GET /playback/transcode/{session_id}/master.m3u8.
// Auth is optional — the session UUID serves as an access token (same pattern
// as /stream/{session_id}). When auth context is present, ownership is verified.
//
// Known-duration sessions expose a synthetic full VOD manifest so the player
// can seek immediately. Copy-mode seeks that would start mid-GOP are forced to
// encoded HLS earlier in HandleStartTranscode; otherwise BuildPlaybackManifest
// still uses the same synthetic VOD path when the session duration is known.
func (h *PlaybackHandler) HandleGetTranscodeManifest(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	session, status, card := h.loadTranscodeServeSession(r, sessionID)
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

	transcodeSession := h.tm.GetTranscodeSession(sessionID)
	if transcodeSession == nil {
		// No local session — try proxying to remote transcode node.
		if session.TranscodeNodeURL != "" {
			h.touchSessionActivity(sessionID)
			h.proxyToTranscodeNode(w, r, session.TranscodeNodeURL,
				"/transcode/"+sessionID+"/master.m3u8")
			return
		}
		// Local transcode whose process state was lost: reconstruct it from the
		// token recipe. The manifest path has no segment context, so pass -1 (use
		// the token's seek position).
		if card == nil {
			writeError(w, http.StatusNotFound, "not_found", "Transcode session not found")
			return
		}
		transcodeSession = h.tm.ReconstructTranscode(r.Context(), sessionID, -1, *card)
		if transcodeSession == nil {
			writeError(w, http.StatusNotFound, "not_found", "Transcode session not found")
			return
		}
	}
	h.touchSessionActivity(sessionID)

	manifest, err := transcodeSession.BuildPlaybackManifest("segment/", r.URL.RawQuery)
	if err != nil {
		slog.Error("build transcode manifest", "error", err, "session", sessionID, "playback_session_id", sessionID)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Transcode manifest not ready")
		return
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(manifest)
}

// HandleGetTranscodeSegment handles GET /playback/transcode/{session_id}/segment/{name}.
// Auth is optional — the session UUID serves as an access token.
func (h *PlaybackHandler) HandleGetTranscodeSegment(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	session, status, card := h.loadTranscodeServeSession(r, sessionID)
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

	transcodeSession := h.tm.GetTranscodeSession(sessionID)
	if transcodeSession == nil {
		if session.TranscodeNodeURL != "" {
			h.touchSessionActivity(sessionID)
			segmentName := chi.URLParam(r, "name")
			h.proxyToTranscodeNode(w, r, session.TranscodeNodeURL,
				"/transcode/"+sessionID+"/segment/"+segmentName)
			return
		}
		// Resume near the segment the client is fetching so reconstruct does not
		// restart from the original seek point and stall. A non-segment name
		// (e.g. init.mp4) parses as negative and falls back to the token position.
		requestedSegment := -1
		if segNum, parseErr := playback.ParseSegmentNumber(chi.URLParam(r, "name")); parseErr == nil {
			requestedSegment = segNum
		}
		if card == nil {
			writeError(w, http.StatusNotFound, "not_found", "Transcode session not found")
			return
		}
		transcodeSession = h.tm.ReconstructTranscode(r.Context(), sessionID, requestedSegment, *card)
		if transcodeSession == nil {
			writeError(w, http.StatusNotFound, "not_found", "Transcode session not found")
			return
		}
	}
	h.touchSessionActivity(sessionID)

	segmentName := chi.URLParam(r, "name")
	segmentPath, err := transcodeSession.GetSegment(segmentName)
	if err != nil && errors.Is(err, playback.ErrSegmentNotFound) {
		segNum, parseErr := playback.ParseSegmentNumber(segmentName)
		if parseErr == nil {
			now := time.Now()
			decision := transcodeSession.SegmentRecoveryDecision(segNum, now)
			lastProducedAgeMS := int64(-1)
			if !decision.Progress.LastProducedAt.IsZero() {
				lastProducedAgeMS = now.Sub(decision.Progress.LastProducedAt).Milliseconds()
			}
			slog.Info("transcode segment missing",
				"segment", segmentName,
				"requested_segment", segNum,
				"produced_head", decision.Progress.ProducedHead,
				"last_requested_segment", decision.Progress.LastRequestedSegment,
				"start_segment_number", decision.Progress.StartSegmentNumber,
				"last_produced_age_ms", lastProducedAgeMS,
				"wait_timeout_ms", decision.WaitTimeout.Milliseconds(),
				"restart_on_timeout", decision.RestartOnTimeout,
				"reason", decision.Reason,
				"session", sessionID,
				"playback_session_id", sessionID,
			)
			if decision.Wait {
				slog.Info("transcode segment wait",
					"segment", segmentName,
					"requested_segment", segNum,
					"produced_head", decision.Progress.ProducedHead,
					"last_requested_segment", decision.Progress.LastRequestedSegment,
					"start_segment_number", decision.Progress.StartSegmentNumber,
					"last_produced_age_ms", lastProducedAgeMS,
					"wait_timeout_ms", decision.WaitTimeout.Milliseconds(),
					"restart_on_timeout", decision.RestartOnTimeout,
					"reason", decision.Reason,
					"session", sessionID,
					"playback_session_id", sessionID,
				)
				segmentPath, err = transcodeSession.WaitForSegment(segmentName, decision.WaitTimeout)
				if err != nil && errors.Is(err, playback.ErrSegmentNotFound) {
					slog.Info("transcode segment wait timeout",
						"segment", segmentName,
						"requested_segment", segNum,
						"produced_head", decision.Progress.ProducedHead,
						"last_requested_segment", decision.Progress.LastRequestedSegment,
						"start_segment_number", decision.Progress.StartSegmentNumber,
						"last_produced_age_ms", lastProducedAgeMS,
						"wait_timeout_ms", decision.WaitTimeout.Milliseconds(),
						"restart_on_timeout", decision.RestartOnTimeout,
						"reason", decision.Reason,
						"session", sessionID,
						"playback_session_id", sessionID,
					)
				}
			}

			// If the segment is still missing (timed out, or outside the
			// active encode range), either restart at the exact manifest-derived
			// timeline position or return 404 for copy-mode segments outside the
			// current manifest window.
			if err != nil && errors.Is(err, playback.ErrSegmentNotFound) && decision.RestartOnTimeout {
				seekSeconds, ok, seekErr := transcodeSession.RestartSeekTarget(segNum)
				if seekErr != nil && !errors.Is(seekErr, playback.ErrManifestNotReady) {
					slog.Error("resolve transcode seek target", "error", seekErr, "segment", segmentName, "session", sessionID, "playback_session_id", sessionID)
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
					slog.Info("transcode seek restart",
						"segment", segmentName,
						"requested_segment", segNum,
						"produced_head", decision.Progress.ProducedHead,
						"last_requested_segment", decision.Progress.LastRequestedSegment,
						"start_segment_number", decision.Progress.StartSegmentNumber,
						"last_produced_age_ms", lastProducedAgeMS,
						"wait_timeout_ms", decision.WaitTimeout.Milliseconds(),
						"restart_on_timeout", decision.RestartOnTimeout,
						"reason", decision.Reason,
						"seek_seconds", seekSeconds,
						"session", sessionID,
						"playback_session_id", sessionID,
					)
					if restartErr := h.tm.RestartSessionLocked(
						context.WithoutCancel(r.Context()),
						sessionID,
						transcodeSession,
						seekSeconds,
						segNum,
					); restartErr == nil {
						// Throttler + exit monitor re-arm via the session's
						// restart hook.
						segmentPath, err = transcodeSession.WaitForSegment(segmentName, 30*time.Second)
						if err == nil && strings.EqualFold(transcodeSession.Opts().TargetCodecVideo, "copy") {
							// Copy-mode seeks can resume as soon as the target segment
							// exists, but that sometimes leaves the player one segment
							// away from stalling while FFmpeg catches up. Briefly wait
							// for a single lookahead fragment when available so the
							// first resumed playback window is less brittle.
							nextSegmentName := fmt.Sprintf("seg_%05d.m4s", segNum+1)
							_, _ = transcodeSession.WaitForSegment(nextSegmentName, 1200*time.Millisecond)
						}
					}
				}
			}
		} else if transcodeSession.IsRunning() {
			// Non-numbered segment (e.g., init.mp4 for fMP4 HLS).
			// Wait briefly — the init segment is written almost immediately.
			segmentPath, err = transcodeSession.WaitForSegment(segmentName, 10*time.Second)
		}
	}
	if err != nil {
		if errors.Is(err, playback.ErrSegmentNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Segment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load segment")
		return
	}

	// Report segment download for throttle tracking.
	if segNum, parseErr := playback.ParseSegmentNumber(segmentName); parseErr == nil {
		transcodeSession.ReportSegmentDownloaded(segNum)
	}

	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	http.ServeFile(w, r, segmentPath)
}

// buildProxyManifestURL signs a stream token carrying the session's full
// reconstruction recipe and builds the manifest URL. proxyNode is the planner's
// pick; when nil the URL falls back to the API-local path, where the token rides
// the ?st= query parameter so the integrated server can reconstruct from it.
func (h *PlaybackHandler) buildProxyManifestURL(card playback.RecipeCard, proxyNode *nodepool.Node) string {
	token := h.signSessionToken(card)
	localURL := fmt.Sprintf("/playback/transcode/%s/master.m3u8", card.SessionID)
	if proxyNode == nil {
		return appendStreamToken(localURL, token)
	}
	if token == "" {
		return localURL
	}
	return proxyNode.URL + "/stream/transcode/" + token + "/master.m3u8"
}

// proxyToTranscodeNode forwards a request to the remote transcode node.
func (h *PlaybackHandler) proxyToTranscodeNode(w http.ResponseWriter, r *http.Request, transcodeNodeURL, path string) {
	sessionID := chi.URLParam(r, "session_id")
	targetURL := transcodeNodeURL + path
	// Capture the signed stream token ("st") before stripping it from the URL.
	// We forward it out-of-band as a header so the node can reconstruct after a
	// self-restart, while keeping it out of the forwarded/logged URL.
	stToken := r.URL.Query().Get("st")
	// Strip the signed stream token ("st") before forwarding/logging: it is a
	// 24h bearer reconstruction descriptor exposing media path + recipe claims.
	// Other query params are preserved.
	query := r.URL.Query()
	query.Del("st")
	if encoded := query.Encode(); encoded != "" {
		targetURL += "?" + encoded
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, targetURL, nil)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", "Bearer "+h.JWTSecret)
	// Best-effort forward of the stream token as a header so the node's
	// reconstruct path (X-Silo-Stream-Token) can rebuild after a self-restart.
	// Verify at the API boundary and confirm it belongs to this session; an
	// invalid or missing token never blocks the live proxy. validToken is kept so
	// the same verified token can be re-injected into the node's manifest segment
	// URIs below.
	var validToken string
	if stToken != "" && h.JWTSecret != "" {
		claims, verifyErr := streamtoken.Verify(stToken, h.JWTSecret)
		if verifyErr == nil && claims.SessionID == sessionID {
			req.Header.Set("X-Silo-Stream-Token", stToken)
			validToken = stToken
		} else if verifyErr != nil {
			slog.Warn("stream token not forwarded to transcode node", "error", verifyErr, "playback_session_id", sessionID)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("proxy to transcode node", "error", err, "url", targetURL, "playback_session_id", sessionID)
		http.Error(w, "transcode node unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// The node strips "st" from the request query (kept out of node URLs/logs),
	// so the segment/init URIs in the manifest it builds carry no token. Without
	// it, a segment fetched after a node or API restart cannot reconstruct the
	// session and 404s. Re-inject the client-facing token into every URI at this
	// boundary so the client's later segment requests carry "st" again. Only the
	// manifest body is rewritten; segments stream through untouched.
	if validToken != "" && resp.StatusCode == http.StatusOK && strings.HasSuffix(path, ".m3u8") {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			slog.Error("read transcode node manifest", "error", readErr, "url", targetURL, "playback_session_id", sessionID)
			http.Error(w, "transcode node unavailable", http.StatusBadGateway)
			return
		}
		rewritten := playback.AppendManifestQueryParam(body, streamTokenParam, validToken)
		for k, vv := range resp.Header {
			if http.CanonicalHeaderKey(k) == "Content-Length" {
				continue
			}
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(rewritten)))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(rewritten)
		return
	}

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// maybeStartThrottler reads throttle settings and starts the throttler if enabled.
func (h *PlaybackHandler) maybeStartThrottler(ctx context.Context, session *playback.TranscodeSession) {
	if h.SettingsRepo == nil {
		return
	}
	enableThrottle, _ := h.SettingsRepo.Get(ctx, "enable_transcode_throttle")
	if enableThrottle != "true" {
		return
	}
	thresholdStr, _ := h.SettingsRepo.Get(ctx, "transcode_throttle_seconds")
	threshold := 300 // default
	if v, err := strconv.Atoi(thresholdStr); err == nil && v > 0 {
		threshold = v
	}
	session.StartThrottler(threshold)
}

// findAlternateFile finds a non-4K file version for the same content.
// Prefers SDR over HDR, then highest resolution, then highest bitrate.
func (h *PlaybackHandler) findAlternateFile(ctx context.Context, source *models.MediaFile) (*models.MediaFile, error) {
	if h.FileVersionFetcher == nil {
		return nil, fmt.Errorf("file version fetcher not configured")
	}

	var files []*models.MediaFile
	var err error
	if source.EpisodeID != "" {
		files, err = h.FileVersionFetcher.GetByEpisodeID(ctx, source.EpisodeID)
	} else {
		files, err = h.FileVersionFetcher.GetByContentID(ctx, source.ContentID)
	}
	if err != nil {
		return nil, err
	}

	// Filter to non-4K candidates.
	candidates := make([]*models.MediaFile, 0, len(files))
	for _, f := range files {
		if f.ID == source.ID || f.Resolution == "2160p" {
			continue
		}
		if source.EditionKey != "" && f.EditionKey != source.EditionKey {
			continue
		}
		if source.EditionKey == "" && f.EditionKey != "" {
			continue
		}
		if source.PresentationGroupKey != "" && f.PresentationGroupKey != "" && f.PresentationGroupKey != source.PresentationGroupKey {
			continue
		}
		if source.PresentationKind != "" && f.PresentationKind != "" && f.PresentationKind != source.PresentationKind {
			continue
		}
		candidates = append(candidates, f)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// Sort: SDR before HDR, then highest resolution, then highest bitrate.
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		// Prefer SDR over HDR (SDR = !HDR, so !HDR < HDR means SDR first).
		if a.HDR != b.HDR {
			return !a.HDR
		}
		aRes := resolutionRank(a.Resolution)
		bRes := resolutionRank(b.Resolution)
		if aRes != bRes {
			return aRes > bRes
		}
		return a.Bitrate > b.Bitrate
	})

	return candidates[0], nil
}

// resolutionRank returns a numeric rank for resolution sorting.
func resolutionRank(res string) int {
	switch res {
	case "2160p":
		return 4
	case "1080p":
		return 3
	case "720p":
		return 2
	case "480p":
		return 1
	case "328p":
		return 0
	default:
		return 0
	}
}
