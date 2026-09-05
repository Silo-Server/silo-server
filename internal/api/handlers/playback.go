package handlers

import (
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
	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/transcodeproxy"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// SessionManagerInterface defines the operations the PlaybackHandler needs
// on the session manager.
type SessionManagerInterface interface {
	StartSession(userID int, profileID string, fileID int, method playback.PlayMethod, transcodeAudio bool) (*playback.Session, error)
	StartSessionWithFiles(userID int, profileID string, effectiveFileID int, requestedFileID int, method playback.PlayMethod, transcodeAudio bool) (*playback.Session, error)
	// StartSessionWithFilesContext is required rather than probed for at run
	// time: the context is how the reporting client's identity reaches the new
	// session, so an implementation without it would start sessions that
	// silently carry no client name, version, build or channel.
	StartSessionWithFilesContext(ctx context.Context, userID int, profileID string, effectiveFileID int, requestedFileID int, method playback.PlayMethod, transcodeAudio bool) (*playback.Session, error)
	UpdateProgress(sessionID string, position float64, isPaused bool) error
	UpdateAudioTrack(sessionID string, audioTrackIndex int, method playback.PlayMethod) error
	UpdateStreamState(sessionID string, state playback.SessionStreamState) error
	TouchActivity(sessionID string) error
	BeginTransport(sessionID string) error
	EndTransport(sessionID string) error
	// WatchTransportStop is required rather than probed for at run time: it is
	// the only thing that can interrupt a single-response transport, so an
	// implementation without it would serve progressive remuxes that no session
	// stop can withdraw.
	WatchTransportStop(sessionID string) (<-chan struct{}, func())
	SetRemoteTransport(sessionID string, remote bool) error
	SetEffectiveMediaFileID(sessionID string, fileID int) error
	SetTranscodeNodeURL(sessionID, url string) error
	SetTranscodeRoute(sessionID string, route playback.TranscodeRoute) error
	ApplyReplacement(sessionID string, replacement playback.SessionReplacement) (playback.SessionReplacementRollback, error)
	ApplyReplacementIfRoute(sessionID string, expected playback.TranscodeRoute, replacement playback.SessionReplacement) (playback.SessionReplacementRollback, bool, error)
	RollbackReplacement(sessionID string, rollback playback.SessionReplacementRollback) error
	SetWebSocket(sessionID string, connected bool) error
	SetRealtimeConnection(sessionID string, connected bool) error
	SetProgressPersistenceDisabled(sessionID string, disabled bool) error
	StopSession(sessionID string) error
	GetSession(sessionID string) (*playback.Session, error)
}

type transcodePermissionChecker interface {
	CheckTranscodingAllowed(ctx context.Context, userID int, requiresVideoTranscode bool) error
}

func (h *PlaybackHandler) ensureUserTranscodingAllowed(w http.ResponseWriter, r *http.Request, userID int, requiresVideoTranscode bool) bool {
	checker, ok := h.sessionMgr.(transcodePermissionChecker)
	if !ok {
		return true
	}
	if err := checker.CheckTranscodingAllowed(r.Context(), userID, requiresVideoTranscode); err != nil {
		if errors.Is(err, playback.ErrTranscodingDisabled) {
			writeError(w, http.StatusForbidden, "transcoding_disabled", "Transcoding is disabled for your user")
			return false
		}
		if errors.Is(err, playback.ErrAudioTranscodingDisabled) {
			writeError(w, http.StatusForbidden, "audio_transcoding_disabled", "Audio transcoding is disabled for your user")
			return false
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to verify transcoding access")
		return false
	}
	return true
}

type PlaybackItemAccessChecker interface {
	EnsureAccessible(ctx context.Context, contentID string, filter catalog.AccessFilter) error
}

type PlaybackEpisodeLookup interface {
	GetByID(ctx context.Context, contentID string) (*models.Episode, error)
}

// PlaybackExtraLookup resolves local extras (media_extras) so their files
// authorize through the parent item, like episodes authorize through their
// series.
type PlaybackExtraLookup interface {
	GetByID(ctx context.Context, contentID string) (*models.MediaExtra, error)
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

// PlaybackProbeEnsurer repairs probe metadata and stamps the H.264 copy-safety
// verdict when it is already known.
//
// It deliberately exposes no blocking variant: a play must never wait on the
// multi-second bitstream scan, so an unknown verdict is planned optimistically
// and resolved behind the play (see PlaybackCopySafetyRacer).
//
// EnsureProbeOnly is declared because this interface is also the type the
// router carries the shared ensurer in when handing it to the catalog and
// chapter-thumbnail services, which repair probe metadata and nothing else.
type PlaybackProbeEnsurer interface {
	EnsureProbeOnly(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error)
	EnsureCopySafetyCached(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error)
}

// PlaybackCopySafetyRacer resolves an unknown H.264 copy-safety verdict out of
// band, after a plan that stream-copies video has already been issued.
// *playback.CopySafetyRace implements it.
type PlaybackCopySafetyRacer interface {
	RaceScanForPlan(fileID int, plan *playback.PlanV3)
	// RaceScan re-engages the race for a file whose verdict is still open. The
	// serve paths use it when they revive a stream-copy transport: the replica
	// that planned it may be gone, and only a race running *here* can withdraw
	// the route from the session this replica just rebuilt.
	RaceScan(fileID int)
	// VideoCopyUnsafeKnown answers, without ffmpeg and without waiting, whether
	// this replica can already condemn a video stream-copy of the file —
	// including from a verdict whose write to the row failed.
	VideoCopyUnsafeKnown(ctx context.Context, file *models.MediaFile) bool
}

type PlaybackChapterThumbnailQueuer interface {
	QueuePriorityFileAtPosition(ctx context.Context, fileID int, targetSeconds float64)
}

// PlaybackOriginalLanguageLookup fetches the original language for a content item.
type PlaybackOriginalLanguageLookup interface {
	GetOriginalLanguage(ctx context.Context, contentID string) (string, error)
}

type copySeekAnchorResolver func(
	ctx context.Context,
	ffmpegPath string,
	inputPath string,
	requestedSeekSeconds float64,
	segmentDuration int,
) (float64, int, error)

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
	StreamTelemetry         *streamtelemetry.Registry // local observation-only telemetry
	// ProxyGrantStore hands a proxy the recipe it serves a header-authenticated
	// session from. Optional: without it (or without Redis behind it) an attempt
	// that negotiated authorized_media_origins_v1 simply stays on the API origin.
	ProxyGrantStore recipeCardStoreV3
	// NodeRecipeStore hands a transcode node the recipe it rebuilds a
	// header-authenticated remote transcode from after its own restart, keyed by
	// the transport id the node serves it under. It is also the active authority
	// for transcode-executed progressive remuxes, whose signed tokens otherwise
	// outlive a node's in-memory stop fence. Without a usable store those remuxes
	// use another legal route; tokenless HLS sessions replan instead of recovering.
	NodeRecipeStore    recipeCardStoreV3
	ItemAccess         PlaybackItemAccessChecker // optional; enables file authorization checks
	EpisodeLookup      PlaybackEpisodeLookup     // optional; resolves episode files to their series
	ExtraLookup        PlaybackExtraLookup       // optional; resolves extras files to their parent item
	OriginalLangLookup PlaybackOriginalLanguageLookup
	SettingsRepo       PlaybackSettingsReader     // optional; reads server settings (e.g., allow_4k_transcode)
	FileVersionFetcher PlaybackFileVersionFetcher // optional; queries sibling file versions for 4K guard
	ProbeEnsurer       PlaybackProbeEnsurer       // optional; repairs missing probe metadata on demand
	// CopySafetyRacer resolves an unknown H.264 copy-safety verdict behind an
	// already-issued stream-copy plan. Optional: without it an unknown verdict
	// simply stays unknown and the copy route is never withdrawn.
	CopySafetyRacer        PlaybackCopySafetyRacer
	ChapterThumbnailQueuer PlaybackChapterThumbnailQueuer
	IntroAnalyzer          IntroEpisodeAnalyzer
	IntroRepository        PlaybackIntroEligibilityChecker
	MarkerRegistry         *markers.Registry
	MarkerResolver         markers.ExternalIDResolver
	MarkerUpserter         PlaybackMarkerUpserter
	MarkerUpdateNotifier   PlaybackMarkerUpdateNotifier
	MarkerLazyContext      context.Context
	MarkerLazyInFlight     sync.Map
	SubtitleRepo           subtitles.Repository // optional; enables downloaded subtitles in playback
	RealtimeHub            *playback.RealtimeHub
	CommandTracker         *playback.CommandTracker
	CommandDispatcher      *playback.CommandDispatcher
	// PlaybackConfig returns the current playback config (ffmpeg path,
	// hwaccel, transcode dir). Wired to the live config in integrated mode
	// so admin changes apply to newly started transcodes. Read it through
	// playbackConfig(), which falls back to defaults when unset.
	PlaybackConfig func() config.PlaybackConfig
	FFmpegLogSink  playback.FFmpegLogSink
	copySeekAnchor copySeekAnchorResolver
	// beforeIdentityLifecycleLockV3 is a test seam for proving that identity
	// route authority remains unpublished until the shared lifecycle boundary.
	beforeIdentityLifecycleLockV3 func()
	realtimeCommandMu             sync.Mutex
	realtimeCommands              map[string]playbackCommandRecord
	// tm owns the transcode-session lifecycle (live map, recipe cards, and
	// restart reconstruct) shared with the jellycompat handler. The handler
	// delegates all transcode-session and recipe operations to it.
	tm *playback.TranscodeManager
	// PlanStoreV3 owns the short-lived protocol-v3 control-plane state. Router
	// wiring replaces the in-memory default with PostgreSQL in integrated mode.
	PlanStoreV3          playback.PlanStoreV3
	v3RegistryMu         sync.Mutex
	v3Registry           *playback.TransformationRegistryV3
	v3RegistryProbe      func(context.Context, string, tonemap.Capabilities) (*playback.TransformationRegistryV3, error)
	v3ToneMapProbe       func(context.Context, string, string, string) (tonemap.Capabilities, error)
	v3NodeCapabilitiesMu sync.Mutex
	v3NodeCapabilities   map[string]v3NodeCapabilityCache
	// v3NodeProbeBudgets holds what each node last said a capability read of it
	// costs, guarded by v3NodeCapabilitiesMu. It is kept apart from the
	// inventory above because the two are invalidated for different reasons: an
	// acceleration change makes the inventory wrong and the next read slow,
	// while how long that node takes to answer is unchanged. See
	// remoteToneMapProbeTimeoutV3.
	v3NodeProbeBudgets map[string]time.Duration
	// v3NodeCapabilityInvalidations counts invalidations per node URL, guarded
	// by v3NodeCapabilitiesMu. A probe that started before the count moved
	// describes hardware the health sweep has already reported as changed, so
	// its result must not be installed. See RefreshNodeCapabilitiesV3.
	v3NodeCapabilityInvalidations map[string]uint64
	// v3NodeCapabilityRefresh holds the nodes with a background refresh in
	// flight, one at a time each. Guarded by v3NodeCapabilitiesMu, the same
	// lock as the invalidation counter, so a refresh cannot release its slot in
	// between an invalidation and that invalidation's claim on it.
	v3NodeCapabilityRefresh map[string]struct{}
	v3EventOnce             sync.Once
	v3EventQueue            chan playback.RouteEventRecordV3
	v3StartEffectsOnce      sync.Once
	v3StartEffectsQueue     chan playbackStartSideEffectsV3
	v3StartEffectsMu        sync.Mutex
	v3StartEffectsPending   map[string]*playbackStartSideEffectsStateV3
	v3AudioPreferenceMu     sync.Mutex
	v3ReplanMu              sync.Mutex
	v3ReplanLocks           map[string]*v3ReplanLock
	v3ReplanSlotsOnce       sync.Once
	v3ReplanSlots           chan struct{}
	v3EventRateMu           sync.Mutex
	v3EventRates            map[string]v3EventRate
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
		PlanStoreV3:      playback.NewMemoryPlanStoreV3(),
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
			TranscodeDir:            c.TranscodeDir,
			FFmpegPath:              c.FFmpegPath,
			HWAccel:                 c.HWAccel,
			HWDevice:                c.HWDevice,
			SegmentRetentionSeconds: c.SegmentRetentionSeconds,
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
			slog.ErrorContext(ctx, "failed to stop playback after local transcode exit", "component", "api", "session", sessionID, "error", err, "playback_session_id", sessionID)
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
		Routing:          config.DefaultPlaybackRoutingPolicy(),
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

// progressRequest represents the JSON body for POST /playback/{session_id}/progress.
type progressRequest struct {
	Position float64 `json:"position"`
	IsPaused bool    `json:"is_paused"`
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

// ensurePlaybackProbe repairs probe metadata and stamps the H.264 copy-safety
// verdict when it is already known. It never runs the bitstream scan: an
// unknown verdict plans optimistically (the planner reads nil MultiplePPS as
// "copy is allowed") and is resolved asynchronously once the plan is issued, so
// starting playback never waits on a multi-second read of the source.
func (h *PlaybackHandler) ensurePlaybackProbe(ctx context.Context, file *models.MediaFile) *models.MediaFile {
	if h == nil || h.ProbeEnsurer == nil || file == nil {
		return file
	}
	repaired, err := h.ProbeEnsurer.EnsureCopySafetyCached(ctx, file)
	if err != nil {
		slog.WarnContext(ctx, "playback probe repair failed", "component", "api", "file_id", file.ID, "path", file.FilePath, "error", err)
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
// (reconstruct effectively disabled, e.g. in tests), or when the attempt
// negotiated header-authenticated media.
//
// requireMediaAuth is the attempt's negotiated media-auth mode, threaded from
// the session/recipe state the caller holds. It is refused here, at the mint,
// rather than only at the call sites that build URLs: a token that is never
// signed cannot leak into a client-visible URL by way of a builder that forgot
// to ask. Call sites keep their own checks as defense in depth.
func (h *PlaybackHandler) signSessionToken(card playback.RecipeCard, requireMediaAuth bool) string {
	if requireMediaAuth {
		return ""
	}
	return h.signStreamClaims(card.ToClaims())
}

// signStreamClaims mints a stream token from claims that are already assembled.
// Callers serving a session from another node use it to add the claims a
// RecipeCard does not model (the file's Dolby Vision profile, audio-only flag),
// which a remote executor cannot look up for itself.
func (h *PlaybackHandler) signStreamClaims(claims streamtoken.Claims) string {
	if h.JWTSecret == "" {
		return ""
	}
	token, err := streamtoken.Sign(claims, h.JWTSecret, playback.MaxTokenTTL)
	if err != nil {
		slog.Warn("sign stream token failed", "error", err, "session", claims.SessionID, "playback_session_id", claims.SessionID)
		return ""
	}
	return token
}

// loadTranscodeServeSession resolves the playback Session for the transcode
// manifest/segment serve routes while keeping stream-token verification off the
// hot path. A V3 session that negotiated header-authenticated media requires a
// live authenticated owner on every request; a legacy session retains its UUID
// bearer behavior. The overwhelmingly common case is a live in-memory session,
// which needs no token at all, so the cheap GetSession lookup runs first and the
// (HMAC + JSON) token decode is performed only when the session cannot serve by
// itself: on a not-found miss where a reconstruct is required, and on a live
// session with no runtime, where any valid recipe card (plain or tone-mapped)
// joins the manager's atomic playback+runtime reconstruction operation. On the
// miss it delegates to the shared LoadOrReconstructSession front door so
// reconstruct/ownership semantics stay identical. The returned card (nil on the
// live-session path when no token is presented) is the decoded recipe the
// caller's own reconstruct branch consumes.
func (h *PlaybackHandler) loadTranscodeServeSession(r *http.Request, sessionID string, requestedSegment int) (*playback.Session, playback.SessionLoadStatus, *playback.RecipeCard, *streamtoken.Claims, error) {
	requestUserID := apimw.GetUserID(r.Context())
	session, err := h.sessionMgr.GetSession(sessionID)
	if err == nil {
		// Defense in depth: LoadOrReconstructSession enforces the same rule for
		// every serve handler, but this fast path never reaches it.
		if session.RequireMediaAuthorization && requestUserID == 0 {
			return nil, playback.SessionUnauthorized, nil, nil, nil
		}
		// Live session: secure transports require a user above; legacy bearer
		// routes allow zero. Either way, a present but mismatched identity is
		// forbidden. No token verification on this hot path.
		if requestUserID != 0 && session.UserID != requestUserID {
			return nil, playback.SessionForbidden, nil, nil, nil
		}
		// A non-API or incomplete route is returned to the handler for the
		// committed-egress guard below. Do not rebuild a local runtime first:
		// even discarded output would cross the route's execution boundary.
		if nativeAPIEgressStatusV3(session.RoutingWorkload, session.RoutingExecution, session.RoutingEgress) != 0 {
			return session, playback.SessionLoaded, nil, nil, nil
		}
		if session.TranscodeNodeURL == "" && h.tm.GetTranscodeSession(sessionID) == nil {
			// A live session whose runtime died can recover from the client's
			// recipe token. Tone-mapped cards may back a provisional capability
			// reconstruction; plain cards just respawn the encode. Either way the
			// atomic playback+runtime operation is the same front door.
			card, claims := verifiedStreamCardFromToken(r.URL.Query().Get(streamTokenParam), sessionID, h.JWTSecret)
			if card != nil && nativeAPIEgressStatusV3(card.RoutingWorkload, card.RoutingExecution, card.RoutingEgress) != 0 {
				// The live session may have been replanned since this token was
				// issued. Its API assignment is authoritative; a stale proxy card
				// cannot revive the old runtime on this origin.
				return session, playback.SessionLoaded, nil, nil, nil
			}
			if card != nil {
				if videoCopyReconstructRefused(r.Context(), h.fileResolver, h.CopySafetyRacer, card) {
					return nil, playback.SessionMissing, nil, nil, nil
				}
				session, _, status, reconstructErr := h.tm.LoadOrReconstructTranscodeWithError(r.Context(), h.sessionMgr.GetSession, sessionID, requestUserID, requestedSegment, card)
				return session, status, card, claims, reconstructErr
			}
		}
		return session, playback.SessionLoaded, nil, nil, nil
	}
	if !errors.Is(err, playback.ErrSessionNotFound) {
		return nil, playback.SessionLoadFailed, nil, nil, nil
	}
	// Genuine miss (e.g. after a restart): now — and only now — pay for the token
	// decode so the recipe is available for reconstruction.
	card, claims := verifiedStreamCardFromToken(r.URL.Query().Get(streamTokenParam), sessionID, h.JWTSecret)
	if card != nil {
		if routeStatus := nativeAPIEgressStatusV3(card.RoutingWorkload, card.RoutingExecution, card.RoutingEgress); routeStatus != 0 {
			return nil, playback.SessionUnavailable, card, claims, &nativeRouteBindingErrorV3{status: routeStatus}
		}
	}
	// The copy-safety verdict gates the revival before it happens, not after.
	// Reconstruction registers the playback session against the user's stream
	// caps, so a refusal that ran later would leave a session nobody serves
	// holding an admission slot the client's fresh attempt needs — and a
	// remote-node recipe never reaches the local transport reconstruct at all
	// (the serve handlers proxy to the node instead), so a gate down there would
	// miss it entirely. A revival the verdict does not condemn re-engages the
	// race here, so the session about to be rebuilt is covered by a race this
	// replica owns. See playback_copy_safety.go.
	if videoCopyReconstructRefused(r.Context(), h.fileResolver, h.CopySafetyRacer, card) {
		return nil, playback.SessionMissing, nil, nil, nil
	}
	if card != nil && card.ToneMapMode != "" {
		session, _, status, reconstructErr := h.tm.LoadOrReconstructTranscodeWithError(r.Context(), h.sessionMgr.GetSession, sessionID, requestUserID, requestedSegment, card)
		return session, status, card, claims, reconstructErr
	}
	session, status := h.tm.LoadOrReconstructSession(r.Context(), h.sessionMgr.GetSession, sessionID, requestUserID, card)
	return session, status, card, claims, nil
}

// streamCardFromToken verifies a stream token and decodes its reconstruction
// recipe, returning nil when the token is absent, unparseable/expired, or bound
// to a different session id. Shared by the native serve handlers (PlaybackHandler
// and StreamHandler).
func verifiedStreamCardFromToken(tokenStr, sessionID, secret string) (*playback.RecipeCard, *streamtoken.Claims) {
	if tokenStr == "" || secret == "" {
		return nil, nil
	}
	claims, err := streamtoken.Verify(tokenStr, secret)
	if err != nil || claims.SessionID != sessionID {
		return nil, nil
	}
	card := playback.RecipeCardFromClaims(claims)
	return &card, claims
}

// requireNativeAPIEgressV3 enforces the origin frozen into a v3 playback
// assignment. Empty assignments predate node routing and remain API-compatible;
// a partially populated assignment is a failed commit and must not fail open.
func requireNativeAPIEgressV3(w http.ResponseWriter, workload, execution, egress string) bool {
	status := nativeAPIEgressStatusV3(workload, execution, egress)
	if status == 0 {
		return true
	}
	writeNativeRouteStatusV3(w, status)
	return false
}

func writeNativeRouteStatusV3(w http.ResponseWriter, status int) {
	switch status {
	case http.StatusConflict:
		writeError(w, status, "playback_route_unbound", "Request a new playback plan before serving media")
	default:
		writeError(w, status, string(noderouting.OutcomePolicyUnsatisfied), "The media request does not match the route bound by the playback plan")
	}
}

func nativeAPIEgressStatusV3(workload, execution, egress string) int {
	workload = strings.TrimSpace(workload)
	execution = strings.TrimSpace(execution)
	egress = strings.TrimSpace(egress)
	if workload == "" && execution == "" && egress == "" {
		return 0
	}
	if workload == "" || execution == "" || egress == "" {
		return http.StatusConflict
	}
	if egress != string(noderouting.EgressAPI) {
		return http.StatusServiceUnavailable
	}
	return 0
}

func requireNativeSessionAPIEgressV3(w http.ResponseWriter, session *playback.Session) bool {
	if session == nil {
		return false
	}
	return requireNativeAPIEgressV3(w, session.RoutingWorkload, session.RoutingExecution, session.RoutingEgress)
}

func requireNativeRecipeAPIEgressV3(w http.ResponseWriter, card *playback.RecipeCard) bool {
	if card == nil {
		return true
	}
	return requireNativeAPIEgressV3(w, card.RoutingWorkload, card.RoutingExecution, card.RoutingEgress)
}

type nativeRouteBindingErrorV3 struct {
	status int
}

func (e *nativeRouteBindingErrorV3) Error() string {
	return "media request does not match its bound playback route"
}

func writeNativeRouteBindingErrorV3(w http.ResponseWriter, err error) bool {
	var routeErr *nativeRouteBindingErrorV3
	if !errors.As(err, &routeErr) {
		return false
	}
	writeNativeRouteStatusV3(w, routeErr.status)
	return true
}

func attachPlaybackSession(ctx context.Context, session *playback.Session, claims *streamtoken.Claims) {
	if session == nil {
		return
	}
	startedAt := session.StartedAt
	startedSource := streamtelemetry.StartedAtSourceSession
	tokenIssuedAt := time.Time{}
	tokenSource := streamtelemetry.TokenIssuedAtSourceNone
	if claims != nil {
		if resolved, source := claims.StartedAt(); !resolved.IsZero() {
			switch source {
			case streamtoken.StartedAtSourceClaim:
				startedAt = resolved
				startedSource = streamtelemetry.StartedAtSourceClaim
			case streamtoken.StartedAtSourceIssuedAt:
				if startedAt.IsZero() {
					startedAt = resolved
					startedSource = streamtelemetry.StartedAtSourceIssuedAt
				}
			}
		}
		if claims.IssuedAt != nil {
			tokenIssuedAt = claims.IssuedAt.Time
			tokenSource = streamtelemetry.TokenIssuedAtSourceVerified
		}
	}
	streamtelemetry.Attach(ctx, streamtelemetry.Attachment{Subject: streamtelemetry.UserSubject(session.UserID),
		ProfileID: session.ProfileID, SessionID: session.ID, MediaFileID: session.MediaFileID,
		PlayMethod: string(session.PlayMethod), StartedAt: startedAt, StartedAtSource: startedSource,
		TokenIssuedAt: tokenIssuedAt, TokenIssuedAtSource: tokenSource})
}

func attachTransfer(ctx context.Context, userID int, profileID string, mediaFileID int) {
	streamtelemetry.Attach(ctx, streamtelemetry.Attachment{Subject: streamtelemetry.UserSubject(userID),
		ProfileID: profileID, MediaFileID: mediaFileID, StartedAtSource: streamtelemetry.StartedAtSourceFirstSeen,
		TokenIssuedAtSource: streamtelemetry.TokenIssuedAtSourceNone})
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
// client re-supplies its byte position). Transcode sessions are told which URL
// to play by their v3 plan; the URL here is an informational placeholder that
// the plan's delivery URL supersedes.
//
// A session that requires media authorization gets the bare relative URL: it
// authenticates every media request with the caller's own access token, so no
// client-visible URL may carry a playback credential. Losing the token also
// means losing transparent reconstruction after a restart, which is the
// documented trade of that mode.
func (h *PlaybackHandler) playbackStreamURL(s *playback.Session) string {
	if s == nil {
		return ""
	}
	if s.PlayMethod == playback.PlayTranscode {
		return fmt.Sprintf("/playback/transcode/%s/master.m3u8", s.ID)
	}
	streamURL := fmt.Sprintf("/stream/%s", s.ID)
	if s.RequireMediaAuthorization {
		return streamURL
	}
	card := identityRecipeCard(s)
	return appendStreamToken(streamURL, h.signSessionToken(card, s.RequireMediaAuthorization))
}

// identityRecipeCard builds the identity-only recipe for a direct-play or remux
// session: reconstruction needs only ownership plus the audio selection, since
// the bytes are served by HTTP Range / a re-spawned remux pipe at the
// client-supplied position.
func identityRecipeCard(s *playback.Session) playback.RecipeCard {
	var card playback.RecipeCard
	switch s.PlayMethod {
	case playback.PlayRemux:
		card = playback.NewRemuxRecipeCard(s.ID, s.UserID, s.ProfileID, s.MediaFileID, s.TranscodeAudio, s.AudioTrackIndex, s.RemuxDVMode)
		card.TargetCodecAudio = s.TargetAudioCodec
		card.TargetAudioChannels = s.TargetAudioChannels
		card.TargetAudioBitrateKbps = s.TargetAudioBitrateKbps
		if s.TranscodeAudio && playback.IsAudioToAACStereoDownmixV3(s.SourceAudioChannels, s.TargetAudioCodec, s.TargetAudioChannels) {
			card.SourceAudioChannels = s.SourceAudioChannels
		}
	default:
		card = playback.NewDirectRecipeCard(s.ID, s.UserID, s.ProfileID, s.MediaFileID)
	}
	card.OriginalStartedAt = s.StartedAt
	card.RoutingWorkload = s.RoutingWorkload
	card.RoutingExecution = s.RoutingExecution
	card.RoutingExecutionNodeID = s.RoutingExecutionNodeID
	card.RoutingEgress = s.RoutingEgress
	card.RoutingEgressNodeID = s.RoutingEgressNodeID
	card.TranscodeNodeURL = s.TranscodeNodeURL
	card.TranscodeTransportID = s.TranscodeTransportID
	return card
}

func fileBitrateKbps(file *models.MediaFile) int {
	if file == nil || file.Bitrate <= 0 {
		return 0
	}
	return file.Bitrate
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

func remoteTransportID(session *playback.Session) string {
	if session != nil && session.TranscodeTransportID != "" {
		return session.TranscodeTransportID
	}
	if session == nil {
		return ""
	}
	return session.ID
}

func (h *PlaybackHandler) closeTranscodeForSession(session *playback.Session) {
	if session == nil {
		return
	}
	// Local sessions remain keyed by the public playback session. Remote v3
	// processes use a plan-scoped transport identity so a prepared successor can
	// coexist with its predecessor until commit.
	h.tm.CloseTranscodeSession(session.ID, "")
	if session.TranscodeNodeURL != "" {
		h.tm.StopRemoteTranscode(remoteTransportID(session), session.TranscodeNodeURL)
	}
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

func normalizeAudioTrackIndex(file *models.MediaFile, audioTrackIndex int) int {
	if file == nil || len(file.AudioTracks) == 0 {
		return 0
	}
	if audioTrackIndex >= 0 && audioTrackIndex < len(file.AudioTracks) {
		return audioTrackIndex
	}
	return directPlayAudioTrackIndex(file)
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

// resolvedPlaybackAudioLanguage returns the effective playback.audio_language
// for one canonical settings context. It may return
// playback.OriginalLanguageSentinel, which the caller resolves to a concrete
// language. Returns "" when nothing is stored: the contract default is null,
// "no preference". Resolution and decoding failures are returned so playback
// does not silently substitute a different track.
func resolvedPlaybackAudioLanguage(ctx context.Context, store userstore.UserStore, rc settingsresolve.Context) (string, error) {
	if store == nil || rc.ProfileID == "" {
		return "", nil
	}
	contract, err := settingscontract.Load()
	if err != nil {
		return "", fmt.Errorf("loading settings contract: %w", err)
	}
	resolved, err := settingsresolve.New(contract).Resolve(ctx, store, rc,
		[]string{settingskeys.PlaybackAudioLanguage}, nil)
	if err != nil {
		return "", fmt.Errorf("resolving playback audio language: %w", err)
	}
	if len(resolved) == 0 {
		return "", nil
	}
	var language string
	if err := json.Unmarshal(resolved[0].Value, &language); err != nil {
		return "", fmt.Errorf("decoding playback audio language: %w", err)
	}
	return strings.TrimSpace(language), nil
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
	// Position 0 carries no resume information (mirrors persistStopAndHistory
	// and the jellycompat report path). Progress is last-write-wins, so an
	// early zero heartbeat — e.g. before a client finishes seeking to its
	// resume point — must not wipe the stored resume position.
	if session.Position <= 0 {
		return
	}

	file, err := h.loadFileByPreferredID(ctx, requestedMediaFileID(session), session.MediaFileID)
	targetID := playbackProgressTarget(file)
	if err != nil || targetID == "" {
		return // file not found or not yet matched to a media item
	}

	store, err := h.StoreProvider.ForUser(ctx, session.UserID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to get user store", "component", "api", "user_id", session.UserID, "error", err)
		return
	}

	duration := float64(file.Duration)
	if err := store.UpdateProgress(ctx, session.ProfileID, targetID, session.Position, duration, h.playbackThresholds(ctx)); err != nil {
		slog.ErrorContext(ctx, "failed to persist progress", "component", "api", "session", session.ID, "error", err)
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
		slog.ErrorContext(ctx, "failed to persist version hints", "component", "api", "session", session.ID, "error", err)
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
		slog.ErrorContext(ctx, "failed to persist playback stop", "component", "api", "session", session.ID, "error", err)
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
	return watchsync.ResolveScrobbleIdentity(ctx, h.StableIdentityResolver, event)
}

func (h *PlaybackHandler) scrobbleEventForStoppedSession(
	ctx context.Context,
	session *playback.Session,
	stopResult watchstate.PlaybackStopResult,
) (watchsync.ScrobbleEvent, bool) {
	if session == nil || session.DisableProgressPersistence {
		return watchsync.ScrobbleEvent{}, false
	}

	mediaItemID := stopResult.MediaItemID
	duration := stopResult.DurationSeconds
	position := stopResult.FinalPositionSeconds
	if mediaItemID == "" {
		if h.fileResolver == nil {
			return watchsync.ScrobbleEvent{}, false
		}
		file, err := h.loadFileByPreferredID(ctx, requestedMediaFileID(session), session.MediaFileID)
		if err != nil || file == nil {
			return watchsync.ScrobbleEvent{}, false
		}
		mediaItemID = playbackProgressTarget(file)
		if mediaItemID == "" {
			return watchsync.ScrobbleEvent{}, false
		}
		duration = float64(file.Duration)
		position = session.Position
	}

	event := h.scrobbleEventForSession(ctx, session, mediaItemID, duration, position)
	event.HistoryID = stopResult.HistoryID
	event.Completed = stopResult.Completed
	return event, true
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
			slog.ErrorContext(ctx, "failed to get user store for admin history", "component", "api", "session", session.ID, "error", storeErr)
		} else if store != nil {
			profile, profileErr := store.GetProfile(ctx, session.ProfileID)
			if profileErr != nil {
				slog.ErrorContext(ctx, "failed to load profile for admin history", "component", "api", "session", session.ID, "error", profileErr)
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
		slog.ErrorContext(ctx, "failed to sync sessions", "component", "api", "reason", reason, "error", err)
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
	h.cancelPlaybackStartSideEffectsV3(ctx, session.ID)

	stopResult := h.persistStopAndHistory(ctx, session)
	if h.WatchScrobbler != nil {
		if event, ok := h.scrobbleEventForStoppedSession(ctx, session, stopResult); ok && (userInitiated || stopResult.Completed) {
			if err := h.WatchScrobbler.ScrobbleStop(ctx, event); err != nil {
				slog.WarnContext(ctx, "failed to queue watch provider stop scrobble", "component", "api", "session", session.ID, "error", err)
			}
		} else if ok {
			if err := h.WatchScrobbler.ScrobblePause(ctx, event); err != nil {
				slog.WarnContext(ctx, "failed to queue watch provider pause scrobble", "component", "api", "session", session.ID, "error", err)
			}
		}
	}
	if entry, buildErr := h.buildAdminHistoryEntry(ctx, session); buildErr != nil {
		slog.ErrorContext(ctx, "failed to build admin history", "component", "api", "session", session.ID, "error", buildErr)
	} else if entry != nil && h.AdminStore != nil {
		if err := h.AdminStore.RecordHistory(ctx, *entry); err != nil {
			slog.ErrorContext(ctx, "failed to record admin history", "component", "api", "session", session.ID, "error", err)
		}
	}

	if h.AdminStore != nil {
		if err := h.AdminStore.DeleteSession(ctx, session.ID); err != nil {
			slog.ErrorContext(ctx, "failed to delete synced session", "component", "api", "session", session.ID, "error", err)
		}
	}

	h.closeTranscodeForSession(session)
	// A session that ends must stop egressing everywhere, not just here: the
	// grant is a proxy's whole authority to serve these bytes, and unlike the
	// recipe card it is never a reconstruction aid, so it is revoked on every
	// stop and abort.
	h.deleteProxyGrantV3(ctx, session.ID)
	// The teardown above stopped the remote job, so its stored recipe must not
	// outlive it: a buffered or retrying request would otherwise rebuild ffmpeg on
	// the node for a transport that no longer exists.
	h.deleteNodeRecipeV3(ctx, session.TranscodeTransportID)
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
	h.cancelPlaybackStartSideEffectsV3(ctx, session.ID)

	if h.WatchScrobbler != nil && h.fileResolver != nil {
		if file, err := h.loadFileByPreferredID(ctx, requestedMediaFileID(session), session.MediaFileID); err == nil && file != nil {
			targetID := playbackProgressTarget(file)
			if targetID != "" {
				event := h.scrobbleEventForSession(ctx, session, targetID, float64(file.Duration), session.Position)
				if err := h.WatchScrobbler.ScrobblePause(ctx, event); err != nil {
					slog.WarnContext(ctx, "failed to queue watch provider abort scrobble", "component", "api", "session", session.ID, "error", err)
				}
			}
		}
	}

	if h.AdminStore != nil {
		if err := h.AdminStore.DeleteSession(ctx, session.ID); err != nil {
			slog.ErrorContext(ctx, "failed to delete synced session", "component", "api", "session", session.ID, "error", err)
		}
	}

	// Abort is a connection drop / non-terminal teardown — keep the recipe card
	// so the client can reconstruct on reconnect.
	h.closeTranscodeForSession(session)
	// A session that ends must stop egressing everywhere, not just here: the
	// grant is a proxy's whole authority to serve these bytes, and unlike the
	// recipe card it is never a reconstruction aid, so it is revoked on every
	// stop and abort.
	h.deleteProxyGrantV3(ctx, session.ID)
	// The teardown above stopped the remote job, so its stored recipe must not
	// outlive it: a buffered or retrying request would otherwise rebuild ffmpeg on
	// the node for a transport that no longer exists.
	h.deleteNodeRecipeV3(ctx, session.TranscodeTransportID)
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
		slog.Info("expired inactive playback session", append([]any{
			"session", sessionCopy.ID, "playback_session_id", sessionCopy.ID,
		}, sessionCopy.ClientInfo().LogAttrs()...)...)
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
		slog.ErrorContext(ctx, "failed to access user store for series playback preference", "component", "api", "user_id", userID, "error", err)
		return
	}

	if err := store.SetSeriesPlaybackPreference(ctx, userstore.SeriesPlaybackPreference{
		ProfileID:  profileID,
		SeriesID:   seriesID,
		Resolution: file.Resolution,
		HDR:        file.HDR,
		CodecVideo: file.CodecVideo,
	}); err != nil {
		slog.ErrorContext(ctx, "failed to persist series playback preference", "component", "api", "series_id", seriesID, "profile_id", profileID, "error", err)
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
		slog.ErrorContext(ctx, "failed to access user store for audio preference", "component", "api", "user_id", userID, "error", err)
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
		slog.ErrorContext(ctx, "failed to persist audio preference", "component", "api", "series_id", seriesID, "profile_id", profileID, "error", err)
	}
}

// --- Handler methods ---

// HandleStartPlayback starts playback. Protocol v3 is the only protocol this
// endpoint speaks: a start that does not declare it comes from a build that
// predates the contract and cannot interpret a plan, so it is refused with
// 426 rather than served something it would misread.
func (h *PlaybackHandler) HandleStartPlayback(w http.ResponseWriter, r *http.Request) {
	if apimw.GetUserID(r.Context()) == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPlaybackV3BodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	var envelope struct {
		ProtocolVersion *int `json:"protocol_version"`
		Capabilities    *struct {
			VideoEvidence *string `json:"video_evidence"`
			AudioEvidence *string `json:"audio_evidence"`
		} `json:"client_capabilities"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if envelope.ProtocolVersion == nil || *envelope.ProtocolVersion != playback.ProtocolV3 ||
		envelope.Capabilities == nil || envelope.Capabilities.VideoEvidence == nil || envelope.Capabilities.AudioEvidence == nil {
		upgrade := playback.LegacyUpgradeErrorV3()
		writeError(w, http.StatusUpgradeRequired, upgrade.Error, upgrade.Message)
		return
	}
	h.handleStartPlaybackV3(w, r, body)
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
							slog.WarnContext(r.Context(), "failed to queue watch provider pause scrobble", "component", "api", "session", sessionID, "error", err)
						}
					} else if err := h.WatchScrobbler.ScrobbleStart(r.Context(), event); err != nil {
						slog.WarnContext(r.Context(), "failed to queue watch provider resume scrobble", "component", "api", "session", sessionID, "error", err)
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
	case file.ExtraID != "":
		if h.ExtraLookup == nil {
			return nil, fmt.Errorf("extra lookup not configured")
		}
		extra, err := h.ExtraLookup.GetByID(r.Context(), file.ExtraID)
		if err != nil {
			if errors.Is(err, catalog.ErrExtraNotFound) {
				return nil, catalog.ErrItemNotFound
			}
			return nil, err
		}
		if extra == nil {
			return nil, catalog.ErrItemNotFound
		}
		if err := h.ItemAccess.EnsureAccessible(r.Context(), extra.ParentID, filter); err != nil {
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

// alignedSeekSeconds snaps an encoded transcode's ffmpeg start position down
// to the boundary of the segment computeStartSegment assigns it. The synthetic
// VOD manifest declares segment N to begin at exactly N×segmentDuration;
// spawning ffmpeg at the raw seek position makes segment N actually begin up
// to one segment later, and hls.js aligns that content to the declared
// position — shifting the session's entire timeline (audio, video, and every
// out-of-band subtitle cue) late by seek mod segmentDuration. Copy-mode
// sessions serve ffmpeg's real manifest, whose declared timings match the
// fragments it produces, so they keep the raw seek.
func alignedSeekSeconds(seekSeconds float64, segmentDuration int, targetVideoCodec string) float64 {
	if strings.EqualFold(targetVideoCodec, "copy") || seekSeconds <= 0 {
		return seekSeconds
	}
	if segmentDuration <= 0 {
		segmentDuration = 2
	}
	return float64(computeStartSegment(seekSeconds, segmentDuration) * segmentDuration)
}

// HandleGetTranscodeManifest handles GET /playback/transcode/{session_id}/master.m3u8.
// Legacy transports allow the session UUID to act as the access capability.
// Header-authenticated V3 transports require the live session owner on every
// request; their UUID is only a route identifier.
//
// Known-duration encoded sessions expose a synthetic full VOD manifest so the
// player can seek immediately. Copy-video sessions expose FFmpeg's real
// keyframe-aligned manifest and use the resolved stream origin the v3 plan
// reports as the timeline's stream_origin_seconds.
//
// Errors: 404 (playback_session_not_found / not_found) when the session is
// missing or cannot be reconstructed, 503 (unavailable) while the transcode is
// temporarily unavailable, and — for tone-map execution failures — 422
// (unsupported) with an X-Silo-Tone-Map-Execution-Error header of
// source_revision_changed or source_preflight_rejected. The 422 responses are
// additive to the existing 404 and 503 cases; the Jellyfin-compatible 415
// mapping is a separate surface and unchanged.
func (h *PlaybackHandler) HandleGetTranscodeManifest(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	session, status, card, claims, reconstructErr := h.loadTranscodeServeSession(r, sessionID, -1)
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
	case playback.SessionUnauthorized:
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	case playback.SessionUnavailable:
		if writeNativeRouteBindingErrorV3(w, reconstructErr) {
			return
		}
		if writePlaybackToneMapExecutionError(w, reconstructErr) {
			return
		}
		if card == nil || card.ToneMapMode == "" {
			// A plain (non-tone-mapped) transcode whose token recipe cannot be
			// rebuilt is served the same 404 the second-chance reconstruct
			// branch produces: the session is effectively gone, and the client
			// re-plans rather than retrying a dead encode.
			writeError(w, http.StatusNotFound, "not_found", "Transcode session not found")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Transcode session is temporarily unavailable")
		return
	}
	if !requireNativeSessionAPIEgressV3(w, session) {
		return
	}
	attachPlaybackSession(r.Context(), session, claims)

	transcodeSession := h.tm.GetTranscodeSession(sessionID)
	if transcodeSession == nil {
		// No local session — try proxying to remote transcode node.
		if session.TranscodeNodeURL != "" {
			h.touchSessionActivity(sessionID)
			h.proxyToTranscodeNode(w, r, session.TranscodeNodeURL,
				"/transcode/"+remoteTransportID(session)+"/master.m3u8")
			return
		}
		// Local transcode whose process state was lost: reconstruct it from the
		// token recipe. The manifest path has no segment context, so pass -1 (use
		// the token's seek position).
		var secondChanceErr error
		transcodeSession, secondChanceErr = h.reconstructTransportForServe(r.Context(), sessionID, -1, card)
		if secondChanceErr != nil && writePlaybackToneMapExecutionError(w, secondChanceErr) {
			return
		}
		if transcodeSession == nil {
			writeError(w, http.StatusNotFound, "not_found", "Transcode session not found")
			return
		}
	}
	h.touchSessionActivity(sessionID)

	manifest, err := transcodeSession.BuildPlaybackManifest("segment/", r.URL.RawQuery)
	if err != nil {
		slog.ErrorContext(r.Context(), "build transcode manifest", "component", "api", "error", err, "session", sessionID, "playback_session_id", sessionID)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Transcode manifest not ready")
		return
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(manifest)
}

func writePlaybackToneMapExecutionError(w http.ResponseWriter, err error) bool {
	if errors.Is(err, tonemap.ErrSourceRevisionChanged) {
		w.Header().Set(transcodenode.ToneMapExecutionErrorHeader, transcodenode.ToneMapSourceRevisionChangedCode)
		writeError(w, http.StatusUnprocessableEntity, "unsupported", "Tone-map source changed")
		return true
	}
	if errors.Is(err, playback.ErrToneMapSourceValidationUnavailable) {
		w.Header().Set(transcodenode.ToneMapExecutionErrorHeader, transcodenode.ToneMapSourceValidationUnavailableCode)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Transcode session is temporarily unavailable")
		return true
	}
	if errors.Is(err, tonemap.ErrSourcePreflightRejected) {
		w.Header().Set(transcodenode.ToneMapExecutionErrorHeader, transcodenode.ToneMapSourcePreflightRejectedCode)
		writeError(w, http.StatusUnprocessableEntity, "unsupported", "Tone-map source is unsupported by the selected executor")
		return true
	}
	return false
}

// HandleGetTranscodeSegment handles GET /playback/transcode/{session_id}/segment/{name}.
// Authorization follows the same negotiated legacy-versus-header-authenticated
// rule as the manifest endpoint above.
//
// Errors: 404 (playback_session_not_found / not_found) when the session is
// missing or cannot be reconstructed, or the segment does not exist; 503
// (unavailable) while the transcode is temporarily unavailable; and — for
// tone-map execution failures — 422 (unsupported) with an
// X-Silo-Tone-Map-Execution-Error header of source_revision_changed or
// source_preflight_rejected. The 422 responses are additive to the existing
// 404 and 503 cases; the Jellyfin-compatible 415 mapping is a separate surface
// and unchanged.
func (h *PlaybackHandler) HandleGetTranscodeSegment(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	requestedSegment := -1
	if segNum, parseErr := playback.ParseSegmentNumber(chi.URLParam(r, "name")); parseErr == nil {
		requestedSegment = segNum
	}
	session, status, card, claims, reconstructErr := h.loadTranscodeServeSession(r, sessionID, requestedSegment)
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
	case playback.SessionUnauthorized:
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	case playback.SessionUnavailable:
		if writeNativeRouteBindingErrorV3(w, reconstructErr) {
			return
		}
		if writePlaybackToneMapExecutionError(w, reconstructErr) {
			return
		}
		if card == nil || card.ToneMapMode == "" {
			// A plain (non-tone-mapped) transcode whose token recipe cannot be
			// rebuilt is served the same 404 the second-chance reconstruct
			// branch produces: the session is effectively gone, and the client
			// re-plans rather than retrying a dead encode.
			writeError(w, http.StatusNotFound, "not_found", "Transcode session not found")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Transcode session is temporarily unavailable")
		return
	}
	if !requireNativeSessionAPIEgressV3(w, session) {
		return
	}
	attachPlaybackSession(r.Context(), session, claims)

	transcodeSession := h.tm.GetTranscodeSession(sessionID)
	if transcodeSession == nil {
		if session.TranscodeNodeURL != "" {
			h.touchSessionActivity(sessionID)
			segmentName := chi.URLParam(r, "name")
			h.proxyToTranscodeNode(w, r, session.TranscodeNodeURL,
				"/transcode/"+remoteTransportID(session)+"/segment/"+segmentName)
			return
		}
		// Resume near the segment the client is fetching so reconstruct does not
		// restart from the original seek point and stall. A non-segment name
		// (e.g. init.mp4) parses as negative and falls back to the token position.
		var secondChanceErr error
		transcodeSession, secondChanceErr = h.reconstructTransportForServe(r.Context(), sessionID, requestedSegment, card)
		if secondChanceErr != nil && writePlaybackToneMapExecutionError(w, secondChanceErr) {
			return
		}
		if transcodeSession == nil {
			writeError(w, http.StatusNotFound, "not_found", "Transcode session not found")
			return
		}
	}
	h.touchSessionActivity(sessionID)

	segmentName := chi.URLParam(r, "name")
	segmentLease, err := transcodeSession.OpenSegment(segmentName)
	if err != nil && errors.Is(err, playback.ErrSegmentNotFound) {
		segNum, parseErr := playback.ParseSegmentNumber(segmentName)
		if parseErr == nil {
			now := time.Now()
			decision := transcodeSession.SegmentRecoveryDecision(segNum, now)
			lastProducedAgeMS := int64(-1)
			if !decision.Progress.LastProducedAt.IsZero() {
				lastProducedAgeMS = now.Sub(decision.Progress.LastProducedAt).Milliseconds()
			}
			slog.InfoContext(r.Context(), "transcode segment missing", "component", "api",
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
				slog.InfoContext(r.Context(), "transcode segment wait", "component", "api",
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
				segmentLease, err = transcodeSession.WaitForOpenSegment(segmentName, decision.WaitTimeout)
				if err != nil && errors.Is(err, playback.ErrSegmentNotFound) {
					slog.InfoContext(r.Context(), "transcode segment wait timeout", "component", "api",
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
				target, ok, restartErr := h.tm.RestartSegmentLocked(
					r.Context(),
					sessionID,
					transcodeSession,
					segNum,
				)
				if restartErr != nil && !errors.Is(restartErr, playback.ErrManifestNotReady) {
					slog.ErrorContext(r.Context(), "restart transcode at missing segment", "component", "api", "error", restartErr, "segment", segmentName, "session", sessionID, "playback_session_id", sessionID)
				}

				// Copy-mode with an unresolved seek target (ok=false, no error)
				// means the manifest can't place this segment yet. Don't restart
				// at a fabricated position; surface ErrSegmentNotFound so the
				// client retries while the session keeps producing manifest.
				// Mirrors the transcode-node guard in
				// internal/transcodenode/server.go.
				if !ok && restartErr == nil && transcodeSession.IsCopyVideo() {
					err = playback.ErrSegmentNotFound
				}

				if restartErr != nil {
					err = restartErr
				} else if ok {
					slog.InfoContext(r.Context(), "transcode seek restart", "component", "api",
						"segment", segmentName,
						"requested_segment", segNum,
						"produced_head", decision.Progress.ProducedHead,
						"last_requested_segment", decision.Progress.LastRequestedSegment,
						"start_segment_number", decision.Progress.StartSegmentNumber,
						"last_produced_age_ms", lastProducedAgeMS,
						"wait_timeout_ms", decision.WaitTimeout.Milliseconds(),
						"restart_on_timeout", decision.RestartOnTimeout,
						"reason", decision.Reason,
						"seek_seconds", target.SeekSeconds,
						"stream_origin_seconds", target.StreamOriginSeconds,
						"resolved_start_segment", target.StartSegmentNumber,
						"session", sessionID,
						"playback_session_id", sessionID,
					)
					// Throttler + exit monitor re-arm via the session's
					// restart hook.
					segmentLease, err = transcodeSession.WaitForOpenSegment(segmentName, 30*time.Second)
					if err == nil && strings.EqualFold(transcodeSession.Opts().TargetCodecVideo, "copy") {
						// Copy-mode seeks can resume as soon as the target segment
						// exists, but that sometimes leaves the player one segment
						// away from stalling while FFmpeg catches up. Briefly wait
						// for a single lookahead fragment when available so the
						// first resumed playback window is less brittle.
						nextSegmentName := fmt.Sprintf("seg_%05d%s", segNum+1, filepath.Ext(segmentName))
						if nextSegment, nextErr := transcodeSession.WaitForOpenSegment(nextSegmentName, 1200*time.Millisecond); nextErr == nil {
							_ = nextSegment.Close()
						}
					}
				}
			}
		} else if transcodeSession.IsRunning() {
			// Non-numbered segment (e.g., init.mp4 for fMP4 HLS).
			// Wait briefly — the init segment is written almost immediately.
			segmentLease, err = transcodeSession.WaitForOpenSegment(segmentName, 10*time.Second)
		}
	}
	if err != nil {
		if writePlaybackToneMapExecutionError(w, err) {
			return
		}
		if errors.Is(err, playback.ErrSegmentNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Segment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load segment")
		return
	}

	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	defer func() { _ = segmentLease.Close() }()
	sw := httpstream.NewRollingDeadlineWriter(w)
	http.ServeContent(sw, r, segmentLease.Info.Name(), segmentLease.Info.ModTime(), segmentLease.File)
	if r.Method == http.MethodGet &&
		sw.CompletedFullResponse(segmentLease.Info.Size()) {
		if segNum, parseErr := playback.ParseSegmentNumber(segmentName); parseErr == nil {
			transcodeSession.ReportSegmentDownloadedForGeneration(segNum, segmentLease.Generation)
		}
	}
}

// buildProxyManifestURL signs a stream token carrying the session's full
// reconstruction recipe and builds the manifest URL. proxyNode is the planner's
// pick; when nil the URL falls back to the API-local path, where the token rides
// the ?st= query parameter so the integrated server can reconstruct from it.
//
// requireMediaAuth is the attempt's negotiated media-auth mode: such a session
// never receives a token, and therefore never a proxy origin either, since a
// proxy authenticates from the token in the URL path alone. It gets the
// API-local manifest path, which the client fetches with its own credential.
func (h *PlaybackHandler) buildProxyManifestURL(card playback.RecipeCard, proxyNode *nodepool.Node, requireMediaAuth bool) string {
	localURL := fmt.Sprintf("/playback/transcode/%s/master.m3u8", card.SessionID)
	if proxyNode == nil {
		return appendStreamToken(localURL, h.signSessionToken(card, requireMediaAuth))
	}
	card.RoutingEgressNodeID = proxyNode.ID
	token := h.signSessionToken(card, requireMediaAuth)
	if token == "" {
		return appendStreamToken(localURL, token)
	}
	return nodepool.NodeEndpoint(proxyNode.ClientURL(), "/stream/transcode/"+token+"/master.m3u8")
}

// proxyToTranscodeNode forwards a request to the remote transcode node.
func (h *PlaybackHandler) proxyToTranscodeNode(w http.ResponseWriter, r *http.Request, transcodeNodeURL, path string) {
	sessionID := chi.URLParam(r, "session_id")
	targetURL := transcodeNodeURL + path
	isSegmentRoute := strings.Contains(path, "/segment/")
	_, segmentParseErr := playback.ParseSegmentNumber(filepath.Base(path))
	isMediaSegment := segmentParseErr == nil
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
	if isSegmentRoute {
		// The node's immediate transport peer is this API process, so receiving a
		// complete response there does not prove that the browser received it.
		// Suppress node-local accounting and acknowledge only after the downstream
		// writer completes. Forward range validators so both hops serve the same
		// representation.
		transcodeproxy.PrepareRequest(req, r)
	}
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
			slog.WarnContext(r.Context(), "stream token not forwarded to transcode node", "component", "api", "error", verifyErr, "playback_session_id", sessionID)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.ErrorContext(r.Context(), "proxy to transcode node", "component", "api", "error", err, "url", targetURL, "playback_session_id", sessionID)
		http.Error(w, "transcode node unavailable", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// The node strips "st" from the request query (kept out of node URLs/logs),
	// so the segment/init URIs in the manifest it builds carry no token. Without
	// it, a segment fetched after a node or API restart cannot reconstruct the
	// session and 404s. Re-inject the client-facing token into every URI at this
	// boundary so the client's later segment requests carry "st" again. Only the
	// manifest body is rewritten; segments stream through untouched.
	if validToken != "" && resp.StatusCode == http.StatusOK && strings.HasSuffix(path, ".m3u8") {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			slog.ErrorContext(r.Context(), "read transcode node manifest", "component", "api", "error", readErr, "url", targetURL, "playback_session_id", sessionID)
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

	generation := resp.Header.Get(transcodeproxy.GenerationHeader)
	transcodeproxy.CopyResponseHeaders(w.Header(), resp.Header)
	// Proxied transcode output can stream past the server's absolute
	// WriteTimeout; roll the write deadline with progress instead.
	sw := httpstream.NewRollingDeadlineWriter(w)
	sw.WriteHeader(resp.StatusCode)
	if _, copyErr := io.Copy(sw, resp.Body); copyErr != nil {
		return
	}
	fullSize := transcodeproxy.FullRepresentationSize(resp)
	if isMediaSegment && generation != "" && r.Method == http.MethodGet &&
		sw.CompletedFullResponse(fullSize) {
		if ackErr := transcodeproxy.Acknowledge(r.Context(), http.DefaultClient, transcodeNodeURL+path, h.JWTSecret, generation); ackErr != nil {
			slog.WarnContext(r.Context(), "acknowledge transcode segment completion", "component", "api", "error", ackErr, "playback_session_id", sessionID)
		}
	}
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

// findAlternateFile finds another file version for the same content. It
// prefers non-4K versions because they can escape a disabled-4K-transcode
// terminal, but when none exist it returns a 4K candidate so the planner can
// still accept one that direct-plays or remuxes without forbidden video
// encoding.
// Within each class it prefers SDR, then resolution, then bitrate.
func (h *PlaybackHandler) findAlternateFile(ctx context.Context, source *models.MediaFile) (*models.MediaFile, error) {
	candidates, err := h.findAlternateFiles(ctx, source)
	if err != nil || len(candidates) == 0 {
		return nil, err
	}
	return candidates[0], nil
}

// findAlternateFiles returns every compatible edition/version candidate in
// fallback order. Callers that plan candidates must keep trying after a
// terminal: a lower-resolution candidate can still fail while a later 4K
// candidate direct-plays or remuxes without forbidden video encoding.
func (h *PlaybackHandler) findAlternateFiles(ctx context.Context, source *models.MediaFile) ([]*models.MediaFile, error) {
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

	candidates := make([]*models.MediaFile, 0, len(files))
	for _, f := range files {
		if f.ID == source.ID {
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

	// Prefer non-4K before 4K so a lower-resolution sibling is tried first.
	// Keep 4K siblings at the end: when no non-4K sibling exists, the planner
	// may still direct-play or remux one because the policy only forbids video
	// encoding.
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		a4K := playback.Is4KMediaFileV3(a)
		b4K := playback.Is4KMediaFileV3(b)
		if a4K != b4K {
			return !a4K
		}
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

	return candidates, nil
}

const (
	transcodeResolution2160p = "2160p"
	transcodeResolution1080p = "1080p"
	transcodeResolution720p  = "720p"
	transcodeResolution480p  = "480p"
	transcodeResolution420p  = "420p"
	transcodeResolution328p  = "328p"
)

// resolutionRank returns a numeric rank for resolution sorting.
func resolutionRank(res string) int {
	height, known := transcodeResolutionHeight(res)
	if !known {
		return 0
	}

	switch {
	case height >= 2160:
		return 4
	case height >= 1080:
		return 3
	case height >= 720:
		return 2
	case height >= 480:
		return 1
	default:
		return 0
	}
}

func transcodeResolutionHeight(resolution string) (int, bool) {
	switch resolution {
	case transcodeResolution2160p:
		return 2160, true
	case transcodeResolution1080p:
		return 1080, true
	case transcodeResolution720p:
		return 720, true
	case transcodeResolution480p:
		return 480, true
	case transcodeResolution420p:
		return 420, true
	case transcodeResolution328p:
		return 328, true
	default:
		return 0, false
	}
}
