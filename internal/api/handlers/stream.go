package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/config"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/remotestream"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary/altmount"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary/resolver"
)

const (
	subtitleFormatASS  = "ass"
	subtitleFormatSSA  = "ssa"
	subtitleFormatSUP  = "sup"
	subtitleFormatSRT  = "srt"
	subtitleMIMESubRip = "application/x-subrip"
	// subtitleSourceUnavailableErrorCode is the subtitle-local resolve failure.
	// A subtitle sidecar/font request that cannot resolve its provider source is
	// a subtitle-delivery problem, never the playback transport's
	// virtual_resolve_failed: the client must not read it as the video source
	// having failed and move off the release.
	subtitleSourceUnavailableErrorCode = "subtitle_source_unavailable"
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
	// StreamDeny is the shared session-deny marker (same instance as the
	// PlaybackHandler's). A denied session is answered 410 and never
	// reconstructed. Nil-safe: without Redis nothing is ever denied.
	StreamDeny *playback.StreamDeny
	// PlanStoreV3 is the shared attempt store (same instance as the
	// PlaybackHandler's); an aborted session's attempt row is marked stopped
	// through it. May be nil (tests / minimal setups).
	PlanStoreV3 playback.PlanStoreV3
	// SourceCommittedNotifier publishes the effective version a serve-layer
	// rotation just committed to, with its declared inventory, so a playing
	// client can follow the streamed release before any replan. Optional: a nil
	// notifier keeps the rotation commit but sends no event.
	SourceCommittedNotifier sourceCommittedNotifier
	// SessionRecipeStore is the PlaybackHandler's store of header-authenticated
	// session recipes (same instance), the only reconstruction source for a
	// session whose URLs carry no stream token. May be nil.
	SessionRecipeStore sessionRecipeStoreV3
	// FinishOtherEnd is the PlaybackHandler's finisher for a stop an abort
	// here took over (same instance). May be nil; the finish hook then
	// records the stop's history without its provider event.
	FinishOtherEnd func(ctx context.Context, claim nativeEndClaim, sessionID string)
	// PlaybackConfig returns the current playback config; read it through
	// ffmpegPath(). May be nil (tests).
	PlaybackConfig func() config.PlaybackConfig
	// CopySafetyRacer gates and covers a revived progressive remux: it answers
	// whether this replica already condemns a video stream-copy of the source,
	// and re-engages the copy-safety race for one whose verdict is still open.
	// Optional — without it a revived remux is gated on the persisted row alone
	// and no race is started here.
	CopySafetyRacer PlaybackCopySafetyRacer
	// SubtitleCache stores complete embedded subtitle extracts under the transcode
	// dir so repeat selections skip the whole-file ffmpeg demux. May be nil
	// (tests / minimal setups) — extraction then always streams uncached.
	SubtitleCache *playback.SubtitleCache
	// subtitleWarmWG tracks handler-owned detached subtitle warms
	// (warmVirtualSubtitleAfterWindowMiss). They outlive the request by design —
	// the request's relay registration is released when the request ends, so the
	// handler starts its own warm — and a caller that owns the cache directory
	// (a test with a temp dir, a graceful drain) waits on this to keep the warm
	// from writing into a directory being torn down.
	subtitleWarmWG sync.WaitGroup
	// virtualSubtitleLayoutMu guards virtualSubtitleLayouts, the short-lived
	// memo of a drift probe's verdict. Windowed playback requests the same
	// track every couple of minutes; re-probing the live relay layout on each
	// one costs seconds before the extraction even starts. The memo is keyed by
	// the pre-probe (CacheIdentity, plan ordinal) and expires after
	// virtualSubtitleLayoutCacheTTL. Only successful probes are recorded, so a
	// probe error or a positive mismatch is re-evaluated on every request. The
	// zero value (nil map) is usable.
	virtualSubtitleLayoutMu sync.Mutex
	virtualSubtitleLayouts  map[string]virtualSubtitleLayoutVerdict
	// virtualSubtitleLayoutMisses records the opposite verdict: a probe that
	// completed and found no live track this plan ordinal could carry. Only the
	// warm establishes it (it probes its own relay registration), and a serve
	// that never probes again would otherwise start the same fill forever: every
	// page is refused, the fill abandons on the same positive mismatch, and the
	// loop has no answer. The serve consults it and answers
	// `subtitle_source_changed` so the client replans onto the rotated
	// inventory. Keyed like virtualSubtitleLayouts and kept much shorter: a
	// rotated release is a fact about the bytes, and the client needs to re-select
	// soon. A probe error never records one — only a completed probe that found
	// no match. The zero value (nil map) is usable.
	virtualSubtitleLayoutMisses map[string]time.Time
	SubtitleRepo                subtitles.Repository // optional; enables S3-sourced subtitles
	S3Client                    subtitles.S3Client   // optional; needed for fetching S3 subtitles
	S3Bucket                    string               // bucket for subtitle storage
	// VirtualMediaResolver resolves virtual:// URIs to a real provider URL.
	// Required for embedded subtitle extraction from virtual sources.
	VirtualMediaResolver         VirtualMediaResolver
	VirtualMediaRefreshResolver  VirtualMediaRefreshResolver
	VirtualMediaDetailedResolver VirtualMediaDetailedResolver
	// VirtualFileSaver/VirtualFileMetadataSaver persist a refreshed provider URL
	// through the existing Phase-1 write path when a candidate's stored URL has
	// expired. Nil disables the refresh; the resolve still lists as before.
	VirtualFileSaver         VirtualFileSaver
	VirtualFileMetadataSaver VirtualFileMetadataSaver
	// VirtualReleaseCacheStatus reports whether a pinned release already has a
	// cached copy in AltMount. known=false, or a nil probe, preserves today's
	// serve behavior: the session streams the provider source and no handoff is
	// attempted.
	VirtualReleaseCacheStatus VirtualReleaseCacheStatusFunc
	// VirtualReleaseCacheFiller starts (or joins) a provider-side cache fill for
	// an uncached pinned release. Pressing play on an uncached release calls it;
	// the provider fetches the media, Vio only serves the addon URL. Nil
	// disables play-triggered fills while leaving remote streaming intact.
	VirtualReleaseCacheFiller VirtualReleaseCacheFillFunc
	// DownloadProgress delivers download.progress events to a live session.
	// *playback.RealtimeHub implements it. Nil disables the push.
	DownloadProgress DownloadProgressPublisher
	// cacheWaiters indexes the live sessions streaming an uncached pinned
	// release, so the monitor's release-confirmed signal can hand each off to
	// its cached copy. See HandleVirtualReleaseConfirmed.
	cacheWaiters virtualCacheHandoffRegistry
	// RemoteStreamRelay pins the resolved provider URL to a loopback relay
	// so ffmpeg reads through it with a stable IP.
	RemoteStreamRelay *remotestream.Relay
	// AllowInsecureVirtual reports whether the owning plugin installation has
	// explicitly enabled allow_insecure_http for HTTP manifests on
	// private/local provider hosts.
	AllowInsecureVirtual func(installationID int) bool
	// AllowPrivateStreams reports whether the core
	// virtual_library.allow_private_streams opt-in permits virtual streams
	// from private/local network destinations.
	AllowPrivateStreams func(installationID int) bool
	// VirtualCandidateTrustWindow reports how long a persisted virtual
	// candidate row may be trusted for replay after its last listing or
	// resolution. A positive value keeps a delisted same-identity candidate
	// preferred (and retained) inside the window; zero disables the window and
	// keeps the pre-window behavior. Wired lazily from the settings store.
	VirtualCandidateTrustWindow func() time.Duration
	// VirtualCandidateFailMarker stamps a virtual candidate row as known-bad
	// after a transport produced no bytes, so the auto-pick skips it on the
	// next play while the dropdown still shows it for a manual retry. It is
	// fenced on the candidate identity the transport served (expectedFilePath)
	// and the failure state observed when it started (observedFailedAt): a row
	// rotated in place to a sibling is never stamped by a late failure of the
	// candidate the session actually served.
	VirtualCandidateFailMarker func(ctx context.Context, fileID int, expectedFilePath string, observedFailedAt *time.Time) error
	// VirtualCandidateRecoveredMarker clears a known-bad stamp after the
	// candidate actually delivered media bytes to a client — the only evidence
	// that forgives a transport failure. The callback is fenced on the
	// delivered candidate identity and the failure state observed when the
	// transport started, so a rotation or a newer failure is never cleared.
	// Metadata-only liveness checks resolve URLs without opening media and
	// must never clear it.
	VirtualCandidateRecoveredMarker func(ctx context.Context, fileID int, deliveredFilePath string, observedFailedAt *time.Time) error
	SubtitleBlobs                   subtitles.BlobStore // optional; backs downloaded subtitle reads
	// ExternalTimings finds sidecar timing corrections; nil serves sidecars as
	// they are on disk.
	ExternalTimings subtitles.ExternalTimingLookup
	// PlaySync aligns a subtitle the first time a player is served it, when
	// it was never synced; nil leaves that to a request.
	PlaySync subtitles.PlaySyncer
	// fontExtractFailures throttles the repetitive "subtitle font extraction
	// failed" warning to the first failure per file+track; repeats drop to
	// debug. A successful extraction clears the key, so a later regression warns
	// again. The key space is the target identity (file_id+track, or
	// session+track before the file resolves), so recovery and failure always
	// address the same slot; the retained map is capped with TTL and
	// count-based eviction (see fontExtractFailLog). Font extraction never
	// fails playback: the endpoint answers a pending empty bundle rather than
	// a 500, and the throttled warning keeps the diagnostic trail.
	fontExtractFailures fontExtractFailLog
	// virtualStreamRecoveryTerminals bounds cross-request recovery after a
	// session-bound virtual candidate and its one bounded rotation have both
	// failed. Once recorded, matching requests fail fast for a short TTL
	// instead of replaying the resolve and rotation on every segment retry. A
	// successful serve or a different candidate binding clears the way.
	virtualStreamRecoveryTerminals virtualStreamRecoveryCache
}

// fontExtractFailLog throttles the subtitle-font extraction warning. See the
// field doc on StreamHandler.fontExtractFailures.
//
// Retention is bounded two ways so unique keys — pre-resolution session+track
// pairs above all, since session IDs are unbounded over the process lifetime —
// cannot grow the map without bound: an entry expires (and the next failure
// for it warns again) after fontExtractFailLogTTL, and the map evicts the
// oldest entries once it reaches fontExtractFailLogMaxEntries. A failed or
// abandoned target may never reach a success path that clears its key, so
// cleanup-on-success alone would leak those entries.
type fontExtractFailLog struct {
	mu   sync.Mutex
	seen map[string]time.Time
	// now is the clock used for TTL checks and eviction ordering; nil uses
	// time.Now. Tests inject a monotonic fake.
	now func() time.Time
}

const (
	// fontExtractFailLogMaxEntries caps the throttle map. Eviction is by
	// oldest insertion, so a live target keeps its slot while it keeps
	// failing inside the TTL; an evicted target merely warns again on its
	// next failure — diagnostics only, never correctness.
	fontExtractFailLogMaxEntries = 512
	// fontExtractFailLogTTL bounds how long one failure can stay throttled,
	// so an unrecovered failure cannot suppress warnings forever.
	fontExtractFailLogTTL = time.Hour
)

func (l *fontExtractFailLog) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// failed logs the first failure for key at warn and repeats at debug.
func (l *fontExtractFailLog) failed(ctx context.Context, key string, attrs ...any) {
	l.mu.Lock()
	now := l.clock()
	if l.seen == nil {
		l.seen = make(map[string]time.Time)
	}
	at, repeat := l.seen[key]
	if repeat && now.Sub(at) >= fontExtractFailLogTTL {
		// The entry expired: a target that kept failing silently for the TTL
		// re-warns once instead of staying throttled forever.
		repeat = false
	}
	if !repeat {
		l.evictLocked(now)
		l.seen[key] = now
	}
	l.mu.Unlock()
	// The message wording is fixed so sloglint sees string literals; the
	// cause attribute distinguishes the failure kinds, and the throttle key
	// space gives each cause its own slot.
	args := append([]any{virtualEvidenceLogKeyComponent, virtualEvidenceLogValueAPI}, attrs...)
	if repeat {
		slog.DebugContext(ctx, "subtitle font request failed", args...)
		return
	}
	slog.WarnContext(ctx, "subtitle font request failed", args...)
}

// evictLocked makes room when the map is full: it drops expired entries
// first, then the oldest ones, so the map never grows past
// fontExtractFailLogMaxEntries no matter how many unique session/track
// failures arrive.
func (l *fontExtractFailLog) evictLocked(now time.Time) {
	for len(l.seen) >= fontExtractFailLogMaxEntries {
		oldestAt := now
		oldestKey := ""
		for key, at := range l.seen {
			if now.Sub(at) >= fontExtractFailLogTTL {
				delete(l.seen, key)
				continue
			}
			if oldestKey == "" || at.Before(oldestAt) {
				oldestAt, oldestKey = at, key
			}
		}
		if oldestKey != "" {
			delete(l.seen, oldestKey)
		}
	}
}

// recovered clears the throttle key after a successful extraction so a later
// failure is reported at warn again. The key must be the same one the failure
// inserted — success and failure both build it with fontExtractFailureKey, so
// failure → recovery → failure warns again.
func (l *fontExtractFailLog) recovered(key string) {
	l.mu.Lock()
	delete(l.seen, key)
	l.mu.Unlock()
}

// fontExtractFailureKey identifies an extraction target across retries.
func fontExtractFailureKey(fileID, trackIndex int) string {
	return strconv.Itoa(fileID) + ":" + strconv.Itoa(trackIndex)
}

// logFontBundleExtractFailure records an on-demand font extraction failure.
// The extraction runs detached and often outlives the request that started it:
// the client receives a pending bundle and polls again, so the request-side
// error handling never sees the cause. Reporting from the extraction closure
// keeps a slow-relay dump failure visible at warn (throttled per file+track)
// instead of leaving only the cache backoff's debug line.
func (h *StreamHandler) logFontBundleExtractFailure(ctx context.Context, file *models.MediaFile, trackIndex int, err error) {
	if h == nil || file == nil || err == nil || errors.Is(err, errVirtualFontResolve) {
		// Provider resolution has its own log path in the callers; logging it
		// here too would double every relay failure.
		return
	}
	h.fontExtractFailures.failed(ctx, fontExtractFailureKey(file.ID, trackIndex),
		"file_id", file.ID,
		"track", trackIndex,
		"error", err,
	)
}

// logSubtitleFontInternalError records the cause behind the font route's
// internal_error returns. Neither the session-load nor the source-preflight
// failure used to log anything, so a client that retried a broken font URL
// produced a flood the v2 request log could only describe as a bare
// internal_error. The extraction-failure throttle backs it: the first failure
// per target warns with file_id+track (or session+track before the file
// resolves) and repeats drop to debug. Status codes and bodies are unchanged.
//
// The throttle key is the target identity alone — file_id+track, or
// session+track before the file resolves. The cause travels as an attribute:
// recovery clears exactly the key a failure inserted, so failure →
// recovery → failure warns again regardless of which causes were involved.
// The session path keys on the parsed track index (normalized) rather than the
// raw request string when it can, keeping the keyspace bounded.
func (h *StreamHandler) logSubtitleFontInternalError(ctx context.Context, in SubtitleFontRequest, fileID, trackIndex int, cause string, err error) {
	if h == nil || err == nil {
		return
	}
	if fileID > 0 {
		h.fontExtractFailures.failed(ctx, fontExtractFailureKey(fileID, trackIndex),
			"file_id", fileID,
			"track", trackIndex,
			"cause", cause,
			"error", err,
		)
		return
	}
	// The file (and its track list) is not known yet, so the raw request
	// track is the only identity available. Parsing it here, when it parses,
	// normalizes the key against what the file-side path would later use.
	rawTrack := in.Track
	if parsed, _, parseErr := playback.ParseSubtitleTrackParam(rawTrack); parseErr == nil {
		rawTrack = strconv.Itoa(parsed)
	}
	h.fontExtractFailures.failed(ctx, "session:"+in.SessionID+":track:"+rawTrack,
		"session", in.SessionID,
		"track", in.Track,
		"cause", cause,
		"error", err,
	)
}

// subtitlePlayed hands a subtitle a player is being served to PlaySync.
func (h *StreamHandler) subtitlePlayed(r *http.Request, target subtitles.SyncTarget) {
	if h.PlaySync != nil {
		h.PlaySync.SubtitlePlayed(r.Context(), target)
	}
}

// ffmpegPath returns the currently configured ffmpeg binary path.
func (h *StreamHandler) ffmpegPath() string {
	if h.PlaybackConfig != nil {
		return h.PlaybackConfig().FFmpegPath
	}
	return ""
}

// waitForBackgroundSubtitleWarms blocks until every handler-owned detached
// subtitle warm started so far has settled. A warm that was admitted by the
// cache can run for its full warm budget, so callers that own the cache
// directory should release any gate the warm is blocked on first. A nil
// receiver returns immediately.
func (h *StreamHandler) waitForBackgroundSubtitleWarms() {
	if h == nil {
		return
	}
	h.subtitleWarmWG.Wait()
}

// bindSessionVirtualSource returns a copy of a virtual file bound to the
// provider-neutral source captured by the playback session.
func bindSessionVirtualSource(file *models.MediaFile, session *playback.Session) *models.MediaFile {
	if file == nil || session == nil || session.VirtualSourceURI == "" || !isVirtualPlaybackFile(file) {
		return file
	}
	bound := *file
	bound.FilePath = session.VirtualSourceURI
	bound.VirtualOwnerInstallationID = session.VirtualSourceOwnerInstallationID
	return &bound
}

// virtualSessionSourceBinder binds a live session to a provider-neutral virtual
// candidate. *playback.SessionManager implements it; the narrow interface keeps
// the serve-layer rotation commit optional for minimal/test managers, which
// simply keep the binding captured at plan time.
type virtualSessionSourceBinder interface {
	SetVirtualSource(sessionID, virtualURI string, ownerInstallationID int) error
}

// sourceCommittedNotifier publishes the effective version a transport just
// committed to, with its declared inventory, to the live session. The
// PlaybackHandler implements it; a minimal/test setup without a notifier skips
// the push while keeping the rotation commit itself.
type sourceCommittedNotifier interface {
	PublishSourceCommitted(ctx context.Context, sessionID string)
}

// sourceCommittedPublishBudget bounds the detached publish of a committed
// source so a slow catalog read cannot outlive the stream far beyond the media
// response.
const sourceCommittedPublishBudget = 5 * time.Second

// commitRotatedVirtualSessionSource rebinds a live session to the candidate a
// serve-layer rotation just resolved. The session id and account are unchanged:
// only the pinned anchor moves, so the client-visible session identity survives
// while later serves and replans bind to the live release instead of the dead
// pin. Best-effort — a manager without SetVirtualSource keeps its prior binding,
// and the current request already holds a valid resolved URL.
func (h *StreamHandler) commitRotatedVirtualSessionSource(ctx context.Context, sessionID string, resolved ResolvedVirtualMedia) {
	if h == nil || resolved.URI == "" {
		return
	}
	binder, ok := h.sessionMgr.(virtualSessionSourceBinder)
	if !ok {
		return
	}
	if err := binder.SetVirtualSource(sessionID, resolved.URI, resolved.OwnerID); err != nil {
		slog.WarnContext(ctx, "failed to rebind virtual session after candidate rotation",
			"component", "api", "session", sessionID, "virtual_uri", resolved.URI, "error", err)
		return
	}
	h.afterVirtualSourceCommit(ctx, sessionID, resolved)
}

// afterVirtualSourceCommit performs the bookkeeping shared by every binding
// move: associate the session with the committed release's catalog row, then
// publish the new effective version. The plan's MediaFileID still names the
// release the plan was built with, so notifiers that look sessions up by the
// new file id (marker and subtitle events) would otherwise miss this session.
// The manager keeps RequestedMediaFileID untouched. The publish is detached: it
// reads the catalog for the new release's declared inventory and must not delay
// the media response that is about to serve.
func (h *StreamHandler) afterVirtualSourceCommit(ctx context.Context, sessionID string, resolved ResolvedVirtualMedia) {
	h.associateEffectiveMediaFile(ctx, sessionID, resolved.URI)
	h.publishSourceCommittedAsync(ctx, sessionID)
}

// virtualSessionGenerationBinder is the generation-fenced binding surface.
// *playback.SessionManager implements it; the narrow interface keeps the
// handoff optional for minimal/test managers.
type virtualSessionGenerationBinder interface {
	VirtualSourceBinding(sessionID string) (playback.VirtualSourceBindingSnapshot, error)
	SetVirtualSourceIfBinding(sessionID string, expected playback.VirtualSourceBindingSnapshot, virtualURI string, ownerInstallationID int, effectiveMediaFileID int) (uint64, bool, error)
	VirtualSourceGeneration(sessionID string) (uint64, error)
	SetVirtualSourceIfGeneration(sessionID string, expectedGeneration uint64, virtualURI string, ownerInstallationID int) (uint64, bool, error)
}

func (h *StreamHandler) virtualSourceBinding(sessionID string) (playback.VirtualSourceBindingSnapshot, bool) {
	if binder, ok := h.sessionMgr.(virtualSessionGenerationBinder); ok {
		if snap, err := binder.VirtualSourceBinding(sessionID); err == nil {
			return snap, true
		}
	}
	return playback.VirtualSourceBindingSnapshot{}, false
}

// commitHandoffVirtualSessionSource rebinds a session to the cached copy of the
// release it is already pinned to. When a binding snapshot was captured it is
// enforced, so a handoff that resolved its replacement before a newer binding
// move is a benign no-op rather than a clobber. It commits the new virtual URI,
// owner, and effective media file ID atomically under the session manager lock.
// Without a generation-capable manager it falls back to the best-effort rotation
// commit.
func (h *StreamHandler) commitHandoffVirtualSessionSource(ctx context.Context, sessionID string, expected playback.VirtualSourceBindingSnapshot, expectedSet bool, resolved ResolvedVirtualMedia) bool {
	if h == nil || resolved.URI == "" {
		return false
	}
	var effectiveFileID int
	if pathResolver, ok := h.fileResolver.(interface {
		GetByPath(context.Context, string) (*models.MediaFile, error)
	}); ok {
		if row, err := pathResolver.GetByPath(ctx, resolved.URI); err == nil && row != nil && row.ID > 0 {
			effectiveFileID = row.ID
		}
	}
	if binder, ok := h.sessionMgr.(virtualSessionGenerationBinder); ok && expectedSet {
		if _, applied, err := binder.SetVirtualSourceIfBinding(sessionID, expected, resolved.URI, resolved.OwnerID, effectiveFileID); err != nil {
			slog.WarnContext(ctx, "cache handoff: failed to rebind virtual session",
				"component", "api", "session", sessionID, "virtual_uri", resolved.URI, "error", err)
			return false
		} else if !applied {
			return false
		}
		h.publishSourceCommittedAsync(ctx, sessionID)
		return true
	}
	h.commitRotatedVirtualSessionSource(ctx, sessionID, resolved)
	return true
}

// VirtualReleaseCacheStatusFunc reports whether a release already has a cached
// copy, looked up by its provider-neutral virtual URI. known=false means the
// provider could not be consulted and the caller must not change behavior.
type VirtualReleaseCacheStatusFunc func(ctx context.Context, virtualURI string, ownerInstallationID int) (cached bool, known bool)

// VirtualReleaseCacheFillFunc starts (or joins) a provider-side cache fill for
// one pinned release. The implementation resolves and requests the addon URL;
// the handler never fetches media. report is called with progress until the fill
// completes or fails and must remain safe to call after the fill returns.
type VirtualReleaseCacheFillFunc func(ctx context.Context, virtualURI string, ownerInstallationID int, report func(playback.DownloadProgressPayload)) error

// FillVirtualReleaseCache adapts the function to a nil-checkable value.
func (f VirtualReleaseCacheFillFunc) FillVirtualReleaseCache(ctx context.Context, virtualURI string, ownerInstallationID int, report func(playback.DownloadProgressPayload)) error {
	if f == nil {
		return errors.New("virtual release cache filler is not configured")
	}
	return f(ctx, virtualURI, ownerInstallationID, report)
}

// DownloadProgressPublisher delivers download.progress events to a live
// playback session. *playback.RealtimeHub implements it. Nil disables the push.
type DownloadProgressPublisher interface {
	PublishDownloadProgress(sessionID string, payload playback.DownloadProgressPayload) bool
}

const (
	// virtualCacheFillBudget bounds a detached play-triggered cache fill so a
	// provider that never finishes cannot hold the goroutine forever. A fill
	// that outlives it is reported failed; playback already continued on the
	// remote source.
	virtualCacheFillBudget = 30 * time.Minute
	// virtualCacheHandoffBudget bounds one release-confirmed handoff reaction.
	virtualCacheHandoffBudget = 30 * time.Second
)

// virtualCacheHandoffWaiter is a live session streaming an uncached pinned
// release. It is registered at serve start and removed when the transport ends,
// so a later release-confirmation only reaches sessions still playing.
type virtualCacheHandoffWaiter struct {
	regID      uint64
	sessionID  string
	fileID     int
	releaseKey string
	ownerID    int
	binding    playback.VirtualSourceBindingSnapshot
	pinnedID   string
}

// virtualCacheHandoffRegistry indexes waiters by normalized release key. The
// zero value is usable; the map is created lazily. Each registration receives a
// unique registration ID so overlapping transports for the same session/release
// do not overwrite each other or delete a newer request's waiter on cleanup.
type virtualCacheHandoffRegistry struct {
	mu        sync.Mutex
	byRelease map[string]map[uint64]virtualCacheHandoffWaiter
	nextID    uint64
}

// register adds a waiter and returns a function that idempotently removes it.
// A waiter with no session or release key is not registered and its release
// function is a no-op.
func (r *virtualCacheHandoffRegistry) register(w virtualCacheHandoffWaiter) func() {
	if r == nil || w.sessionID == "" || w.releaseKey == "" {
		return func() {}
	}
	r.mu.Lock()
	if r.byRelease == nil {
		r.byRelease = make(map[string]map[uint64]virtualCacheHandoffWaiter)
	}
	r.nextID++
	id := r.nextID
	w.regID = id
	sessions := r.byRelease[w.releaseKey]
	if sessions == nil {
		sessions = make(map[uint64]virtualCacheHandoffWaiter)
		r.byRelease[w.releaseKey] = sessions
	}
	sessions[id] = w
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			current := r.byRelease[w.releaseKey]
			if current == nil {
				return
			}
			delete(current, id)
			if len(current) == 0 {
				delete(r.byRelease, w.releaseKey)
			}
		})
	}
}

// forRelease returns the waiters for a release key. The caller must not mutate
// the returned slice.
func (r *virtualCacheHandoffRegistry) forRelease(releaseKey string) []virtualCacheHandoffWaiter {
	if r == nil || releaseKey == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	sessions := r.byRelease[releaseKey]
	if len(sessions) == 0 {
		return nil
	}
	out := make([]virtualCacheHandoffWaiter, 0, len(sessions))
	for _, w := range sessions {
		out = append(out, w)
	}
	return out
}

// virtualHandoffReleaseKey returns the normalized release identity that keys the
// cache-handoff registry for a serving session. A row without a persisted
// release name cannot be matched to a provider completion signal, so it yields
// "" and no handoff is attempted.
func virtualHandoffReleaseKey(file *models.MediaFile, resolved ResolvedVirtualMedia) string {
	if file != nil {
		if key := altmount.ReleaseKey(file.ProviderReleaseName); key != "" {
			return key
		}
	}
	return altmount.ReleaseKey(resolved.ProviderReleaseName)
}

// sameVirtualReleaseCandidate reports whether a resolved candidate id names the
// same release the session is pinned to. An empty id on either side cannot be
// compared, so it is not a match: a neutral row (no concrete pin) or a resolver
// that reports no candidate id must not hand off, because a force-refresh
// resolve ranks candidates anew and could substitute a ranked candidate from
// another release under the session binding.
func sameVirtualReleaseCandidate(pinnedID, resolvedID string) bool {
	return pinnedID != "" && resolvedID != "" && pinnedID == resolvedID
}

// handoffVirtualSessionToCached re-resolves the exact release a session is
// pinned to and rebinds the session to the cached copy. cachedConfirmed lets the
// release-confirmed path skip the status probe it already answered. A resolve
// that names a different release is refused so a cache handoff can never
// silently switch releases, and a resolve that cannot be tied to a concrete
// pinned candidate id is refused rather than trusted; a stale binding
// generation is a benign no-op. The returned cleanup must be released by the
// caller when non-nil.
func (h *StreamHandler) handoffVirtualSessionToCached(ctx context.Context, session *playback.Session, file *models.MediaFile, cachedConfirmed bool) (ResolvedVirtualMedia, func(), bool, error) {
	snap, snapSet := h.virtualSourceBinding(session.ID)
	return h.handoffVirtualSessionToCachedWithSnapshot(ctx, session, file, snap, snapSet, cachedConfirmed)
}

func (h *StreamHandler) handoffVirtualSessionToCachedWithSnapshot(ctx context.Context, session *playback.Session, file *models.MediaFile, snap playback.VirtualSourceBindingSnapshot, snapSet bool, cachedConfirmed bool) (ResolvedVirtualMedia, func(), bool, error) {
	if h == nil || session == nil || file == nil || !isVirtualPlaybackFile(file) || !hasVirtualMediaResolver(h) {
		return ResolvedVirtualMedia{}, nil, false, nil
	}
	if !cachedConfirmed {
		if h.VirtualReleaseCacheStatus == nil {
			// Without a cache probe there is no way to tell the cached copy
			// from the remote one, so today's behavior is preserved.
			return ResolvedVirtualMedia{}, nil, false, nil
		}
		if cached, known := h.VirtualReleaseCacheStatus(ctx, file.FilePath, effectiveVirtualOwner(0, file.VirtualOwnerInstallationID)); !known || !cached {
			return ResolvedVirtualMedia{}, nil, false, nil
		}
	}
	pinnedID := virtualResultCandidateID(file.FilePath)
	// forceRefresh lists afresh so the provider reports its now-cached URL; a
	// plain re-resolve could serve the stored (remote) URL again.
	resolved, cleanup, err := h.resolveVirtualInputURIExcluding(ctx, file, session.UserID, session.ProfileID, true, nil, false)
	if err != nil {
		return ResolvedVirtualMedia{}, nil, false, err
	}
	if !sameVirtualReleaseCandidate(pinnedID, resolved.CandidateID) {
		// An empty resolved candidate names no release at all, so it can never
		// be carried: refuse it explicitly before the identity checks. A pin
		// (pinnedID) is equally unverifiable, and this refusal must not be
		// reachable via a successful IdentityRematched or durable-identity
		// match, which prove a release only when a candidate was returned.
		if pinnedID == "" || resolved.CandidateID == "" {
			if cleanup != nil {
				cleanup()
			}
			return ResolvedVirtualMedia{}, nil, false, fmt.Errorf("cache handoff cannot verify the pinned release: pinned candidate %q, resolved candidate %q; refusing an unverifiable handoff", pinnedID, resolved.CandidateID)
		}
		// A provider that renumbers its per-listing result ids names the same
		// release under a new concrete candidate. That is not a release swap:
		// carry the session pin forward and rebind the session to the pinned
		// candidate, never the churned listing token. A resolve that shares no
		// durable identity with the pin is still refused so a cache handoff can
		// never silently switch releases.
		if !resolved.IdentityRematched && !resolvedMatchesPersistedIdentity(resolved, file) {
			if cleanup != nil {
				cleanup()
			}
			return ResolvedVirtualMedia{}, nil, false, fmt.Errorf("cache handoff resolved candidate %q for pinned candidate %q; refusing a release swap", resolved.CandidateID, pinnedID)
		}
		// Adopt against the session pin: keep the candidate the session is
		// actually serving. The cached bytes are the same release, so the
		// provider-neutral pin remains the binding identity.
		resolved.URI = file.FilePath
		resolved.CandidateID = pinnedID
	}
	if !h.commitHandoffVirtualSessionSource(ctx, session.ID, snap, snapSet, resolved) {
		if cleanup != nil {
			cleanup()
		}
		return ResolvedVirtualMedia{}, nil, false, nil
	}
	return resolved, cleanup, true, nil
}

// HandleVirtualReleaseConfirmed reacts to AltMount first reporting a release
// completed. It finds live sessions still streaming that release and hands each
// off to the cached copy, preserving the session's virtual pin and stream
// timeline. It matches the altmount.ReleaseConfirmationObserver signature so a
// monitor can be wired to it directly. Best-effort and bounded: an unknown
// release, a session that already moved on, or a resolve failure is logged and
// never disrupts playback.
//
// The observer contract requires a quick, non-blocking callback (see
// altmount.ReleaseConfirmationObserver): the caller is a classification or
// refresh on the serve path. Every handoff resolves the cached copy under a
// network budget, so the work is dispatched to its own goroutine and the
// callback returns immediately.
func (h *StreamHandler) HandleVirtualReleaseConfirmed(releaseKey string) {
	if h == nil || strings.TrimSpace(releaseKey) == "" {
		return
	}
	go h.handoffConfirmedRelease(releaseKey)
}

// handoffConfirmedRelease performs the bounded handoff work for one confirmed
// release. It owns its context so the caller's return cannot cancel it.
func (h *StreamHandler) handoffConfirmedRelease(releaseKey string) {
	ctx, cancel := context.WithTimeout(context.Background(), virtualCacheHandoffBudget)
	defer cancel()
	for _, waiter := range h.cacheWaiters.forRelease(releaseKey) {
		session, err := h.sessionMgr.GetSession(waiter.sessionID)
		if err != nil || session == nil {
			continue
		}
		// Bound to the release that registered the waiter: if the session has
		// moved to another candidate, was replaced, or replanned, its binding
		// generation or identity will have changed. Refuse the handoff and do
		// not attribute completion of releaseKey to this session.
		currentBinding, hasBinder := h.virtualSourceBinding(waiter.sessionID)
		if hasBinder {
			if currentBinding.Generation != waiter.binding.Generation ||
				strings.TrimSpace(currentBinding.VirtualURI) != strings.TrimSpace(waiter.binding.VirtualURI) ||
				currentBinding.OwnerInstallationID != waiter.binding.OwnerInstallationID ||
				(waiter.binding.EffectiveMediaFileID > 0 && currentBinding.EffectiveMediaFileID != waiter.binding.EffectiveMediaFileID) {
				continue
			}
		}
		file, err := h.fileResolver.GetByID(ctx, waiter.fileID)
		if err != nil || file == nil {
			continue
		}
		file = bindSessionVirtualSource(file, session)
		// Ensure the file's current pinned candidate still matches the waiter.
		if waiter.pinnedID != "" && virtualResultCandidateID(file.FilePath) != waiter.pinnedID {
			continue
		}
		resolved, cleanup, applied, handoffErr := h.handoffVirtualSessionToCachedWithSnapshot(ctx, session, file, waiter.binding, hasBinder, true)
		if handoffErr != nil {
			slog.WarnContext(ctx, "cache handoff failed for confirmed release",
				"component", "api", "session", waiter.sessionID, "file_id", waiter.fileID,
				"release_key", releaseKey, "status", "handoff_failed",
				"error", logredact.SanitizeURLError(handoffErr))
			continue
		}
		if applied {
			if cleanup != nil {
				cleanup()
			}
			h.reportDownloadProgress(waiter.sessionID, waiter.fileID, releaseKey, playback.DownloadProgressPayload{
				State:   playback.DownloadProgressStateCompleted,
				Message: "cached copy is now servable",
			})
			slog.InfoContext(ctx, "cache handoff rebound session to the cached copy",
				"component", "api", "session", waiter.sessionID, "file_id", waiter.fileID,
				"release_key", releaseKey, "status", "handed_off", "virtual_uri", resolved.URI)
		}
	}
}

// beginVirtualCacheHandoff registers a session streaming a pinned release for a
// later release-confirmation handoff and, when a filler is wired, asks the
// provider to start a cache fill while streaming download.progress. It returns a
// release function the caller defers so the registration is dropped when the
// transport ends. The fill is detached and bounded: the caller keeps streaming
// the remote source and never blocks on it, and a fill that cannot start is
// reported failed rather than hanging.
func (h *StreamHandler) beginVirtualCacheHandoff(ctx context.Context, session *playback.Session, file *models.MediaFile, resolved ResolvedVirtualMedia) func() {
	if h == nil || session == nil || file == nil {
		return func() {}
	}
	releaseKey := virtualHandoffReleaseKey(file, resolved)
	if releaseKey == "" {
		return func() {}
	}
	ownerID := effectiveVirtualOwner(resolved.OwnerID, file.VirtualOwnerInstallationID)
	snap, _ := h.virtualSourceBinding(session.ID)
	release := h.cacheWaiters.register(virtualCacheHandoffWaiter{
		sessionID:  session.ID,
		fileID:     file.ID,
		releaseKey: releaseKey,
		ownerID:    ownerID,
		binding:    snap,
		pinnedID:   virtualResultCandidateID(file.FilePath),
	})
	if h.VirtualReleaseCacheFiller == nil {
		return release
	}
	filler := h.VirtualReleaseCacheFiller
	sessionID, fileID := session.ID, file.ID
	virtualURI := resolved.URI
	go func() {
		fillCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), virtualCacheFillBudget)
		defer cancel()
		err := filler.FillVirtualReleaseCache(fillCtx, virtualURI, ownerID, func(progress playback.DownloadProgressPayload) {
			h.reportDownloadProgress(sessionID, fileID, releaseKey, progress)
		})
		if err != nil {
			h.reportDownloadProgress(sessionID, fileID, releaseKey, playback.DownloadProgressPayload{
				State:   playback.DownloadProgressStateFailed,
				Message: "cache fill could not start; continuing from the provider",
			})
		}
	}()
	return release
}

// reportDownloadProgress sends one download.progress event, best-effort.
func (h *StreamHandler) reportDownloadProgress(sessionID string, fileID int, releaseKey string, progress playback.DownloadProgressPayload) {
	if h == nil || h.DownloadProgress == nil || sessionID == "" {
		return
	}
	progress.FileID = fileID
	if progress.ReleaseID == "" {
		progress.ReleaseID = releaseKey
	}
	h.DownloadProgress.PublishDownloadProgress(sessionID, progress)
}

// associateEffectiveMediaFile points the live session at the catalog row of the
// candidate a rotation just bound, preserving its requested file selection.
func (h *StreamHandler) associateEffectiveMediaFile(ctx context.Context, sessionID, candidateURI string) {
	if h == nil || h.sessionMgr == nil || strings.TrimSpace(candidateURI) == "" {
		return
	}
	pathResolver, ok := h.fileResolver.(interface {
		GetByPath(context.Context, string) (*models.MediaFile, error)
	})
	if !ok {
		return
	}
	row, err := pathResolver.GetByPath(ctx, candidateURI)
	if err != nil || row == nil || row.ID <= 0 {
		return
	}
	if err := h.sessionMgr.SetEffectiveMediaFileID(sessionID, row.ID); err != nil {
		slog.WarnContext(ctx, "failed to associate virtual session with the rotated release row",
			"component", "api", "session", sessionID, "virtual_uri", candidateURI, "file_id", row.ID, "error", err)
	}
}

// publishSourceCommittedAsync delivers the committed-source event on a detached
// five-second context. A missing notifier is a no-op.
func (h *StreamHandler) publishSourceCommittedAsync(ctx context.Context, sessionID string) {
	if h == nil || h.SourceCommittedNotifier == nil {
		return
	}
	notifier := h.SourceCommittedNotifier
	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sourceCommittedPublishBudget)
	go func() {
		defer cancel()
		notifier.PublishSourceCommitted(bgCtx, sessionID)
	}()
}

// bindSessionVirtualSourceWithTracks binds the session's virtual source and
// prefers the subtitle evidence captured at plan time. The catalog row is
// mutable: candidate rotation re-probes it and can replace its subtitle
// tracks after this session planned against a specific release. Pinned
// subtitle URLs name plan-time ordinals/stream indices, so the extraction
// must use the evidence the plan promised, not whatever the row holds now.
//
// Carried evidence is only trustworthy while it still belongs to the candidate
// actually being served. A serve-layer rotation can move the session binding
// without moving the catalog row's subtitle inventory, and the planner can
// replan onto a different file; in both cases the carried inventory describes
// the previous release, so applying it would silently serve the wrong tracks.
// The evidence URI records the candidate it was captured for: when the bound
// file no longer names that candidate, the evidence is discarded and the bound
// file's own catalog tracks are served instead.
func bindSessionVirtualSourceWithTracks(ctx context.Context, file *models.MediaFile, session *playback.Session, resolver FilePathResolver) *models.MediaFile {
	bound := bindSessionVirtualSource(file, session)
	if bound == nil || !isVirtualPlaybackFile(bound) {
		return bound
	}

	// The evidence-set flag, not slice length, is the signal. A candidate probed
	// with no subtitle tracks is captured-empty evidence, and it is still
	// authoritative: the plan promised this release has none, so serving the
	// row's later-populated inventory would serve a track the client never
	// selected. Absent evidence (the flag unset) is the only case with nothing
	// to apply, and the only case the live row may speak for.
	//
	// The carried inventory is authoritative for the whole session, verified
	// row or not. This same bind feeds the serve path (the transport muxes the
	// audio stream the session selected) and the inventory endpoint, so both
	// must read the one list the plan committed to: swapping in the row's tracks
	// here would publish a selection_index/ordinal space the transport was never
	// muxed against, and a fast start would play silence. When a deferred probe
	// commits the release's real tracks, the truth reaches the session itself
	// (the probe worker replaces this carried inventory and advances the source
	// revision) rather than being swapped in on read; that keeps the menu and
	// the transport coherent and breaks the client's 304. Evidence for a
	// different candidate is still rejected outright.
	evidenceSet := session.VirtualSubtitleEvidenceSet
	evidenceRejected := evidenceSet && !virtualEvidenceMatchesBoundFile(ctx, bound, session, resolver)
	if evidenceRejected {
		slog.WarnContext(ctx, "virtual session track evidence belongs to a different candidate; using the bound file's tracks",
			"component", "api",
			"session", session.ID,
			"file_id", file.ID,
			"evidence_uri", session.VirtualSubtitleEvidenceURI,
			"file_path", bound.FilePath)
	}
	if evidenceSet && !evidenceRejected {
		boundCopy := *bound
		boundCopy.SubtitleTracks = session.VirtualSubtitleTracks
		boundCopy.ExternalSubtitles = session.VirtualExternalSubtitles
		// Audio evidence travels with the subtitle evidence. The catalog row is
		// mutable and the bound file may be a placeholder for a release no row
		// names, so the audio inventory captured at plan time is as
		// authoritative as the subtitle inventory; serving the bound file's own
		// audio instead would drop a menu the plan already promised. Captured-
		// empty audio is genuine evidence, so an empty slice is applied too, not
		// treated as absent.
		boundCopy.AudioTracks = session.VirtualAudioTracks
		return &boundCopy
	}

	// No probe-authoritative evidence applies. When the bound file carries no
	// committed probe stamp — the placeholder built for a release no catalog row
	// names, or a virtual row the deferred write did not reach — the plan-time
	// audio inventory the session captured is the only menu the plan promised.
	// Apply it, but only while it still belongs to the bound candidate: a
	// rotation onto a different release must not carry the previous release's
	// audio, and a probed row must never be downgraded from verified to declared.
	if bound.ProbeUpdatedAt == nil && session.VirtualAudioTracks != nil &&
		virtualEvidenceMatchesBoundFile(ctx, bound, session, resolver) {
		boundCopy := *bound
		boundCopy.AudioTracks = session.VirtualAudioTracks
		return &boundCopy
	}

	// No applicable session evidence (reconstructed session, or evidence for a
	// previous candidate): fall back to the live candidate row when the bound
	// file only carries provider-declared placeholders, mirroring the
	// historical behavior.
	if resolver == nil || hasUsableSubtitleTracks(bound) {
		return bound
	}

	var candidate *models.MediaFile
	if session.MediaFileID > 0 && session.MediaFileID != file.ID {
		candidate, _ = resolver.GetByID(ctx, session.MediaFileID)
	}
	if (candidate == nil || !hasUsableSubtitleTracks(candidate)) && session.VirtualSourceURI != "" {
		if pathResolver, ok := resolver.(interface {
			GetByPath(context.Context, string) (*models.MediaFile, error)
		}); ok {
			candidate, _ = pathResolver.GetByPath(ctx, session.VirtualSourceURI)
		}
	}
	if candidate != nil && hasUsableSubtitleTracks(candidate) {
		if evidenceRejected && (bound.ID <= 0 || candidate.ID != bound.ID) {
			// The session carried evidence for a specific catalog row and the
			// bound row is not that row: the evidence was rejected as a
			// cross-row mismatch. The URI-only fallback must not reauthorize the
			// rejected row (or any other row that merely shares the candidate
			// URI) — doing so is the exact bypass this guard exists to stop.
			// Only the bound row's own declared inventory may speak for it; an
			// unknown bound row (id 0) cannot be confirmed and fails closed too.
			slog.WarnContext(ctx, "virtual session fallback refused a rejected evidence row; serving the bound row's own inventory",
				"component", "api",
				"session", session.ID,
				"file_id", file.ID,
				"evidence_file_id", session.VirtualSubtitleEvidenceFileID,
				"candidate_file_id", candidate.ID,
				"file_path", bound.FilePath)
			return bound
		}
		boundCopy := *bound
		boundCopy.SubtitleTracks = candidate.SubtitleTracks
		if len(boundCopy.ExternalSubtitles) == 0 {
			boundCopy.ExternalSubtitles = candidate.ExternalSubtitles
		}
		return &boundCopy
	}

	return bound
}

// virtualEvidenceMatchesBoundFile reports whether the session's carried virtual
// subtitle evidence was captured for the candidate the bound file names. The
// evidence row id, when known, is authoritative; the candidate URI is a second
// anchor that always applies. Sessions created before the provenance URI or row
// id existed fall back to the plan-time binding.
//
// The URI alone is ambiguous: duplicate catalog rows for one release share
// candidate URIs and neutral keys, so a serve-layer rotation can move the
// binding to a sibling row that names the same candidate while the carried
// inventory still describes the row the plan captured it from. The evidence
// row id disambiguates them: evidence captured from row A must not be applied
// to sibling row B, and an unknown bound row (id 0) carries no identity to
// confirm. A zero evidence row id (a legacy or reconstructed session) falls
// back to URI-only matching, preserving the historical behavior.
//
// One further case is admitted: the canonical collection variant. A
// result-pinned session can carry evidence anchored at the release's
// provider-neutral collection path while its bound row names the concrete
// ?result= sibling actually served. Those two paths differ only by the result
// id, so sameVirtualCandidate rejects them even though they can name one
// release. That pairing is accepted only when the neutral keys agree and the
// two rows' durable provider identities prove a shared release (see
// canonicalCollectionEvidenceMatchesBoundRow); empty or conflicting tiers fail
// closed.
func virtualEvidenceMatchesBoundFile(ctx context.Context, bound *models.MediaFile, session *playback.Session, fileResolver FilePathResolver) bool {
	if session == nil || bound == nil {
		return false
	}
	// The candidate URI applies even on a matching row: a row adopted in place
	// by a provider rotation rewrites its file path, so a URI that no longer
	// names the evidence's candidate means the evidence is stale. Legacy
	// sessions predate the evidence URI field; they still matched on the
	// session's virtual source URI.
	evidenceURI := strings.TrimSpace(session.VirtualSubtitleEvidenceURI)
	if evidenceURI == "" {
		evidenceURI = strings.TrimSpace(session.VirtualSourceURI)
	}
	if evidenceURI == "" {
		return false
	}
	if virtualEvidenceAnchorsBoundRow(bound, session, evidenceURI) {
		return true
	}
	return canonicalCollectionEvidenceMatchesBoundRow(ctx, bound, session, fileResolver, evidenceURI)
}

// virtualEvidenceAnchorsBoundRow is the historical provenance match: the bound
// row must be the exact row the evidence was captured from and the evidence URI
// must still name that row's candidate. A known evidence-row identity is
// enforced even when the URI agrees, so an unknown bound row (id 0) carries no
// identity to confirm and a sibling row that merely shares the candidate URI
// never inherits the evidence.
func virtualEvidenceAnchorsBoundRow(bound *models.MediaFile, session *playback.Session, evidenceURI string) bool {
	if evidenceID := session.VirtualSubtitleEvidenceFileID; evidenceID > 0 && bound.ID != evidenceID {
		return false
	}
	return sameVirtualCandidate(evidenceURI, bound.FilePath)
}

// canonicalCollectionEvidenceMatchesBoundRow reports whether evidence anchored
// at a provider-neutral collection path describes the same release as the
// result-pinned row the session is bound to. The collection variant materializes
// a neutral row for a release while the transport can pin a concrete ?result=
// sibling of it, so the session carries the neutral anchor and the serve path
// must still recognize the release rather than rejecting the verified tracks
// for the whole session.
//
// The gate is deliberately narrow, because the neutral key alone does not prove
// a release: every candidate of one content shares the base path and differs
// only in the result id, so two genuinely different releases of the same episode
// share a neutral key. Recognition therefore requires all three of: the evidence
// names the neutral path, the bound row names a concrete result-pinned sibling
// with the same neutral key, and the two rows' durable provider identities share
// a non-empty tier. Empty or conflicting tiers fail closed, so an identity-less
// or cross-release collection row is never accepted.
func canonicalCollectionEvidenceMatchesBoundRow(ctx context.Context, bound *models.MediaFile, session *playback.Session, fileResolver FilePathResolver, evidenceURI string) bool {
	boundPath := strings.TrimSpace(bound.FilePath)
	if virtualResultCandidateID(evidenceURI) != "" || virtualResultCandidateID(boundPath) == "" {
		return false
	}
	if virtualPlaybackNeutralKey(evidenceURI) != virtualPlaybackNeutralKey(boundPath) {
		return false
	}
	evidence := canonicalEvidenceReleaseRow(ctx, session, fileResolver, evidenceURI)
	if evidence == nil || evidence.ID == bound.ID {
		// No distinct collection row to prove the release against: comparing a
		// row with itself is not proof, so an unresolvable or self-named anchor
		// fails closed.
		return false
	}
	return boundRowSharesRelease(bound, evidence)
}

// canonicalEvidenceReleaseRow resolves the catalog row that owns the session's
// neutral evidence anchor. The evidence row id is tried first and is trusted
// only when it still names the neutral path; a stale id (or a legacy session
// carrying none) falls back to an exact path lookup. A resolver without
// GetByPath cannot answer, and the caller then fails closed rather than reading
// an identity off the bound row.
func canonicalEvidenceReleaseRow(ctx context.Context, session *playback.Session, fileResolver FilePathResolver, evidenceURI string) *models.MediaFile {
	if fileResolver == nil || evidenceURI == "" {
		return nil
	}
	if evidenceID := session.VirtualSubtitleEvidenceFileID; evidenceID > 0 {
		if row, err := fileResolver.GetByID(ctx, evidenceID); err == nil && row != nil && row.ID == evidenceID {
			path := strings.TrimSpace(row.FilePath)
			if virtualResultCandidateID(path) == "" && virtualPlaybackNeutralKey(path) == virtualPlaybackNeutralKey(evidenceURI) {
				return row
			}
		}
	}
	pathResolver, ok := fileResolver.(interface {
		GetByPath(context.Context, string) (*models.MediaFile, error)
	})
	if !ok {
		return nil
	}
	if row, err := pathResolver.GetByPath(ctx, evidenceURI); err == nil && row != nil && row.ID > 0 {
		return row
	}
	return nil
}

// boundRowSharesRelease reports whether a result-pinned bound row and the row a
// neutral evidence anchor names describe one release, judged by their durable
// provider identity. The bound row is the viewer's persisted selection and the
// evidence row is the fresh claim, so the asymmetric SharedTier is used: a
// stronger stored tier the evidence cannot corroborate is a veto, and an
// identity whose tiers are all empty never matches.
func boundRowSharesRelease(bound, evidence *models.MediaFile) bool {
	if bound == nil || evidence == nil {
		return false
	}
	boundTiers := resolver.NewPersistedIdentityTiers(bound.ProviderVideoHash, bound.ProviderGUID, bound.ProviderReleaseName, bound.ProviderReleaseSize)
	evidenceTiers := resolver.NewPersistedIdentityTiers(evidence.ProviderVideoHash, evidence.ProviderGUID, evidence.ProviderReleaseName, evidence.ProviderReleaseSize)
	_, shared := boundTiers.SharedTier(evidenceTiers)
	return shared
}

// sameVirtualCandidate reports whether two provider-neutral virtual paths name
// the same candidate. Exact equality is sufficient across the codebase; the
// result-id comparison additionally tolerates a URI whose non-result query
// parameters were re-encoded (`withVirtualResultKey` preserves the rest).
func sameVirtualCandidate(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == b {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	aID, bID := virtualResultCandidateID(a), virtualResultCandidateID(b)
	if aID == "" || bID == "" || aID != bID {
		return false
	}
	return sameVirtualProfileScope(a, b)
}

// sameVirtualProfileScope reports whether two virtual paths share a
// provider-neutral profile scope: the same scheme/host/path and quality profile
// with the concrete per-listing "result=" pick removed. Scope equality is not
// release identity — a provider can mint several concrete result picks
// (siblings) under one profile — so it must never be used alone to conclude two
// paths are the same release. Callers that need release identity pair it with a
// concrete result-id equality (sameVirtualCandidate) or with the session's
// pinned, probe-authoritative evidence.
func sameVirtualProfileScope(a, b string) bool {
	return virtualPlaybackNeutralKey(a) == virtualPlaybackNeutralKey(b)
}

// hasUsableSubtitleTracks reports whether a file carries embedded subtitle
// tracks with real codec evidence. Provider-declared language placeholders
// (Index 0, no Codec, no ContainerTrackID) are not usable for extraction:
// the stream handler would pick the wrong output format and ffmpeg would
// fail against the real provider stream.
func hasUsableSubtitleTracks(file *models.MediaFile) bool {
	if file == nil {
		return false
	}
	for _, track := range file.SubtitleTracks {
		if strings.TrimSpace(track.Codec) != "" {
			return true
		}
	}
	return false
}

func resolvedVirtualCandidatePath(resolved ResolvedVirtualMedia) string {
	uri := strings.TrimSpace(resolved.URI)
	id := virtualResultCandidateID(uri)
	if !strings.HasPrefix(uri, "virtual://") || id == "" || resolved.CandidateID == "" || resolved.CandidateID != id {
		return ""
	}
	return uri
}

// Disposition vocabulary for the direct-play proxy verdict. Each names one
// first-attempt outcome; the verdict log records exactly one of them. Named
// constants (not inline literals) so the closed set reads in one place and
// stays out of the repeated-literal lint.
const (
	virtualProxyDispositionClientCanceled = "client_canceled"
	virtualProxyDispositionRelayNotFound  = "relay_not_found"
	virtualProxyDispositionRelayTemporary = "relay_temporary"
	virtualProxyDispositionCommitted      = "committed"
	virtualProxyDispositionFailedOpen     = "failed_open"

	virtualStreamUnavailableCode    = "virtual_stream_unavailable"
	virtualStreamUnavailableMessage = "Failed to stream virtual media source"
)

// Attribute keys for the proxy verdict line. The verdict is the single site
// that emits this shape, so the keys live with it rather than as package
// shared constants; logVirtualStreamFailure in playback.go keeps its own
// inline keys (two occurrences stay below the repeated-literal lint).
const (
	virtualVerdictLogKeySession         = "session"
	virtualVerdictLogKeyPlaybackSession = "playback_session_id"
)

// virtualProxyFailureStatus maps the first-attempt proxy failure to the
// response written when nothing has been committed. A client cancellation is an
// abandoned request, not a dependency failure: the upstream fetch runs on the
// request context, so the viewer navigating away already aborted it. Answering
// 502 there records a server-error and logs a virtual_stream_unavailable ERROR
// for a request whose client never saw it. A 4xx request-timeout keeps the
// response honest for a half-closed client and keeps the outcome in the client
// error class instead of counting a server error.
func virtualProxyFailureStatus(ctx context.Context, proxyErr error) (status int, code, message string) {
	if isClientCancellation(ctx, proxyErr) {
		return http.StatusRequestTimeout, virtualProxyDispositionClientCanceled, "Client closed the request"
	}
	return http.StatusBadGateway, virtualStreamUnavailableCode, virtualStreamUnavailableMessage
}

// virtualProxyDisposition names how a failed direct-play proxy attempt ended
// before any heal or rotation runs: the first-attempt verdict the verdict log
// records. Client cancellation is the viewer's own doing; relay_not_found is
// the lapsed registration the same-release retry covers; relay_temporary is
// the upstream 5xx the rotation treats as availability, not a dead release;
// committed means the response had committed headers when the error arrived
// (headers written, not necessarily media bytes — mid-stream copy failures
// panic with ErrAbortHandler instead of reaching this verdict); failed_open
// is everything else. The disposition is a log value only and never steers
// recovery: the branches below keep their own conditions.
//
// The returned values are the verdict's contract vocabulary, pinned by
// TestVirtualProxyDisposition.
func virtualProxyDisposition(ctx context.Context, proxyErr error, statusCode int, relayNotFound, relayTemporary bool) string {
	if isClientCancellation(ctx, proxyErr) {
		return virtualProxyDispositionClientCanceled
	}
	if relayNotFound {
		return virtualProxyDispositionRelayNotFound
	}
	if relayTemporary {
		return virtualProxyDispositionRelayTemporary
	}
	if statusCode != 0 {
		return virtualProxyDispositionCommitted
	}
	return virtualProxyDispositionFailedOpen
}

// logVirtualRotationOutcome records the serve-layer rotation's recovery
// outcome: which candidate the retry resolved and whether that attempt then
// delivered bytes. The first-attempt proxy verdict (logVirtualProxyVerdict)
// names the failure; this line names the recovery, so a later
// virtual_stream_unavailable is a join, not forensics. Provider URLs never
// appear here: only provider-neutral virtual URIs. A rotation whose sibling
// also failed logs served=false with that attempt's error; a guard refusal
// logs served=false with the refusal reason. Client cancellation stays
// debug; a genuine recovery outcome keeps its level (INFO on delivery,
// WARN otherwise) so it stays visible in the operational log.
func logVirtualRotationOutcome(ctx context.Context, sessionID string, file *models.MediaFile, failedCandidateID string, refreshed ResolvedVirtualMedia, refreshErr error, served bool, serveErr error) {
	pinnedID := virtualCandidateID(file)
	attrs := []any{
		logComponentKey, "api", //nolint:goconst // log attribute key/value, kept inline for readability.
		virtualVerdictLogKeySession, sessionID,
		virtualVerdictLogKeyPlaybackSession, sessionID,
		"pinned_candidate_id", pinnedID,
		"failed_candidate_id", failedCandidateID,
		"refreshed_candidate_id", refreshed.CandidateID,
		"refreshed_uri", refreshed.URI,
		"served", served,
	}
	if file != nil {
		attrs = append(attrs, "file_id", file.ID)
	}
	switch {
	case refreshErr != nil:
		attrs = append(attrs, "status", "rotation_resolve_failed", "error", logredact.SanitizeURLError(refreshErr))
		slog.WarnContext(ctx, "virtual stream rotation outcome", attrs...)
	case !served:
		if serveErr != nil {
			attrs = append(attrs, "status", "rotation_sibling_failed", "error", logredact.SanitizeURLError(serveErr))
		} else {
			attrs = append(attrs, "status", "rotation_guard_refused")
		}
		slog.WarnContext(ctx, "virtual stream rotation outcome", attrs...)
	default:
		attrs = append(attrs, "status", "rotation_served")
		slog.InfoContext(ctx, "virtual stream rotation outcome", attrs...)
	}
}

// logVirtualProxyVerdict records the first-attempt serve verdict of a failed
// direct-play proxy: the session, the pinned candidate, the candidate the
// attempt served, and the disposition above. The delivered candidate id is the
// ?result= identity on the path the transport actually served; at verdict time
// no heal or rotation has run yet, so it agrees with the pin on the common
// path and is empty when the attempt failed before serving one. Provider URLs
// never appear here: only provider-neutral virtual URIs. Client cancellation
// logs at debug; a genuine failure keeps its WARN so it stays visible in the
// operational log. The verdict is first-attempt classification, not the final
// recovery outcome: a later heal or rotation can still serve the request (see
// logVirtualRotationOutcome for that half of the join).
//
// The attribute keys are the verdict's join contract (session,
// playback_session_id, candidate ids, disposition), pinned by
// TestLogVirtualProxyVerdictCarriesJoinKeys.
func logVirtualProxyVerdict(ctx context.Context, sessionID string, file *models.MediaFile, deliveredPath, disposition string, proxyErr error) {
	pinnedID := virtualCandidateID(file)
	deliveredID := virtualResultCandidateID(deliveredPath)
	attrs := []any{
		logComponentKey, "api", //nolint:goconst // log attribute key/value, kept inline for readability.
		virtualVerdictLogKeySession, sessionID,
		virtualVerdictLogKeyPlaybackSession, sessionID,
		"pinned_candidate_id", pinnedID,
		"delivered_candidate_id", deliveredID,
		"disposition", disposition,
	}
	if file != nil {
		attrs = append(attrs, "file_id", file.ID)
	}
	if proxyErr != nil {
		attrs = append(attrs, "error", logredact.SanitizeURLError(proxyErr))
	}
	if disposition == virtualProxyDispositionClientCanceled {
		slog.DebugContext(ctx, "virtual stream proxy verdict", attrs...)
		return
	}
	slog.WarnContext(ctx, "virtual stream proxy verdict", attrs...)
}

// virtualCandidateID is the ?result= identity bound to a catalog row's
// virtual URI, or "" when the row carries no explicit pick. The log field
// must never be a fabricated join key, so absence stays empty. A nil file
// has no identity either.
func virtualCandidateID(file *models.MediaFile) string {
	if file == nil {
		return ""
	}
	return virtualResultCandidateID(file.FilePath)
}

func hasVirtualMediaResolver(h *StreamHandler) bool {
	return h != nil && (h.VirtualMediaResolver != nil || h.VirtualMediaDetailedResolver != nil || h.VirtualMediaRefreshResolver != nil)
}

func (h *StreamHandler) resolveVirtualInputURI(
	ctx context.Context,
	file *models.MediaFile,
	userID int,
	profileID string,
	forceRefresh bool,
) (ResolvedVirtualMedia, func(), error) {
	// A plain re-resolve indicts nothing, so it must not authorize a release
	// substitution.
	return h.resolveVirtualInputURIExcluding(ctx, file, userID, profileID, forceRefresh, nil, false)
}

// resolveVirtualInputURIExcluding resolves a virtual input, optionally
// excluding a failed candidate so the next-ranked release is tried. The
// excluded candidate ID is threaded into the detailed resolver, which re-lists
// and skips it (see plugins.ResolveVirtualPlaybackDetailedWithRouting).
//
// rotateCandidates declares whether the exclusion is the serve layer's own
// indictment of that release — a delivery that produced no bytes, a decode
// rejection, or a dead candidate — which is what authorizes substituting a
// sibling. It is explicit, not inferred from a non-empty exclusion list: an
// exclusion alone is not a verdict, so a caller that excludes a candidate for
// any other reason must pass false and let the resolver keep refusing a silent
// release swap.
func (h *StreamHandler) resolveVirtualInputURIExcluding(
	ctx context.Context,
	file *models.MediaFile,
	userID int,
	profileID string,
	forceRefresh bool,
	excludedCandidateIDs []string,
	rotateCandidates bool,
) (ResolvedVirtualMedia, func(), error) {
	resolved := ResolvedVirtualMedia{}
	var err error
	// Stored-URL shortcut, mirroring the transport resolver: a session-bound
	// re-resolve of a pinned candidate serves the row's own persisted URL when
	// it is present, unexpired and passes the resolver's URL validator. The
	// row is re-read by the exact candidate path so a row bound to a different
	// virtual source cannot contribute its stored URL. forceRefresh (a
	// failover retry) and an exclusion list always list afresh.
	var storedExpiredRow *models.MediaFile
	if !forceRefresh && len(excludedCandidateIDs) == 0 {
		if usable, row, state := h.lookupStoredVirtualURLCandidate(ctx, file.FilePath, file.VirtualOwnerInstallationID); state == virtualStoredURLUsable {
			resolved = usable
		} else if state == virtualStoredURLExpiredWithinWindow || state == virtualStoredURLExpired {
			// An expired stored URL is never served. Inside the window the
			// refresh below is also allowed to keep the same candidate; outside
			// it today's refresh-or-rotate behavior is unchanged.
			storedExpiredRow = row
		}
	}
	// The serve-layer row carries the durable identity the same-release
	// re-match needs; a row with none (legacy) is left untouched. Inside the
	// trust window the row is also marked trusted so a delisted same-identity
	// candidate is not reported as absent (which would rotate to a sibling).
	ctx = virtualResolveContextWithPersistedTrust(ctx, file, time.Now(), h.virtualStoredURLTrustWindow(), h.storedVirtualURLAllowInsecure(file, file.VirtualOwnerInstallationID))
	if resolved.URL == "" {
		if h.VirtualMediaDetailedResolver != nil {
			// The caller declares whether excluding the candidate indicted the
			// release; a display-driven same-file re-plan never does.
			ctx = withVirtualCandidateRotationV3(ctx, rotateCandidates)
			// The session-binding intent travels on the context and is
			// declared by the caller: absent means session-bound, the
			// conservative default that refuses a profile-removed or absent
			// pin instead of silently swapping the release. The serve layer
			// always re-resolves a release an existing session serves, so it
			// leaves the default in place; a fresh selection that does not
			// serve a session declares false (withVirtualSessionBindingV3) and
			// falls through to a profile-satisfying sibling.
			// A transient provider-listing blackout for the session's own
			// trusted candidate must not be read as an indictment of the
			// release. Retry it with a short bounded backoff before giving up,
			// and classify the final failure as a retryable provider outage so
			// the client keeps retrying the release it picked. The retry keeps
			// the caller's exact parameters (exclusions, forceRefresh), so an
			// intentional rotation still rotates on the first successful
			// relist; only a failed listing is retried.
			outageTrusted := h.virtualCandidateTrustedForOutageRetry(file)
			resolved, err = retryVirtualProviderOutageResolve(ctx, outageTrusted, func(retryCtx context.Context, relist bool) (ResolvedVirtualMedia, error) {
				if relist {
					retryCtx = virtuallibrary.WithProviderOutageRelist(retryCtx)
				}
				return h.VirtualMediaDetailedResolver.ResolveVirtualMediaDetailed(
					retryCtx, file.FilePath, file.VirtualOwnerInstallationID, userID, profileID, forceRefresh || relist, excludedCandidateIDs, "",
				)
			})
			if err == nil && resolved.IdentityRematched {
				// Same release, new provider id: adopt the new ?result= and the
				// resolution's identity under the existing CAS/fence write.
				adoptRematchedVirtualResolution(ctx, file, resolved, h.VirtualFileMetadataSaver, h.VirtualFileSaver)
			} else if err == nil && storedExpiredRow != nil {
				// Same best-effort contract as the transport resolver: the URL
				// is already in hand, so persistence must not fail the serve.
				_, _ = refreshStoredVirtualResolution(ctx, storedExpiredRow, resolved, h.VirtualFileMetadataSaver, h.VirtualFileSaver)
			} else if err != nil {
				err = classifyVirtualProviderOutage(err, outageTrusted)
			}
		} else if forceRefresh && h.VirtualMediaRefreshResolver != nil {
			resolved.URL, err = h.VirtualMediaRefreshResolver.RefreshVirtualMedia(
				ctx, file.FilePath, file.VirtualOwnerInstallationID, userID, profileID,
			)
		} else {
			resolved.URL, err = resolveVirtualMediaPath(
				ctx, h.VirtualMediaResolver, file.FilePath,
				file.VirtualOwnerInstallationID, userID, profileID,
			)
		}
	}
	if err != nil {
		return ResolvedVirtualMedia{}, nil, fmt.Errorf("resolve virtual input: %w", err)
	}
	if h.RemoteStreamRelay == nil {
		return resolved, func() {}, nil
	}
	var relayURL string
	var cleanup func()
	ownerID := effectiveVirtualOwner(resolved.OwnerID, file.VirtualOwnerInstallationID)
	insecure := h.AllowPrivateStreams != nil && h.AllowPrivateStreams(ownerID)
	// A serve-path retry that follows a transient relay/edge failure sets the
	// fresh-registration marker (see withVirtualRelayFreshRegistration): it must
	// present a newly minted relay entry instead of reusing the live token whose
	// upstream just answered 5xx/502/503, mirroring the remux seek-anchor retry.
	// Every other caller keeps the content-key reuse that shares one upstream
	// and one range-cache scope.
	fresh := virtualRelayFreshRegistrationRequested(ctx)
	switch {
	case insecure && fresh:
		relayURL, cleanup, err = h.RemoteStreamRelay.RegisterInsecureWithHeadersFresh(ctx, resolved.URL, resolved.RequestHeaders)
	case insecure:
		relayURL, cleanup, err = h.RemoteStreamRelay.RegisterInsecureWithHeaders(ctx, resolved.URL, resolved.RequestHeaders)
	case fresh:
		relayURL, cleanup, err = h.RemoteStreamRelay.RegisterWithHeadersFresh(ctx, resolved.URL, resolved.RequestHeaders)
	default:
		relayURL, cleanup, err = h.RemoteStreamRelay.RegisterWithHeaders(ctx, resolved.URL, resolved.RequestHeaders)
	}
	if err != nil {
		return ResolvedVirtualMedia{}, nil, err
	}
	resolved.URL = relayURL
	return resolved, cleanup, nil
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
	if h.StreamDeny.Denied(r.Context(), sessionID) {
		writePlaybackSessionEnded(w)
		return
	}

	// Look up the session, reconstructing it from the recipe card on a not-found
	// miss (e.g. after a server restart) so a direct/remux stream resumes instead
	// of 404-ing. The client re-supplies its position (HTTP Range for direct, the
	// ?seek= query for remux), so no runtime beyond the Session needs rebuilding.
	// Without a token (or signing secret) reconstruct is off, collapsing to a
	// plain GetSession + ownership check.
	card, claims := verifiedStreamCardFromToken(r.URL.Query().Get(streamTokenParam), sessionID, h.JWTSecret)
	loadCard := card
	if _, err := h.sessionMgr.GetSession(sessionID); err == nil {
		// A live route may have been replanned since this token was issued. Do not
		// let stale recipe routing override the current session, and do not revive
		// the stale recipe if the live session disappears during this request.
		loadCard = nil
	} else if errors.Is(err, playback.ErrSessionNotFound) {
		if loadCard == nil {
			// A header-authenticated session carries no token; its recipe is the
			// server-side recovery card.
			var recipeErr error
			if loadCard, recipeErr = h.storedSessionRecipeV3(r.Context(), sessionID, userID); recipeErr != nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load playback session")
				return
			}
		}
		if !requireNativeRecipeAPIEgressV3(w, loadCard) {
			return
		}
	} else {
		// Do not turn an inconsistent backend read into authority to reconstruct
		// from a recipe whose route was never checked against a clean miss.
		loadCard = nil
	}
	session, status, reconstructed, sessionErr := h.TM.LoadOrReconstructSessionDetailWithError(r.Context(), h.sessionMgr.GetSession, sessionID, userID, loadCard)
	switch status {
	case playback.SessionMissing:
		writePlaybackSessionNotFound(w)
		return
	case playback.SessionLoadFailed:
		writeErrorCause(w, http.StatusInternalServerError, "internal_error", "Failed to load playback session", sessionErr)
		return
	case playback.SessionForbidden:
		writeError(w, http.StatusForbidden, "forbidden", "Session belongs to another user")
		return
	}
	if !requireNativeSessionAPIEgressV3(w, session) {
		return
	}
	if !requireOwningProfile(w, r, session) {
		return
	}

	file, err := h.fileResolver.GetByID(r.Context(), session.MediaFileID)
	if err != nil {
		if isPlaybackFileLookupMissing(err) {
			h.abortPlaybackSession(r.Context(), session)
			writeError(w, http.StatusNotFound, "not_found", "Media file not found")
			return
		}
		// A canceled lookup is the viewer going away mid-start, not a broken
		// catalog row: answer 204 without failing the session so it reads as
		// a cancel in logs and metrics, never a 500. It is also not a stop —
		// a track switch aborts the old stream and replans the same session —
		// so record the cancel as route-scoped evidence and keep the session
		// live. Stopping or denying it here would strand the session the
		// client still holds: the follow-up replan and the reconnected stream
		// would answer a terminal 404/410 for a session the viewer still owns.
		if isClientCancellation(r.Context(), err) {
			if marker, ok := h.sessionMgr.(interface {
				MarkClientCanceled(sessionID string) error
			}); ok {
				_ = marker.MarkClientCanceled(session.ID)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeErrorCause(w, http.StatusInternalServerError, "internal_error", "Failed to load media file", err)
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
	attachPlaybackSession(r.Context(), session, claims)

	if reconstructed && session.PlayMethod == playback.PlayRemux &&
		videoCopyRevivalRefused(r.Context(), h.CopySafetyRacer, file, sessionID) {
		h.abortPlaybackSession(r.Context(), session)
		writePlaybackSessionNotFound(w)
		return
	}

	// Adopt the concrete owner row of the bound candidate when it has appeared
	// since the plan, then bind to the session's planned virtual URI: the
	// catalog row's path is mutable (candidate rotation), but the session
	// captured the exact URI that was resolved and probed during planning. The
	// tracks bind is used so the session's carried overlay travels with the row,
	// exactly as the inventory bind does — a session adopted onto the owner row
	// must keep serving the verified inventory it verified, not the owner row's
	// thinner declared list.
	if adopted, refreshed := reanchorLateVirtualOwnerRow(r.Context(), h.fileResolver, h.sessionMgr, file, session, session.VirtualSourceURI); adopted != nil {
		file, session = adopted, refreshed
	}
	file = bindSessionVirtualSourceWithTracks(r.Context(), file, session, h.fileResolver)

	// A verified overlay may have replaced the carried audio list with one that
	// no longer contains the committed selection. The session records that as an
	// invalid selection and the remux fails closed rather than derive an ordinal
	// from a stale position and mux whichever stream now occupies it; the client
	// replans and re-selects. Direct play needs no gate (the client demuxes the
	// bytes), and a transcode start reads the plan's own selected index.
	if session.PlayMethod == playback.PlayRemux && isVirtualPlaybackFile(file) && session.VirtualAudioSelectionInvalid {
		writeVirtualAudioSelectionInvalid(w)
		return
	}

	// Capture the delivered identity and observed health state at transport
	// start: the delivered candidate is the path the transport will serve
	// (retained by the session even if the row rotates mid-stream), and the
	// failure stamp seen now is the only health state a successful delivery
	// may clear. A rotation to B or a newer failure on A that lands while the
	// stream is being served is preserved.
	virtualObservedFailedAt := (*time.Time)(nil)
	if file != nil && isVirtualPlaybackFile(file) {
		if current, err := h.fileResolver.GetByID(r.Context(), file.ID); err == nil && current != nil {
			virtualObservedFailedAt = current.FailedAt
		}
	}

	inputPath := file.FilePath
	deliveredPath := ""
	releaseInput := func() {}
	// releaseCacheHandoff drops the session's cache-handoff registration when
	// the transport ends. It is a no-op for a non-virtual source.
	releaseCacheHandoff := func() {}
	if isVirtualPlaybackFile(file) && hasVirtualMediaResolver(h) {
		// A previous request already spent the initial resolve and the bounded
		// rotation on this session-bound candidate and produced no bytes. A
		// client segment retry must not replay that whole recovery inside the
		// short terminal window: answer the same retryable 502 until the
		// session replans to another candidate or the entry expires.
		if candidateID := virtualCandidateID(file); candidateID != "" &&
			h.virtualStreamRecoveryTerminals.blocked(sessionID, candidateID) {
			err := fmt.Errorf("virtual stream recovery exhausted for candidate %q", candidateID)
			slog.DebugContext(r.Context(), "virtual stream recovery terminal",
				"component", "api",
				"session", sessionID,
				"playback_session_id", sessionID,
				"file_id", file.ID,
				"pinned_candidate_id", candidateID,
				"status", "failed_fast",
			)
			writeErrorCause(w, http.StatusBadGateway, "virtual_stream_unavailable", "Failed to stream virtual media source", err)
			return
		}
		resolved, cleanup, resolveErr := h.resolveVirtualInputForServe(r.Context(), file, session.UserID, session.ProfileID)
		if resolveErr != nil && (errors.Is(resolveErr, virtuallibrary.ErrSessionBoundCandidateAbsent) || errors.Is(resolveErr, ErrVirtualCandidateMarkedFailed)) {
			// The session's pinned release is absent from the provider's current
			// list (it renumbered or dropped the result id). Mirror the transcode
			// startup loop: relist fresh, exclude the absent pin, and declare the
			// exclusion a verdict so the resolver may serve a live sibling. The
			// session identity is preserved — only the anchor rotates.
			pinnedID := virtualResultCandidateID(file.FilePath)
			excluded := []string(nil)
			if pinnedID != "" {
				excluded = []string{pinnedID}
			}
			rotated, rotatedCleanup, rotateErr := h.resolveVirtualInputURIExcluding(
				r.Context(), file, session.UserID, session.ProfileID, true, excluded, true,
			)
			if rotateErr == nil {
				slog.InfoContext(r.Context(), "virtual stream rotated an absent session-bound candidate",
					"component", "api", "session", sessionID, "file_id", file.ID,
					"status", "rotated", "old_candidate_id", pinnedID,
					"new_candidate_id", rotated.CandidateID, "virtual_uri", rotated.URI)
				resolved, cleanup, resolveErr = rotated, rotatedCleanup, nil
				h.commitRotatedVirtualSessionSource(r.Context(), sessionID, rotated)
			} else {
				slog.WarnContext(r.Context(), "virtual stream candidate rotation failed",
					"component", "api", "session", sessionID, "file_id", file.ID,
					"status", "rotation_failed", "old_candidate_id", pinnedID,
					"error", logredact.SanitizeURLError(rotateErr))
				resolveErr = rotateErr
			}
		}
		if resolveErr != nil {
			logVirtualStreamFailure(r.Context(), sessionID, file, resolveErr)
			// A provider resolve failure is a dependency problem, not an
			// internal error. A transient provider-listing blackout for the
			// session's own trusted candidate is answered 503
			// provider_unavailable (retryable) so hls.js keeps retrying the
			// release the viewer picked instead of rotating; every other
			// resolve failure keeps the v1 502 that the v2 byte-delivery
			// adapter maps to a retryable dependency_unavailable.
			if errors.Is(resolveErr, errVirtualProviderUnavailable) {
				w.Header().Set("Retry-After", strconv.Itoa(virtualServeResolveRetryAfterSeconds))
				writeErrorCause(w, http.StatusServiceUnavailable, "provider_unavailable", "The virtual source provider is temporarily unavailable", resolveErr)
				return
			}
			writeErrorCause(w, http.StatusBadGateway, "virtual_resolve_failed", "Failed to resolve virtual source", resolveErr)
			return
		}
		inputPath = resolved.URL
		deliveredPath = resolvedVirtualCandidatePath(resolved)
		releaseInput = cleanup
		// A pinned release streaming from the provider is a candidate for a
		// cache handoff: pressing play asks the provider to fill its cache (Vio
		// never fetches the media) and, when the fill completes, the session is
		// rebound to the cached copy without changing the release.
		releaseCacheHandoff = h.beginVirtualCacheHandoff(r.Context(), session, file, resolved)
	}
	defer releaseCacheHandoff()
	defer func() {
		if releaseInput != nil {
			releaseInput()
		}
	}()

	switch session.PlayMethod {
	case playback.PlayDirect:
		if err := h.sessionMgr.BeginTransport(sessionID); err == nil {
			defer func() {
				_ = h.sessionMgr.EndTransport(sessionID)
			}()
		}
		if isVirtualPlaybackFile(file) {
			streamWriter := httpstream.NewRollingDeadlineWriter(w)
			targetURL, err := url.Parse(inputPath)
			if err == nil && targetURL.Scheme != "http" {
				err = fmt.Errorf("unsupported virtual stream scheme %q", targetURL.Scheme)
			}
			if err != nil {
				logVirtualStreamFailure(r.Context(), sessionID, file, err)
				h.handleTransportStartFailure(r.Context(), session, file, err)
				writeErrorCause(streamWriter, http.StatusBadGateway, "virtual_stream_unavailable", "Failed to stream virtual media source", err)
				return
			}
			// This proxy forwards client headers to the target by design; that
			// is only safe because virtual inputs always resolve to the local
			// relay. Assert the invariant rather than trusting every caller.
			host := targetURL.Hostname()
			if host != "127.0.0.1" && host != "::1" && host != "[::1]" {
				err := fmt.Errorf("virtual direct-play proxy target %q is not the local relay", targetURL.Host)
				logVirtualStreamFailure(r.Context(), sessionID, file, err)
				h.handleTransportStartFailure(r.Context(), session, file, err)
				writeErrorCause(streamWriter, http.StatusBadGateway, "virtual_stream_unavailable", "Failed to stream virtual media source", err)
				return
			}
			var lastProxyErr error
			// relayNotFound captures a relay-own 404 (unknown/expired token —
			// upstream 404s arrive collapsed as 502, so a 404 here always
			// means our registration lapsed, never a dead release).
			relayNotFound := false
			// relayTemporary captures a relay error response that marks the
			// upstream failure as transport-temporary (5xx after the relay's
			// open retry, or an unreachable upstream). Such a failure is
			// availability-shaped, so it must not stamp a durable
			// dead-candidate verdict; the pinned release may still rotate for
			// this attempt when a sibling exists.
			relayTemporary := false
			// recoveryExhaustedCandidateID names the pinned candidate whose
			// initial serve and bounded rotation both failed without bytes.
			// Recording it remembers the exhausted recovery for this session;
			// an empty value means there is nothing to remember.
			recoveryExhaustedCandidateID := ""
			proxy := &httputil.ReverseProxy{
				Rewrite: func(pr *httputil.ProxyRequest) {
					pr.Out.URL = targetURL
					pr.Out.Host = targetURL.Host
				},
				ModifyResponse: func(res *http.Response) error {
					if res.StatusCode == http.StatusNotFound {
						relayNotFound = true
						return fmt.Errorf("relay token not found")
					}
					if res.StatusCode >= http.StatusInternalServerError {
						relayTemporary = remotestream.RelayTemporaryFailure(res)
						return fmt.Errorf("relay returned HTTP %d", res.StatusCode)
					}
					return nil
				},
				ErrorHandler: func(rw http.ResponseWriter, req *http.Request, proxyErr error) {
					lastProxyErr = proxyErr
				},
			}
			proxy.ServeHTTP(streamWriter, r)
			if lastProxyErr != nil {
				// Log the first-attempt serve verdict while the recovery state
				// is still observable: which candidate the attempt served and
				// how that attempt ended. This is first-attempt
				// classification, not the final recovery outcome — a later
				// heal or rotation can still serve the request. A later 503
				// names only the status; this line names the release and the
				// path taken, so the next virtual_stream_unavailable is a
				// join, not forensics. Client cancellation stays debug (see
				// logVirtualStreamFailure); a genuine failure keeps its WARN.
				disposition := virtualProxyDisposition(r.Context(), lastProxyErr, streamWriter.StatusCode(), relayNotFound, relayTemporary)
				logVirtualProxyVerdict(r.Context(), sessionID, file, deliveredPath, disposition, lastProxyErr)
				// A client that navigated away is not a candidate failure:
				// never stamp the pinned release known-bad or spend a failover
				// resolve on it. The upstream request already aborted with the
				// canceled request context.
				if !isClientCancellation(r.Context(), lastProxyErr) && streamWriter.StatusCode() == 0 && (h.VirtualMediaDetailedResolver != nil || h.VirtualMediaRefreshResolver != nil) {
					if releaseInput != nil {
						releaseInput()
						releaseInput = nil
					}
					// A pinned release that already has a cached copy heals by
					// handing off to it: the failed fetch was the remote
					// provider, not the release, so the release is never
					// indicted. This runs before the sibling-rotation path so a
					// cache-aware heal never silently switches releases; when no
					// cached copy exists the existing rotation is unchanged.
					handoffAttempted := false
					if healedResolved, healCleanup, healedOK, _ := h.handoffVirtualSessionToCached(r.Context(), session, file, false); healedOK {
						healed := false
						if healedURL, parseErr := url.Parse(healedResolved.URL); parseErr == nil && healedURL.Scheme == "http" {
							healedHost := healedURL.Hostname()
							if healedHost == "127.0.0.1" || healedHost == "::1" || healedHost == "[::1]" {
								targetURL = healedURL
								deliveredPath = resolvedVirtualCandidatePath(healedResolved)
								releaseInput = healCleanup
								lastProxyErr = nil
								relayTemporary = false
								proxy.ServeHTTP(streamWriter, r)
								// The heal is a handoff only when the retried
								// stream actually delivered. A non-loopback
								// target, a parse failure, or a second proxy
								// error must fall through to failure marking and
								// sibling rotation rather than answer 5xx/502/503
								// while a live sibling exists.
								healed = lastProxyErr == nil
							}
						}
						if healed {
							handoffAttempted = true
						} else if healCleanup != nil {
							healCleanup()
						}
					}
					if !handoffAttempted {
						// The pinned candidate served no bytes. Before
						// indicting it, one same-release retry covers a
						// lapsed relay registration: re-resolve mints a
						// fresh token (no backoff needed — the token, not
						// time, is the fix). Serving bytes heals without
						// indictment; anything else falls through to the
						// existing mark-and-rotate path unchanged.
						// (Transient 5xx flaps are already retried once at
						// the relay open phase; retrying them again here
						// would break the pinned single-relist and
						// rotation-declaration contracts, so they proceed
						// to bounded rotation with the durable stamp
						// suppressed below.)
						if relayNotFound {
							sameRetryCtx := withVirtualRelayFreshRegistration(r.Context())
							sameMedia, sameCleanup, sameErr := h.resolveVirtualInputURIExcluding(sameRetryCtx, file, session.UserID, session.ProfileID, true, nil, false)
							sameID := ""
							if sameErr == nil {
								sameID = sameMedia.CandidateID
								if sameID == "" {
									if parsed, err := url.Parse(sameMedia.URI); err == nil {
										sameID = parsed.Query().Get("result")
									}
								}
							}
							var expectedID string
							if parsed, err := url.Parse(file.FilePath); err == nil {
								expectedID = parsed.Query().Get("result")
							}
							if sameErr == nil && (expectedID == "" || sameID == expectedID) {
								if sameURL, parseErr := url.Parse(sameMedia.URL); parseErr == nil && sameURL.Scheme == "http" {
									if sameHost := sameURL.Hostname(); sameHost == "127.0.0.1" || sameHost == "::1" || sameHost == "[::1]" {
										targetURL = sameURL
										deliveredPath = resolvedVirtualCandidatePath(sameMedia)
										releaseInput = sameCleanup
										sameCleanup = nil
										lastProxyErr = nil
										relayNotFound = false
										relayTemporary = false
										proxy.ServeHTTP(streamWriter, r)
									}
								}
							}
							if sameCleanup != nil {
								sameCleanup()
							}
						}
						if lastProxyErr == nil {
							// The same-release retry healed: skip indictment
							// and sibling rotation entirely — the pin just
							// served bytes. Mirrors the success tail below.
							if virtualCandidateDeliveryEvidence(streamWriter.StatusCode(), streamWriter.BytesWritten()) {
								h.clearVirtualCandidateRecovered(r.Context(), file, deliveredPath, virtualObservedFailedAt)
								h.virtualStreamRecoveryTerminals.clearSession(sessionID)
							}
							return
						}
						// The pinned candidate served no bytes (corrupted NZB, dead
						// provider URL), or a transport-temporary upstream failure
						// exhausted both attempts. Re-resolve with the failed
						// candidate excluded so the next-ranked release is tried.
						//
						// A transport-temporary relay failure (upstream 5xx after
						// the relay's own open retry, or an unreachable upstream)
						// is a provider-availability event, not a verdict about
						// the release: two 503s half a second apart cannot
						// distinguish an outage from a dead release, so the
						// durable dead-candidate stamp is suppressed. The
						// bounded rotation below still runs — the exclusion and
						// relist let a same-release sibling serve this attempt —
						// but nothing is persisted, so the pin stays eligible
						// for the next auto-pick.
						failedID := virtualResultCandidateID(deliveredPath)
						if failedID != "" {
							// The initial serve failed; the rotation below is
							// the only remaining recovery for this request.
							// Remember the candidate so an exhausted rotation
							// can fail later retries fast. A transport-
							// temporary failure is still remembered here: it
							// is not a durable dead-candidate verdict, but the
							// exhausted request is just as expensive to
							// replay.
							recoveryExhaustedCandidateID = failedID
							if !relayTemporary {
								h.markVirtualCandidateFailed(r.Context(), file, failedID)
							}
						}
						excluded := []string{failedID}
						if failedID == "" {
							excluded = nil
						}
						// Re-resolve with the failed candidate excluded so the
						// next-ranked release can serve this attempt. The
						// exclusion is normally the serve layer's own
						// indictment of the delivered candidate; for a
						// transport-temporary failure it is a bounded,
						// non-durable rotation (the stamp above was skipped),
						// so the resolver may serve a same-release sibling
						// without anything being persisted. Declare
						// substitution in either case: a retry with no
						// identified candidate (failedID empty) keeps
						// refusing, so a live pin is never swapped blindly.
						// The retry re-resolves with a forced relist and,
						// because the edge just failed, a fresh relay
						// registration: a reused token would replay the
						// registration whose upstream returned 5xx/502/503
						// instead of presenting newly resolved bytes, mirroring
						// the remux seek-anchor retry.
						retryCtx := withVirtualRelayFreshRegistration(r.Context())
						refreshedMedia, refreshCleanup, refreshErr := h.resolveVirtualInputURIExcluding(retryCtx, file, session.UserID, session.ProfileID, true, excluded, failedID != "")
						if refreshErr == nil {
							expectedCandidateID := ""
							if parsed, err := url.Parse(file.FilePath); err == nil {
								expectedCandidateID = parsed.Query().Get("result")
							}
							// A failed pin this serve layer just marked is an
							// intended substitution: the retry excluded that id, so
							// it can only return a sibling. The guard exists to stop
							// a *silent* swap of a live candidate, so it only applies
							// when no failed candidate was identified.
							if failedID == "" && expectedCandidateID != "" && refreshedMedia.CandidateID != "" && refreshedMedia.CandidateID != expectedCandidateID {
								if refreshCleanup != nil {
									refreshCleanup()
								}
								lastProxyErr = fmt.Errorf("refreshed candidate %q does not match pinned candidate %q", refreshedMedia.CandidateID, expectedCandidateID)
								logVirtualRotationOutcome(r.Context(), sessionID, file, failedID, refreshedMedia, nil, false, lastProxyErr)
							} else {
								// Release any adopted registration first: the
								// same-release retry above may hold one, and
								// overwriting it would leak it until LRU/24h
								// expiry. The original flow always arrives
								// with nil here (released at failure entry),
								// so this is a no-op there.
								if releaseInput != nil {
									releaseInput()
									releaseInput = nil
								}
								releaseInput = refreshCleanup
								refreshedURL, parseErr := url.Parse(refreshedMedia.URL)
								if parseErr == nil && refreshedURL.Scheme == "http" {
									refreshedHost := refreshedURL.Hostname()
									if refreshedHost == "127.0.0.1" || refreshedHost == "::1" || refreshedHost == "[::1]" {
										targetURL = refreshedURL
										deliveredPath = resolvedVirtualCandidatePath(refreshedMedia)
										lastProxyErr = nil
										// The rotated attempt has its own
										// disposition: the first attempt's
										// temporary/not-found marker must not
										// decide whether this sibling's own
										// verdict is stamped.
										relayTemporary = false
										relayNotFound = false
										proxy.ServeHTTP(streamWriter, r)
										// Name the recovery: which sibling the
										// retry resolved and whether it
										// delivered. Without this the
										// first-attempt verdict is the only
										// evidence and a sibling that also
										// failed is indistinguishable from a
										// rotation that never ran.
										logVirtualRotationOutcome(r.Context(), sessionID, file, failedID, refreshedMedia, nil,
											lastProxyErr == nil && virtualCandidateDeliveryEvidence(streamWriter.StatusCode(), streamWriter.BytesWritten()), lastProxyErr)
									} else {
										logVirtualRotationOutcome(r.Context(), sessionID, file, failedID, refreshedMedia, nil, false,
											fmt.Errorf("refreshed candidate target %q is not the local relay", refreshedURL.Host))
									}
								} else {
									logVirtualRotationOutcome(r.Context(), sessionID, file, failedID, refreshedMedia, nil, false, parseErr)
								}
							}
						} else {
							logVirtualRotationOutcome(r.Context(), sessionID, file, failedID, ResolvedVirtualMedia{}, refreshErr, false, nil)
						}
					}
				}
				if lastProxyErr != nil {
					// Both the pinned candidate and its one bounded rotation
					// failed without delivering bytes. Remember that for this
					// session and candidate so segment retries inside the
					// short terminal window fail fast instead of re-running
					// the same resolve and rotation. A successful serve, a
					// different binding, or expiry clears the way.
					if streamWriter.StatusCode() == 0 && recoveryExhaustedCandidateID != "" &&
						!isClientCancellation(r.Context(), lastProxyErr) {
						h.virtualStreamRecoveryTerminals.record(sessionID, recoveryExhaustedCandidateID)
					}
					h.handleTransportStartFailure(r.Context(), session, file, lastProxyErr)
					if streamWriter.StatusCode() == 0 {
						logVirtualStreamFailure(r.Context(), sessionID, file, lastProxyErr, deliveredPath)
						status, code, message := virtualProxyFailureStatus(r.Context(), lastProxyErr)
						writeErrorCause(streamWriter, status, code, message, lastProxyErr)
					}
				}
			}
			if lastProxyErr == nil && virtualCandidateDeliveryEvidence(streamWriter.StatusCode(), streamWriter.BytesWritten()) {
				h.clearVirtualCandidateRecovered(r.Context(), file, deliveredPath, virtualObservedFailedAt)
				h.virtualStreamRecoveryTerminals.clearSession(sessionID)
			}
			return
		}
		if err := playback.ServeDirectPlay(w, r, inputPath); err != nil {
			h.handleTransportStartFailure(r.Context(), session, file, err)
		}

	case playback.PlayRemux:
		if setter, ok := h.sessionMgr.(interface {
			SetOutputFormat(string, string, string) error
		}); ok {
			_ = setter.SetOutputFormat(sessionID, playback.OutputContainerFMP4, playback.OutputProtocolHTTP)
		}
		if err := h.sessionMgr.BeginTransport(sessionID); err == nil {
			defer func() {
				_ = h.sessionMgr.EndTransport(sessionID)
			}()
		}
		// A progressive remux is one long response whose FFmpeg is canceled only
		// by this request's context. A server-initiated stop — StopSession, an
		// admin kill, an idle reap — would otherwise leave it streaming a route
		// the session manager has already withdrawn, so feed the session's
		// transport-stop watch, together with the request context, into the
		// remux abort. The request context also interrupts a response write
		// blocked on a client that stopped reading.
		remuxAbort, releaseRemuxAbort := watchProgressiveRemuxAbort(r.Context(), h.sessionMgr, sessionID)
		defer releaseRemuxAbort()
		// A server-initiated stop ends this remux through remuxAbort, and the
		// request context is not canceled, so the resulting failure is not a
		// client cancellation. Before the first media byte it surfaces as
		// errRemuxNoOutput, which the virtual failure branch below would read as
		// a provider release that served nothing — retiring a healthy release
		// because of a stop the operator asked for. Record that the transport was
		// stopped so the branch can tell the two apart.
		seekSeconds := 0.0
		if seekStr := r.URL.Query().Get("seek"); seekStr != "" {
			if s, err := strconv.ParseFloat(seekStr, 64); err == nil && s >= 0 {
				seekSeconds = s
			}
		}
		// An audio-only source muxes an audio-only fMP4. The v3 plan promises
		// audio/mp4 for it, and a declared-tier client refuses to attach a
		// source buffer whose advertised type its probe rejected — so the
		// response has to keep the same promise the plan made.
		stoppedServerSide := remuxStoppedServerSide(r.Context(), remuxAbort)
		serveRemux := func() error {
			// The remux writes through the raw writer; wrap it to observe how
			// many bytes actually reached the client. nil return is NOT
			// delivery evidence (remux.go can return nil when the first
			// client write fails) — recovery is gated on positive bytes.
			remuxWriter := httpstream.NewRollingDeadlineWriter(w)
			// FFmpeg opens the input this transport serves. A virtual row's
			// FilePath is the session's virtual:// identity, which FFmpeg
			// cannot open: handing it over makes FFmpeg exit before its first
			// media byte and the remux reports "produced no output". Serve the
			// resolved relay URL instead, exactly as direct play does, so the
			// cache heal and sibling rotation below (which update inputPath)
			// also reach the retry.
			err := playback.ServeRemuxWithOptions(remuxWriter, r, inputPath, "mp4", seekSeconds, session.TranscodeAudio, session.AudioTrackIndex, file.PrimaryDVProfile(), playback.RemuxServeOptions{
				DVMode:                    session.RemuxDVMode,
				FFmpegPath:                h.ffmpegPath(),
				DropResumeLeadingPictures: session.RemuxResumeLeadingPictureDrop,
				ContentType:               playback.RemuxContentType(file.IsAudioOnly()),
				AudioOnly:                 file.IsAudioOnly(),
				SourceAudioChannels:       session.SourceAudioChannels,
				TargetAudioChannels:       session.TargetAudioChannels,
				TargetAudioBitrateKbps:    session.TargetAudioBitrateKbps,
				// For a copied track the planned codec is the source's, but the
				// ADTS filter applies only when the container frames that track as
				// ADTS (see playback.CopiedAudioADTSCodec).
				CopyAudioCodec: playback.CopiedAudioADTSCodec(session.TargetAudioCodec, fileContainerV3(file)),
				Abort:          remuxAbort,
				// A pre-body start failure must leave the response
				// uncommitted so the handler can fail over to a sibling
				// candidate; the v2 writer locks the first >=400 status and
				// would discard a successful retry.
				DeferStartError: true,
			})
			if err == nil && isVirtualPlaybackFile(file) &&
				virtualCandidateDeliveryEvidence(http.StatusOK, remuxWriter.BytesWritten()) {
				// Media bytes actually flowed to the client for the candidate
				// the session planned — the only evidence that forgives a
				// transport failure. Fenced on the delivered identity and the
				// health state observed at transport start.
				h.clearVirtualCandidateRecovered(r.Context(), file, deliveredPath, virtualObservedFailedAt)
			}
			return err
		}
		remuxErr := serveRemux()
		if remuxErr != nil {
			// The remux only commits 200 after FFmpeg produces media bytes, so
			// a failure here means the provider release served no output
			// (corrupted NZB, dead URL). Mark the candidate failed and retry
			// once with it excluded so the next-ranked release is tried. A
			// client that disconnected is not a candidate failure: the remux
			// (and its provider fetch) already aborted on the request context.
			// A fault in the recipe this server built (a bitstream filter that
			// refuses the mapped stream) is not evidence about the release. Do
			// not mark the candidate failed or rotate: that indicts healthy bytes
			// and hands the client a verdict about a file it never faulted.
			if !isClientCancellation(r.Context(), remuxErr) && !errors.Is(remuxErr, playback.ErrRemuxRecipeFault) && !stoppedServerSide() && isVirtualPlaybackFile(file) && hasVirtualMediaResolver(h) {
				if releaseInput != nil {
					releaseInput()
					releaseInput = nil
				}
				// Cache-aware heal first: a cached copy of the pinned release
				// heals the failure without indicting the release. Only when no
				// cached copy exists does the existing rotation-to-a-sibling
				// path run, so a cache-aware heal never silently changes the
				// release.
				handled := false
				if healedResolved, healCleanup, healedOK, _ := h.handoffVirtualSessionToCached(r.Context(), session, file, false); healedOK {
					if healedURL, parseErr := url.Parse(healedResolved.URL); parseErr == nil && healedURL.Scheme == "http" {
						healedHost := healedURL.Hostname()
						if healedHost == "127.0.0.1" || healedHost == "::1" || healedHost == "[::1]" {
							inputPath = healedResolved.URL
							deliveredPath = resolvedVirtualCandidatePath(healedResolved)
							releaseInput = healCleanup
							remuxErr = serveRemux()
							// Only a heal that actually delivered is handled;
							// a cached retry that failed too falls through to
							// failure marking and sibling rotation.
							if remuxErr == nil {
								handled = true
							} else if healCleanup != nil {
								healCleanup()
								releaseInput = nil
							}
						} else if healCleanup != nil {
							healCleanup()
							releaseInput = nil
						}
					} else if healCleanup != nil {
						healCleanup()
						releaseInput = nil
					}
				}
				if !handled {
					// The cached retry above re-ran serveRemux, so re-read the
					// abort: a stop or a client disconnect that landed during it
					// ends recovery here instead of indicting a release that was
					// never given a chance to deliver. Without this the retry
					// marks the candidate failed and rotates away from a healthy
					// release, which is what the guard above exists to prevent.
					if isClientCancellation(r.Context(), remuxErr) || stoppedServerSide() {
						handled = true
					}
				}
				if !handled {
					failedID := virtualResultCandidateID(deliveredPath)
					if failedID != "" {
						h.markVirtualCandidateFailed(r.Context(), file, failedID)
					}
					excluded := []string{failedID}
					if failedID == "" {
						excluded = nil
					}
					// The remux failed because this serve layer just indicted the
					// delivered candidate; declare substitution so the resolver may
					// serve a sibling release. A retry with no indictment keeps
					// refusing.
					retried, retryCleanup, retryErr := h.resolveVirtualInputURIExcluding(r.Context(), file, session.UserID, session.ProfileID, true, excluded, failedID != "")
					if retryErr == nil {
						// Same pinned-candidate guard direct play has: a retry that
						// resolved a different release than the session-bound pin
						// must not silently swap the bytes mid-stream. A failed pin
						// this serve layer just marked is an intended substitution
						// (the retry excluded it, so it can only return a sibling),
						// so the guard applies only when no failed candidate was
						// identified.
						expectedCandidateID := ""
						if parsed, err := url.Parse(file.FilePath); err == nil {
							expectedCandidateID = parsed.Query().Get("result")
						}
						if failedID == "" && expectedCandidateID != "" && retried.CandidateID != "" && retried.CandidateID != expectedCandidateID {
							if retryCleanup != nil {
								retryCleanup()
							}
							remuxErr = fmt.Errorf("retried candidate %q does not match pinned candidate %q", retried.CandidateID, expectedCandidateID)
						} else {
							releaseInput = retryCleanup
							retryURL, parseErr := url.Parse(retried.URL)
							if parseErr == nil && retryURL.Scheme == "http" {
								retryHost := retryURL.Hostname()
								if retryHost == "127.0.0.1" || retryHost == "::1" || retryHost == "[::1]" {
									inputPath = retried.URL
									deliveredPath = resolvedVirtualCandidatePath(retried)
									remuxErr = serveRemux()
								}
							}
						}
					}
				}
			}
			if remuxErr != nil {
				h.handleTransportStartFailure(r.Context(), session, file, remuxErr)
				// The remux defers pre-body start failures, so with that option set
				// nothing has committed a status and the v2 writer is still
				// unlocked; a mid-stream failure returns nil instead. Commit one
				// coherent error here. A client that disconnected needs no body.
				if !isClientCancellation(r.Context(), remuxErr) && !stoppedServerSide() {
					// The remux produced nothing: a provider release, a relay, or
					// this server's own spawn. That is a dependency failure the
					// client may retry, not a verdict about the file, so keep the
					// v1 502 the byte-delivery adapter maps to dependency_unavailable
					// and carry the same Retry-After the resolve failure above sends.
					// Without it a 503 delivery answer reaches the client with no
					// delay to back off by.
					writeRemuxStartFailure(w)
				}
			}
		}

	case playback.PlayTranscode:
		writeError(w, http.StatusBadRequest, "bad_request",
			"Transcode streams use manifest/segment endpoints")

	default:
		writeError(w, http.StatusInternalServerError, "internal_error",
			"Unknown play method")
	}
}

// writeRemuxStartFailure answers a remux that produced no media byte. It keeps
// the v1 502 a byte-delivery adapter maps to a retryable dependency_unavailable
// — the failure says nothing about the file — and carries the same Retry-After
// the virtual resolve refusal sends, so a client that retries the stream has a
// delay to back off by. A 503 delivery answer used to arrive without one.
//
// The body is the standard v1 error envelope with a machine-readable code. A
// plain-text body left the byte-delivery adapter with nothing to name the
// failure by, so its request log recorded code=internal_error for a provider or
// relay refusal and made a dependency outage look like a server defect. The code
// is additive: the 502 and its dependency_unavailable problem type are
// unchanged.
func writeRemuxStartFailure(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(virtualServeResolveRetryAfterSeconds))
	writeError(w, http.StatusBadGateway, "remux_start_failed",
		"Failed to start the remux; the stream produced no media. Retry after the advertised delay.")
}

// loadSidecarSession resolves the session a subtitle or font request names.
// It reconstructs from the signed stream reference after a restart or on a
// replica that never served the media, and checks account and selected-profile
// ownership before exposing a sidecar.
func (h *StreamHandler) loadSidecarSession(ctx context.Context, reference, sessionID string, userID int) (*playback.Session, *streamtoken.Claims, error) {
	card, claims := verifiedStreamCardFromToken(reference, sessionID, h.JWTSecret)
	loadCard := card
	if _, err := h.sessionMgr.GetSession(sessionID); err == nil {
		loadCard = nil
	} else if !errors.Is(err, playback.ErrSessionNotFound) {
		loadCard = nil
	} else if loadCard == nil {
		// A header-authenticated session's sidecar URLs carry no reference either.
		var recipeErr error
		if loadCard, recipeErr = h.storedSessionRecipeV3(ctx, sessionID, userID); recipeErr != nil {
			return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load playback session")
		}
	}
	session, status, _, sessionErr := h.TM.LoadOrReconstructSessionDetailWithError(ctx, h.sessionMgr.GetSession, sessionID, userID, loadCard)
	switch status {
	case playback.SessionMissing:
		return nil, nil, apiError(http.StatusNotFound, playbackSessionNotFoundErrorCode, "Playback session not found")
	case playback.SessionLoadFailed:
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load playback session").WithCause(sessionErr)
	case playback.SessionForbidden:
		return nil, nil, apiError(http.StatusForbidden, "forbidden", "Session belongs to another user")
	case playback.SessionUnauthorized:
		return nil, nil, apiError(http.StatusUnauthorized, "unauthorized", "Authentication required")
	}
	if session == nil {
		return nil, nil, apiError(http.StatusNotFound, playbackSessionNotFoundErrorCode, "Playback session not found")
	}
	if profileID := apimw.GetProfileID(ctx); profileID != "" && session.ProfileID != "" && profileID != session.ProfileID {
		return nil, nil, apiError(http.StatusForbidden, "forbidden", "Session belongs to another profile")
	}
	return session, claims, nil
}

// HandleSubtitle extracts a subtitle track from the media file associated with
// a playback session and serves it as WebVTT or raw ASS depending on the
// URL extension (e.g. /subtitles/2.ass or /subtitles/2.vtt).
func (h *StreamHandler) handleSubtitle(w http.ResponseWriter, r *http.Request) {
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
	if h.StreamDeny.Denied(r.Context(), sessionID) {
		writePlaybackSessionEnded(w)
		return
	}

	trackParam := chi.URLParam(r, "track")
	trackIndex, requestedFormat, err := playback.ParseSubtitleTrackParam(trackParam)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid subtitle track index")
		return
	}

	session, claims, err := h.loadSidecarSession(r.Context(), r.URL.Query().Get(streamTokenParam), sessionID, userID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	attachPlaybackSession(r.Context(), session, claims)

	if session.UserID != userID {
		writeError(w, http.StatusForbidden, "forbidden", "Session belongs to another user")
		return
	}

	fileID, err := h.acceptedSubtitleSourceFileID(r.Context(), r.URL.Query().Get("file_id"), session)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	file, err := h.fileResolver.GetByID(r.Context(), fileID)
	if err != nil || file == nil {
		writeError(w, http.StatusNotFound, "not_found", "Media file not found")
		return
	}

	// Capture the catalog row's full subtitle layout before the session
	// overlay. A virtual release can rotate between planning and extraction:
	// the row is re-probed against the current candidate while this session's
	// URLs still name the layout captured at plan time. When the two diverge,
	// extraction must verify the live source (and possibly re-map the plan
	// ordinal) before spawning ffmpeg, because the ordinal is only valid
	// against the pinned release's actual layout. External subtitles are part
	// of that ordinal space (they precede the embedded segment), so the
	// comparison covers them too.
	rowEmbedded := file.SubtitleTracks
	rowExternal := file.ExternalSubtitles

	// Resolve the source file this request actually serves from. A subtitle
	// URL may name the session's requested (old) edition after an edition
	// switch moved the effective file; such a request is foreign, and its
	// ordinal must be re-mapped onto the effective inventory rather than
	// interpreted against a version no longer playing. Virtual sources bind to
	// the session's planned candidate at the same time.
	//
	// The named row is captured first because the resolver returns a nil file on
	// failure, and the refusal has to name the release the request was minted
	// for, not the row it failed to resolve.
	namedForRefusal := file
	file, trackIndex, err = h.resolveSubtitleSourceRequest(r.Context(), file, session, trackIndex, r.URL.Query())
	if err != nil {
		if errors.Is(err, errSubtitleSourceChangedAfterRotation) {
			// The rotation already proved this ordinal cannot be carried. Fail
			// closed before any source resolve or ffmpeg spawn; the client
			// replans and re-selects from the rotated inventory.
			writeSubtitleSourceChanged(w)
			return
		}
		if errors.Is(err, errSubtitleIdentityInvalid) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		// A track identity that names no track on either inventory is a 404 for
		// a stable file, and a rotation verdict when the URL was minted for a
		// release the session no longer serves (a substituted sibling or an
		// edition switch). Answering 404 there strands the client on a URL it
		// can never repair; the 409 is the retryable answer that sends it back
		// to replan and re-select from the effective row's inventory.
		if subtitleRequestNamesAnotherRelease(session, namedForRefusal) {
			writeSubtitleSourceChanged(w)
			return
		}
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}

	// driftSuspected means the layout this request's plan-time ordinal was
	// minted against may differ from the release now being served, so the
	// pre-spawn live-layout probe must run before ffmpeg is spawned. It is
	// true when the mutable catalog row no longer matches the plan-time
	// evidence, and also when the carried evidence provably belongs to a
	// different candidate than the session is bound to (the serve layer
	// rotated without updating the evidence) — in that case the plan ordinal
	// is untrustworthy against every candidate on hand.
	evidenceTrusted := session.VirtualSubtitleEvidenceSet && virtualEvidenceMatchesBoundFile(r.Context(), file, session, h.fileResolver)
	driftSuspected := isVirtualPlaybackFile(file) &&
		session.VirtualSourceURI != "" &&
		session.VirtualSubtitleEvidenceSet &&
		(!evidenceTrusted || !playback.SubtitleLayoutsEqualIncludingExternal(
			rowEmbedded, rowExternal, session.VirtualSubtitleTracks, session.VirtualExternalSubtitles))

	// trackIndex is a combined ordinal, resolved through the same three
	// consecutive ranges playback.BuildSubtitleInventoryV3 assigns them from:
	// externals, then embedded container tracks, then downloaded ones. The
	// ranges cover the full track arrays — including bitmap tracks that have no
	// sidecar shape — so an ordinal always names the same track here as it does
	// in the published inventory.
	// Downloaded subtitle URLs additionally bind that ordinal to a stable row
	// identity. The path ordinal remains for compatibility and display, but it
	// must not be re-resolved against a mutable inventory after a seek reanchor.
	if rawID := strings.TrimSpace(r.URL.Query().Get(playback.DownloadedSubtitleIDParamV3)); rawID != "" {
		downloadedID, parseErr := strconv.Atoi(rawID)
		if parseErr != nil || downloadedID <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid downloaded subtitle identity")
			return
		}
		if h.SubtitleRepo == nil || h.SubtitleBlobs == nil {
			writeError(w, http.StatusNotFound, "not_found", "Subtitle track not found")
			return
		}
		downloaded, lookupErr := h.SubtitleRepo.GetDownloadedSubtitle(r.Context(), downloadedID)
		if lookupErr != nil {
			slog.ErrorContext(r.Context(), "get downloaded subtitle failed", "component", "api",
				"file_id", file.ID,
				"downloaded_subtitle_id", downloadedID,
				"error", lookupErr,
			)
			writeErrorCause(w, http.StatusInternalServerError, "internal_error", "Failed to load downloaded subtitle", lookupErr)
			return
		}
		if downloaded == nil || downloaded.MediaFileID != file.ID {
			writeError(w, http.StatusNotFound, "not_found", "Subtitle track not found")
			return
		}
		if r.Method == http.MethodHead {
			writeSubtitleRepresentationHead(w, subtitleRepresentationFormat(requestedFormat, servesOriginalSubRip(r, string(downloaded.Format), requestedFormat)))
			return
		}
		h.serveDownloadedSubtitle(w, r, *downloaded, requestedFormat)
		return
	}
	externalCount := len(file.ExternalSubtitles)
	if trackIndex < externalCount {
		sub := file.ExternalSubtitles[trackIndex]
		if !subtitleSidecarFormatSupported(sub.Format, requestedFormat, false) {
			writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type",
				"Requested subtitle extension does not match the selected track")
			return
		}
		if r.Method == http.MethodHead {
			writeSubtitleRepresentationHead(w, subtitleRepresentationFormat(requestedFormat, servesOriginalSubRip(r, sub.Format, requestedFormat)))
			return
		}

		// A sidecar's timing correction can change behind the same URL.
		w.Header().Set("Cache-Control", "private, no-cache")
		if subtitles.SupportsRetime(subtitles.SubtitleFormat(sub.Format)) {
			data, err := playback.LoadExternalSubtitle(r.Context(), h.ExternalTimings, file.ID, sub)
			if err != nil {
				slog.ErrorContext(r.Context(), "load sidecar subtitle failed", "component", "api",
					"file_id", file.ID, "error", err)
				writeErrorCause(w, http.StatusInternalServerError, "internal_error",
					"Failed to load external subtitle", err)
				return
			}
			h.subtitlePlayed(r, subtitles.SyncTarget{MediaFileID: file.ID, ExternalPath: sub.Path})
			h.serveSubtitleData(w, r, sub.Format, data, requestedFormat)
			return
		}

		// Other formats (MicroDVD .sub) cannot be retimed; ffmpeg converts
		// them from the file.
		vttData, err := playback.LoadExternalSubtitleAsVTT(r.Context(), sub, h.ffmpegPath())
		if err != nil {
			writeErrorCause(w, http.StatusInternalServerError, "internal_error",
				"Failed to load external subtitle", err)
			return
		}
		serveSubtitleVTT(w, vttData)
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
		if !subtitleSidecarFormatSupported(track.Codec, requestedFormat, true) {
			writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type",
				"Requested subtitle extension does not match the selected track")
			return
		}
		if r.Method == http.MethodHead && requestedFormat != subtitleFormatSUP {
			// Embedded text streams as WebVTT whatever the extension.
			writeSubtitleRepresentationHead(w, subtitleRepresentationFormat(requestedFormat, false))
			return
		}

		// Dedicated streaming extract — ffmpeg seeks to the current
		// playback position and pipes cues to the response as they're
		// demuxed, so the first byte lands within ~1s even on network
		// storage. Works identically for direct-play, remux, and
		// transcode because it doesn't depend on any other ffmpeg.
		h.streamEmbeddedSubtitle(w, r, file, embeddedIndex, session, driftSuspected, requestedFormat)
		return
	}

	// Check downloaded subtitles (from blob storage).
	if h.SubtitleRepo != nil && h.SubtitleBlobs != nil {
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
			writeErrorCause(w, http.StatusInternalServerError, "internal_error", "Failed to list downloaded subtitles", err)
			return
		}

		downloadedIndex := embeddedIndex - len(file.SubtitleTracks)
		if downloadedIndex >= 0 && downloadedIndex < len(downloaded) {
			if r.Method == http.MethodHead {
				writeSubtitleRepresentationHead(w, subtitleRepresentationFormat(requestedFormat, servesOriginalSubRip(r, string(downloaded[downloadedIndex].Format), requestedFormat)))
				return
			}
			h.serveDownloadedSubtitle(w, r, downloaded[downloadedIndex], requestedFormat)
			return
		}
	}

	// An ordinal past every segment of the bound row's inventory is the same
	// rotation verdict as an unresolvable identity pin: the URL was minted for
	// another release's layout, so the client must replan rather than treat the
	// track as gone.
	if subtitleRequestNamesAnotherRelease(session, file) {
		writeSubtitleSourceChanged(w)
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "Subtitle track not found")
}

// subtitleRequestNamesAnotherRelease reports whether this request's subtitle
// ordinal or identity pin was minted against a release the session no longer
// serves: a substituted virtual candidate (the plan rotated 41255/41257 to a
// live sibling) or an edition switch.
//
// Such a request cannot be repaired against the bound row — the ordinal is an
// index into another inventory, and the pin names another container's stream —
// so a miss is a rotation verdict, not a missing track. The answer is the
// existing retryable 409 that makes the client replan and re-select from the
// effective inventory, which is exactly what a 404 never does.
func subtitleRequestNamesAnotherRelease(session *playback.Session, file *models.MediaFile) bool {
	if session == nil || file == nil {
		return false
	}
	if session.RequestedMediaFileID > 0 && session.MediaFileID > 0 &&
		session.MediaFileID != session.RequestedMediaFileID {
		return true
	}
	return session.VirtualSubtitleEvidenceSet &&
		session.VirtualSubtitleEvidenceFileID > 0 &&
		file.ID != session.VirtualSubtitleEvidenceFileID
}

func (h *StreamHandler) serveDownloadedSubtitle(w http.ResponseWriter, r *http.Request, subtitle subtitles.DownloadedSubtitle, requestedFormat string) {
	if !subtitleSidecarFormatSupported(string(subtitle.Format), requestedFormat, false) {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type",
			"Requested subtitle extension does not match the selected track")
		return
	}
	data, err := h.SubtitleBlobs.Get(r.Context(), subtitle.S3Key)
	if err != nil {
		writeErrorCause(w, http.StatusBadGateway, "s3_error", "Failed to load subtitle from storage", err)
		return
	}
	// Both routes to a stored subtitle (the pinned ID and the ordinal) are
	// plays; callers answer HEAD requests before this.
	h.subtitlePlayed(r, subtitles.SyncTarget{MediaFileID: subtitle.MediaFileID, StoredID: subtitle.ID})
	// The stored timing correction can change behind the same URL; a player
	// refetching after a sync or reset must not get a cached copy.
	w.Header().Set("Cache-Control", "private, no-cache")
	// Apply the stored timing correction to the original bytes, before any
	// conversion; a per-request timestamp_offset still stacks on top.
	data, err = subtitles.DeliveryBytes(&subtitle, data)
	if err != nil {
		slog.ErrorContext(r.Context(), "apply downloaded subtitle timing failed", "component", "api",
			"downloaded_subtitle_id", subtitle.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to prepare subtitle")
		return
	}
	h.serveSubtitleData(w, r, string(subtitle.Format), data, requestedFormat)
}

// serveSubtitleData answers a subtitle request from text subtitle bytes that
// already carry their timing correction: ASS/SSA raw, and SRT as stored when
// the URL asks for .srt; anything else as WebVTT.
func (h *StreamHandler) serveSubtitleData(w http.ResponseWriter, r *http.Request, format string, data []byte, requestedFormat string) {
	if servesOriginalSubRip(r, format, requestedFormat) {
		serveOriginalSubRip(w, data)
		return
	}
	if playback.IsASS(format) && requestedFormat != subtitleFormatVTTV3 {
		playback.ServeSubtitle(w, data, subtitleFormatASS)
		return
	}

	// If the subtitle is already VTT, serve directly.
	if subtitles.SubtitleFormat(strings.ToLower(format)) == subtitles.FormatVTT {
		serveSubtitleVTT(w, data)
		return
	}

	// Convert other text formats to VTT using the playback conversion pipeline.
	vttData, err := playback.ConvertToVTTWithFFmpeg(r.Context(), data, format, h.ffmpegPath())
	if err != nil {
		writeErrorCause(w, http.StatusInternalServerError, "convert_error", "Failed to convert subtitle", err)
		return
	}
	serveSubtitleVTT(w, vttData)
}

// subtitleSidecarFormatSupported keeps bitmap and styled-text requests within
// the representations the server can produce. Plain text tracks preserve the
// v1 endpoint's permissive extension behavior and are always returned as VTT;
// ASS/SSA may also be served losslessly, and only an embedded PGS track has a
// binary .sup representation.
func subtitleSidecarFormatSupported(codec, requestedFormat string, embeddedPGS bool) bool {
	requestedFormat = strings.ToLower(strings.TrimSpace(requestedFormat))
	if requestedFormat == "" {
		return true
	}
	if playback.IsPGS(codec) {
		return embeddedPGS && requestedFormat == subtitleFormatSUP
	}
	if playback.NeedsBurnIn(codec) {
		return false
	}
	if playback.IsASS(codec) {
		return requestedFormat == subtitleFormatASS || requestedFormat == subtitleFormatSSA || requestedFormat == "vtt"
	}
	return true
}

// servesOriginalSubRip reports whether a sidecar request is answered with the
// original SRT bytes: only a SubRip track requested through /api/v2 as .srt
// with original=1, which is the URL subrip_sidecar_v1 publishes. Every other
// request for a SubRip track, including any on the frozen /api/v1 route, keeps
// the historical WebVTT response.
func servesOriginalSubRip(r *http.Request, codec, requestedFormat string) bool {
	return playback.IsSubRip(codec) &&
		strings.EqualFold(strings.TrimSpace(requestedFormat), subtitleFormatSRT) &&
		r.URL.Query().Get(playback.SubtitleOriginalParamV3) == "1" &&
		isNativeAPIV2(r.Context())
}

// serveSubtitleVTT writes a sidecar or downloaded subtitle as WebVTT. Files
// written for left-to-right players get their right-to-left lines marked so
// the punctuation lands where the author put it.
func serveSubtitleVTT(w http.ResponseWriter, data []byte) {
	playback.ServeSubtitle(w, subtitles.MarkLTRAuthoredLines(data), "vtt")
}

// serveOriginalSubRip writes stored SRT bytes as they are. SRT declares no
// encoding, so the response claims UTF-8 only when the bytes are valid UTF-8;
// a legacy-encoded file is labeled without a charset rather than mislabeled.
func serveOriginalSubRip(w http.ResponseWriter, data []byte) {
	contentType := subtitleMIMESubRip
	if utf8.Valid(data) {
		contentType += "; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	w.Write(data) //nolint:errcheck
}

// subtitleRepresentationFormat names the representation a GET would serve, so
// a HEAD reports the same content type: a .srt request that is not answered
// with the original SRT is answered with WebVTT.
func subtitleRepresentationFormat(requestedFormat string, servesOriginalSRT bool) string {
	if strings.EqualFold(strings.TrimSpace(requestedFormat), subtitleFormatSRT) && !servesOriginalSRT {
		return "vtt"
	}
	return requestedFormat
}

func writeSubtitleRepresentationHead(w http.ResponseWriter, requestedFormat string) {
	switch strings.ToLower(strings.TrimSpace(requestedFormat)) {
	case subtitleFormatASS, subtitleFormatSSA:
		w.Header().Set("Content-Type", "text/x-ssa; charset=utf-8")
	case subtitleFormatSRT:
		// HEAD does not read the stored bytes, so it cannot vouch for UTF-8.
		w.Header().Set("Content-Type", subtitleMIMESubRip)
	case subtitleFormatSUP:
		w.Header().Set("Content-Type", "application/octet-stream")
	default:
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	}
	w.WriteHeader(http.StatusOK)
}

// subtitleSourceFileID pins a subtitle URL to the file whose track list was
// used to create it. A quality/seek restart may change session.MediaFileID to
// an alternate version; interpreting the old combined track index against the
// alternate file can silently serve a different language. Only the session's
// requested or current effective file may be named by the authenticated URL.
func subtitleSourceFileID(r *http.Request, session *playback.Session) (int, error) {
	return subtitleSourceFile(r.URL.Query().Get("file_id"), session)
}

func subtitleSourceFile(raw string, session *playback.Session) (int, error) {
	if session == nil {
		return 0, errors.New("playback session is required")
	}
	raw = strings.TrimSpace(raw)
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

// acceptedSubtitleSourceFileID is subtitleSourceFileID with the virtual-session
// window widened. A virtual session's subtitle URLs embed the effective file id
// its inventory advertised, which for an adopted provider candidate is that
// candidate's catalog row — not the session's provider-neutral row. The strict
// session.MediaFileID/RequestedMediaFileID window therefore rejects the very URL
// the session published. Accept a row when it names the session's bound
// candidate; every other unrelated row still fails closed.
func (h *StreamHandler) acceptedSubtitleSourceFileID(ctx context.Context, raw string, session *playback.Session) (int, error) {
	fileID, err := subtitleSourceFile(raw, session)
	if err == nil {
		return fileID, nil
	}
	raw = strings.TrimSpace(raw)
	if session == nil || raw == "" || h == nil || h.fileResolver == nil {
		return 0, err
	}
	candidateURI := strings.TrimSpace(session.VirtualSourceURI)
	if candidateURI == "" {
		return 0, err
	}
	requested, convErr := strconv.Atoi(raw)
	if convErr != nil || requested <= 0 {
		return 0, err
	}
	row, lookupErr := h.fileResolver.GetByID(ctx, requested)
	if lookupErr != nil || row == nil {
		return 0, err
	}
	if !isVirtualPlaybackFile(row) {
		return 0, err
	}
	// The row the session captured its evidence from is citable even after it
	// was renumbered, but an evidence row id is not a content fingerprint: an
	// in-place enrichment re-probe keeps the id while rewriting the path to a
	// different result. The URL was minted from that row's advertised
	// inventory, so it is admitted only while the row still names the
	// session's live bound candidate.
	namesBoundCandidate := sameVirtualCandidate(candidateURI, row.FilePath)
	if evidenceID := session.VirtualSubtitleEvidenceFileID; evidenceID > 0 && requested == evidenceID && namesBoundCandidate {
		return requested, nil
	}
	if namesBoundCandidate {
		return requested, nil
	}
	// A legacy session with only an evidence URI still admits a row that names
	// its evidence candidate, but only while the evidence URI agrees with the
	// live binding. A stale evidence anchor from a previous release must not
	// widen the window after the session rotated.
	evidenceURI := strings.TrimSpace(session.VirtualSubtitleEvidenceURI)
	if evidenceURI != "" && sameVirtualCandidate(candidateURI, evidenceURI) && sameVirtualCandidate(evidenceURI, row.FilePath) {
		return requested, nil
	}
	return 0, err
}

// SubtitleFontRequest identifies the session and frozen subtitle inventory
// selection. Query preserves repeated identity parameters so validation cannot
// silently choose one of conflicting stream pins.
type SubtitleFontRequest struct {
	SessionID string
	Track     string
	Query     url.Values
}

// SubtitleFonts loads a bounded bundle of embedded ASS/SSA fonts. Both API
// transports use this operation so reconstruction, deny markers and source-file
// admission stay identical without invoking another transport's HTTP handler.
func (h *StreamHandler) SubtitleFonts(ctx context.Context, in SubtitleFontRequest) ([]playback.SubtitleFontBundleItem, bool, error) {
	items, pending, _, err := h.SubtitleFontsWithFallback(ctx, in)
	return items, pending, err
}

// SubtitleFontsWithFallback is SubtitleFonts plus the explicit system-font
// fallback reason. When subsetting an oversized font pack was the only path to
// a bundle and failed, fallback names playback.SubtitleFontFallbackSubsetUnavailable;
// it is empty for a definitive bundle, an empty file, or a transient in-flight
// extraction. Transports put it on the response so a client can tell a real
// system-font fallback from an ordinary empty bundle.
func (h *StreamHandler) SubtitleFontsWithFallback(ctx context.Context, in SubtitleFontRequest) ([]playback.SubtitleFontBundleItem, bool, string, error) {
	userID := apimw.GetUserID(ctx)
	if userID == 0 {
		return nil, false, "", apiError(http.StatusUnauthorized, "unauthorized", "Authentication required")
	}
	if in.SessionID == "" {
		return nil, false, "", apiError(http.StatusBadRequest, "bad_request", "Session ID is required")
	}
	if lc := activitylog.GetPlaybackLogContext(ctx); lc != nil {
		lc.PlaybackSessionID = in.SessionID
	}
	if h.StreamDeny.Denied(ctx, in.SessionID) {
		return nil, false, "", apiError(http.StatusGone, playbackSessionEndedErrorCode, "Playback session has ended")
	}
	session, claims, err := h.loadSidecarSession(ctx, in.Query.Get(streamTokenParam), in.SessionID, userID)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status >= http.StatusInternalServerError {
			cause := apiErr.cause
			if cause == nil {
				cause = apiErr
			}
			h.logSubtitleFontInternalError(ctx, in, 0, -1, "session_load", cause)
		}
		return nil, false, "", err
	}
	attachPlaybackSession(ctx, session, claims)

	fileID, err := h.acceptedSubtitleSourceFileID(ctx, in.Query.Get("file_id"), session)
	if err != nil {
		return nil, false, "", apiError(http.StatusBadRequest, "bad_request", err.Error())
	}
	file, err := h.fileResolver.GetByID(ctx, fileID)
	if err != nil || file == nil {
		return nil, false, "", apiError(http.StatusNotFound, "not_found", "Media file not found")
	}

	trackIndex, _, err := playback.ParseSubtitleTrackParam(in.Track)
	if err != nil {
		return nil, false, "", apiError(http.StatusBadRequest, "bad_request", "Invalid subtitle track index")
	}
	file, trackIndex, err = h.resolveSubtitleSourceRequest(ctx, file, session, trackIndex, in.Query)
	if err != nil {
		if errors.Is(err, errSubtitleSourceChangedAfterRotation) {
			return nil, false, "", apiError(http.StatusConflict, "subtitle_source_changed",
				"The selected subtitle track changed on the media source; retry")
		}
		if errors.Is(err, errSubtitleIdentityInvalid) {
			return nil, false, "", apiError(http.StatusBadRequest, "bad_request", err.Error())
		}
		return nil, false, "", apiError(http.StatusNotFound, "not_found", err.Error())
	}
	// Source admission comes first: a missing local file must answer 404 and
	// end the session regardless of the codec, and only a genuinely
	// inaccessible file answers internal_error. (Virtual rows short-circuit
	// preflight, so their codec refusal below still answers without any
	// provider round-trip.)
	if err := preflightPlaybackFile(ctx, file, h.MissingMarker, h.EventsHub); err != nil {
		if isPlaybackFileMissing(err) {
			h.abortPlaybackSession(ctx, session)
			return nil, false, "", apiError(http.StatusNotFound, "not_found", "Source media file is missing")
		}
		h.logSubtitleFontInternalError(ctx, in, file.ID, trackIndex, "source_preflight", err)
		// A legitimate ASS track whose source cannot be read right now is a
		// retryable dependency failure, not a server defect. Fonts are
		// optional, so never turn a transient source-access failure into a 500.
		return nil, false, "", apiError(http.StatusServiceUnavailable, subtitleSourceUnavailableErrorCode, "Failed to access source media file")
	}
	// Validate the requested ordinal and codec before extraction. A non-ASS
	// ordinal or one outside the embedded range is a client error no retry
	// can satisfy; it answers 4xx here and never reaches the (500-capable)
	// extraction.
	embeddedIndex := trackIndex - len(file.ExternalSubtitles)
	if embeddedIndex < 0 || embeddedIndex >= len(file.SubtitleTracks) {
		return nil, false, "", apiError(http.StatusNotFound, "not_found", "Embedded subtitle track not found")
	}
	if !playback.IsASS(file.SubtitleTracks[embeddedIndex].Codec) {
		return nil, false, "", apiError(http.StatusBadRequest, "bad_request", "Subtitle font bundles are only available for ASS/SSA tracks")
	}

	// Virtual rows resolve through the relay inside the shared core: a
	// provider-neutral virtual:// URI is not an FFmpeg input, so probing it
	// directly always fails with a 500 the client then retries in a storm.
	items, pending, fallback, err := h.fontBundleForRequest(ctx, session, file, trackIndex)
	if err != nil {
		if errors.Is(err, errVirtualFontResolve) {
			logVirtualStreamFailure(ctx, session.ID, file, err)
			return nil, false, "", apiError(http.StatusBadGateway, subtitleSourceUnavailableErrorCode, "Failed to resolve virtual source for the subtitle font bundle")
		}
		// Throttle logging already happened in the shared core.
		return nil, false, "", apiError(http.StatusInternalServerError, "font_extract_failed", "Failed to extract subtitle fonts")
	}
	return items, pending, fallback, nil
}

// cachedFontBundleForRequest serves one admitted ASS track's attached-font
// bundle through the subtitle cache: disk hit, failure-backoff pending, or a
// detached single-flight extraction the caller waits on up to
// fontBundleClientWait. Extracting it keeps fontBundleForRequest within its
// nesting budget; behavior is unchanged.
func (h *StreamHandler) cachedFontBundleForRequest(ctx context.Context, session *playback.Session, file *models.MediaFile, trackIndex int, cacheKey playback.FontBundleKey, failKey string, failAttrs []any, resolveInput func(extractCtx context.Context) (string, func(), error)) ([]playback.SubtitleFontBundleItem, bool, string, error) {
	if cached, ok := h.SubtitleCache.LookupFontBundle(cacheKey); ok {
		h.fontExtractFailures.recovered(failKey)
		var items []playback.SubtitleFontBundleItem
		if err := json.Unmarshal(cached, &items); err != nil {
			return nil, false, "", err
		}
		return items, false, "", nil
	}
	// A recent extraction failure is retried only after its backoff. Fonts
	// are optional to playback, so the request answers the same pending
	// empty bundle instead of spawning another provider read or ffmpeg.
	if retryAfter, cooling := h.SubtitleCache.FontBundleRetryAfter(cacheKey); cooling {
		slog.DebugContext(ctx, "subtitle font bundle extraction in backoff",
			"component", "api", "file_id", file.ID, "track", trackIndex,
			"retry_after", retryAfter)
		return nil, true, h.SubtitleCache.FontBundleFallback(cacheKey), nil
	}
	bundle, ready, err := h.SubtitleCache.ExtractFontBundleWithin(ctx, cacheKey, fontBundleClientWait, func(extractCtx context.Context) ([]byte, error) {
		inputPath, releaseInput, resolveErr := resolveInput(extractCtx)
		if resolveErr != nil {
			return nil, resolveErr
		}
		defer releaseInput()
		fonts, extractErr := playback.ExtractAttachedSubtitleFonts(extractCtx, inputPath, h.ffmpegPath())
		if extractErr != nil {
			// This closure is the only place that sees a failure which
			// outlives the request that started it: the caller has already
			// been handed a pending bundle by the time the detached flight
			// fails.
			h.logFontBundleExtractFailure(extractCtx, file, trackIndex, extractErr)
			return nil, extractErr
		}
		return json.Marshal(playback.EncodeSubtitleFontBundle(fonts))
	})
	if err != nil {
		if errors.Is(err, errVirtualFontResolve) {
			logVirtualStreamFailure(ctx, session.ID, file, err)
			// Provider resolution is a retryable dependency failure; keep
			// its 502 rather than inventing an empty bundle for a source
			// that was never read.
			return nil, false, "", err
		}
		// The extraction closure already logged this failure through the
		// shared throttle, so repeating it here would only add a duplicate
		// line for the same target.
		//
		// A legitimate ASS track whose optional font bundle could not be
		// extracted is not a server error: degrade to the pending empty
		// bundle so the player uses default fonts and re-fetches after the
		// cache's failure backoff instead of hammering a 500.
		fallback := ""
		if errors.Is(err, playback.ErrSubtitleFontSubsetUnavailable) {
			fallback = playback.SubtitleFontFallbackSubsetUnavailable
		}
		return nil, true, fallback, nil
	}
	if !ready {
		return nil, true, h.SubtitleCache.FontBundleFallback(cacheKey), nil
	}
	h.fontExtractFailures.recovered(failKey)
	var items []playback.SubtitleFontBundleItem
	if err := json.Unmarshal(bundle, &items); err != nil {
		return nil, false, "", err
	}
	return items, false, "", nil
}

// fontBundleForRequest resolves one already-admitted embedded ASS track's
// attached-font bundle through the warmed cache shared with the v1 route and
// the playback pre-warm. The third result is an explicit system-font fallback
// reason when subsetting was the only path to a bundle and failed; empty
// otherwise. A cache hit serves immediately; otherwise a detached
// single-flight extracts while the caller waits up to fontBundleClientWait and
// then receives pending=true. Virtual inputs are resolved through the relay —
// a provider-neutral virtual:// URI is not an FFmpeg input, so probing it
// directly always fails. Virtual keys are stable without a pinned result=: the
// identity follows the probed subtitle layout, so the returned pending flag is
// the only "not definitive yet" signal the caller needs.
func (h *StreamHandler) fontBundleForRequest(ctx context.Context, session *playback.Session, file *models.MediaFile, trackIndex int) ([]playback.SubtitleFontBundleItem, bool, string, error) {
	failKey := fontExtractFailureKey(file.ID, trackIndex)
	failAttrs := []any{"file_id", file.ID, "track", trackIndex}
	virtualFontSource := isVirtualPlaybackFile(file) && session.VirtualSourceURI != ""
	cacheKey := h.fontBundleKeyForRequest(ctx, session, file)

	resolveInput := func(extractCtx context.Context) (string, func(), error) {
		inputPath := file.FilePath
		releaseInput := func() {}
		if virtualFontSource && hasVirtualMediaResolver(h) {
			resolved, cleanup, resolveErr := h.resolveVirtualInputURI(extractCtx, file, session.UserID, session.ProfileID, false)
			if resolveErr != nil {
				return "", nil, fmt.Errorf("%w: %w", errVirtualFontResolve, resolveErr)
			}
			inputPath = resolved.URL
			releaseInput = cleanup
		}
		return inputPath, releaseInput, nil
	}

	if h.SubtitleCache != nil {
		return h.cachedFontBundleForRequest(ctx, session, file, trackIndex, cacheKey, failKey, failAttrs, resolveInput)
	}

	// No cache configured, or an uncacheable virtual key: extract uncached
	// within the request, still resolved through the relay.
	inputPath, releaseInput, err := resolveInput(ctx)
	if err != nil {
		if errors.Is(err, errVirtualFontResolve) {
			logVirtualStreamFailure(ctx, session.ID, file, err)
			return nil, false, "", err
		}
		h.fontExtractFailures.failed(ctx, failKey, append(failAttrs, "error", err)...)
		return nil, true, "", nil
	}
	defer releaseInput()
	fonts, err := playback.ExtractAttachedSubtitleFonts(ctx, inputPath, h.ffmpegPath())
	if err != nil {
		h.fontExtractFailures.failed(ctx, failKey, append(failAttrs, "error", err)...)
		fallback := ""
		if errors.Is(err, playback.ErrSubtitleFontSubsetUnavailable) {
			fallback = playback.SubtitleFontFallbackSubsetUnavailable
		}
		return nil, true, fallback, nil
	}
	h.fontExtractFailures.recovered(failKey)
	return playback.EncodeSubtitleFontBundle(fonts), false, "", nil
}

// HandleSubtitleFonts preserves the bridge API's array response and serves
// font bundles through the subtitle cache for virtual-source pre-warm.
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
	if h.StreamDeny.Denied(r.Context(), sessionID) {
		writePlaybackSessionEnded(w)
		return
	}

	session, claims, err := h.loadSidecarSession(r.Context(), r.URL.Query().Get(streamTokenParam), sessionID, userID)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status >= http.StatusInternalServerError {
			cause := apiErr.cause
			if cause == nil {
				cause = apiErr
			}
			h.logSubtitleFontInternalError(r.Context(), SubtitleFontRequest{
				SessionID: sessionID,
				Track:     chi.URLParam(r, "track"),
				Query:     r.URL.Query(),
			}, 0, -1, "session_load", cause)
		}
		writeAPIError(w, err)
		return
	}
	attachPlaybackSession(r.Context(), session, claims)

	if session.UserID != userID {
		writeError(w, http.StatusForbidden, "forbidden", "Session belongs to another user")
		return
	}

	fileID, err := h.acceptedSubtitleSourceFileID(r.Context(), r.URL.Query().Get("file_id"), session)
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

	trackParam := chi.URLParam(r, "track")
	trackIndex, _, err := playback.ParseSubtitleTrackParam(trackParam)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid subtitle track index")
		return
	}
	// Resolve the file/ordinal actually served: re-map an edition-switch
	// request that names the old edition, then bind the session's planned
	// virtual candidate so the same release the plan probed is extracted.
	file, trackIndex, err = h.resolveSubtitleSourceRequest(r.Context(), file, session, trackIndex, r.URL.Query())
	if err != nil {
		if errors.Is(err, errSubtitleSourceChangedAfterRotation) {
			writeSubtitleSourceChanged(w)
			return
		}
		if errors.Is(err, errSubtitleIdentityInvalid) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		} else {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
		}
		return
	}
	// Source admission comes first (same ordering as SubtitleFonts): a
	// missing local file answers 404 and ends the session regardless of the
	// codec; a genuinely inaccessible file answers internal_error. Virtual
	// rows short-circuit preflight, so their codec refusal below still
	// answers without a provider round-trip.
	if err := preflightPlaybackFile(r.Context(), file, h.MissingMarker, h.EventsHub); err != nil {
		if isPlaybackFileMissing(err) {
			h.abortPlaybackSession(r.Context(), session)
			writePlaybackFilePreflightError(w, err)
			return
		}
		h.logSubtitleFontInternalError(r.Context(), SubtitleFontRequest{
			SessionID: sessionID,
			Track:     trackParam,
			Query:     r.URL.Query(),
		}, file.ID, trackIndex, "source_preflight", err)
		// Same as the v2 path: a legitimate track whose source is temporarily
		// inaccessible answers retryable, never a 500.
		writeSubtitleSourceUnavailable(w)
		return
	}
	// Validate the requested ordinal and codec before extraction, so a
	// permanently unsatisfiable request (non-ASS ordinal, ordinal outside the
	// embedded range) answers 4xx and never reaches the 500-capable
	// extraction.
	embeddedIndex := trackIndex - len(file.ExternalSubtitles)
	if embeddedIndex < 0 || embeddedIndex >= len(file.SubtitleTracks) {
		writeError(w, http.StatusNotFound, "not_found", "Embedded subtitle track not found")
		return
	}
	if !playback.IsASS(file.SubtitleTracks[embeddedIndex].Codec) {
		writeError(w, http.StatusBadRequest, "bad_request", "Subtitle font bundles are only available for ASS/SSA tracks")
		return
	}

	// Build the font-bundle cache key before any virtual resolve: the identity
	// must never depend on the resolved relay URL, which rotates per
	// registration. Shared with the playback pre-warm so the two cannot drift.
	virtualFontSource := isVirtualPlaybackFile(file) && session.VirtualSourceURI != ""
	cacheKey := h.fontBundleKeyForRequest(r.Context(), session, file)

	// A cache hit serves the encoded bundle immediately: no provider round-trip,
	// no relay registration, no ffmpeg spawn.
	if h.SubtitleCache != nil {
		if cached, ok := h.SubtitleCache.LookupFontBundle(cacheKey); ok {
			// A detached extraction can complete between requests; serving its
			// cached bundle is confirmed recovery, so clear the throttle key
			// and let a later regression warn again.
			h.fontExtractFailures.recovered(fontExtractFailureKey(file.ID, trackIndex))
			writeFontBundleResponse(w, cached)
			return
		}
		if retryAfter, cooling := h.SubtitleCache.FontBundleRetryAfter(cacheKey); cooling {
			// The previous extraction failed; do not spawn another provider
			// read inside its backoff. The pending bundle keeps the player on
			// default fonts and tells it to re-fetch.
			slog.DebugContext(r.Context(), "subtitle font bundle extraction in backoff",
				"component", "api", "file_id", file.ID, "track", trackIndex,
				"retry_after", retryAfter)
			writePendingFontBundleResponse(w, h.SubtitleCache.FontBundleFallback(cacheKey))
			return
		}
	}

	if h.SubtitleCache == nil {
		// No cache configured: keep the historical uncached path, resolved and
		// released within the request.
		inputPath := file.FilePath
		releaseInput := func() {}
		if virtualFontSource && hasVirtualMediaResolver(h) {
			var resolved ResolvedVirtualMedia
			resolved, releaseInput, err = h.resolveVirtualInputURI(r.Context(), file, session.UserID, session.ProfileID, false)
			if err != nil {
				logVirtualStreamFailure(r.Context(), session.ID, file, err)
				writeError(w, http.StatusBadGateway, subtitleSourceUnavailableErrorCode, "Failed to resolve virtual source for the subtitle font bundle")
				return
			}
			inputPath = resolved.URL
		}
		defer releaseInput()

		fonts, extractErr := playback.ExtractAttachedSubtitleFonts(r.Context(), inputPath, h.ffmpegPath())
		if extractErr != nil {
			h.fontExtractFailures.failed(r.Context(), fontExtractFailureKey(file.ID, trackIndex),
				"file_id", file.ID,
				"track", trackIndex,
				"error", extractErr,
			)
			// Fonts are optional to playback; a failed extraction degrades to
			// the pending empty bundle rather than a 500 that a retrying client
			// re-issues. A subsetting failure is permanent, so mark the bundle
			// as an explicit system-font fallback.
			fallback := ""
			if errors.Is(extractErr, playback.ErrSubtitleFontSubsetUnavailable) {
				fallback = playback.SubtitleFontFallbackSubsetUnavailable
			}
			writePendingFontBundleResponse(w, fallback)
			return
		}
		h.fontExtractFailures.recovered(fontExtractFailureKey(file.ID, trackIndex))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(playback.EncodeSubtitleFontBundle(fonts)); err != nil {
			slog.WarnContext(r.Context(), "subtitle font response encode failed", "component", "api", "error", err)
		}
		return
	}

	// The detached single-flight owns the virtual relay registration: it is
	// resolved inside the extract closure and released when that closure
	// returns, so a cold HTTP-led flight that outlives the short client wait
	// still has a valid source for the whole extraction. Releasing at HTTP
	// return instead would let ffmpeg start against a relay entry that no
	// longer exists.
	localPath := file.FilePath
	bundle, ready, err := h.SubtitleCache.ExtractFontBundleWithin(r.Context(), cacheKey, fontBundleClientWait, func(extractCtx context.Context) ([]byte, error) {
		inputPath := localPath
		releaseInput := func() {}
		if virtualFontSource && hasVirtualMediaResolver(h) {
			resolved, cleanup, resolveErr := h.resolveVirtualInputURI(extractCtx, file, session.UserID, session.ProfileID, false)
			if resolveErr != nil {
				logVirtualStreamFailure(extractCtx, session.ID, file, resolveErr)
				return nil, fmt.Errorf("%w: %w", errVirtualFontResolve, resolveErr)
			}
			inputPath = resolved.URL
			releaseInput = cleanup
		}
		defer releaseInput()

		fonts, extractErr := playback.ExtractAttachedSubtitleFonts(extractCtx, inputPath, h.ffmpegPath())
		if extractErr != nil {
			h.logFontBundleExtractFailure(extractCtx, file, trackIndex, extractErr)
			return nil, extractErr
		}
		return json.Marshal(playback.EncodeSubtitleFontBundle(fonts))
	})
	if err != nil {
		if errors.Is(err, errVirtualFontResolve) {
			logVirtualStreamFailure(r.Context(), session.ID, file, err)
			writeError(w, http.StatusBadGateway, subtitleSourceUnavailableErrorCode, "Failed to resolve virtual source for the subtitle font bundle")
			return
		}
		// The extraction closure already logged this failure through the shared
		// throttle, so repeating it here would only add a duplicate line for the
		// same target. The track is legitimate; the optional font bundle is not.
		// Answer the pending empty bundle (and let the cache's backoff gate the
		// next extraction) instead of a 500.
		fallback := ""
		if errors.Is(err, playback.ErrSubtitleFontSubsetUnavailable) {
			fallback = playback.SubtitleFontFallbackSubsetUnavailable
		}
		writePendingFontBundleResponse(w, fallback)
		return
	}
	if !ready {
		// The extraction is still running detached. Hold the request only for
		// fontBundleClientWait, then hand the client a distinguishable,
		// uncacheable pending bundle so it falls back to default fonts
		// immediately and re-fetches; the single-flighted extraction keeps
		// running and lands in the cache for that next fetch. A prior failure
		// key stays set: recovery is not confirmed until a bundle is actually
		// served, so a slow retry that fails still logs at debug.
		slog.DebugContext(r.Context(), "subtitle font bundle extraction in flight; serving pending bundle",
			"component", "api", "file_id", file.ID, "track", trackIndex)
		writePendingFontBundleResponse(w, h.SubtitleCache.FontBundleFallback(cacheKey))
		return
	}
	// Recovery is confirmed by the completed bundle; only now clear the
	// throttle key so a later regression warns again.
	h.fontExtractFailures.recovered(fontExtractFailureKey(file.ID, trackIndex))
	writeFontBundleResponse(w, bundle)
}

// errVirtualFontResolve marks a font-bundle extraction failure caused by the
// virtual relay resolution rather than the extraction itself, so the HTTP
// handler can answer with the provider-resolution status instead of a generic
// extraction failure.
var errVirtualFontResolve = errors.New("virtual font bundle resolve failed")

// fontBundleClientWait bounds how long the font-bundle handler waits for a
// cold extraction before returning an empty bundle. It stays well below the
// web client's FONT_BUNDLE_BUDGET_MS (3s) so the client never waits on the
// server; the extraction continues in the background and lands in the cache.
// A cold relay bundle can take tens of seconds, which is exactly why the
// request must not block on it.
var fontBundleClientWait = time.Second

// emptyFontBundle is the valid, empty JSON bundle served when an extraction is
// still in flight or has no fonts to return.
var emptyFontBundle = []byte("[]")

// fontBundleCacheKey builds the FontBundleKey for a media file the same way the
// stream font handler does, and is shared with the playback-time pre-warm so
// the two can never drift. Virtual rows key on the release's durable provider
// identity plus the row's probed subtitle inventory because both the pinned
// result candidate and the resolved relay URL rotate per registration; an
// unchanged release and layout therefore reuse the warmed bundle, while a
// rotation to different bytes re-keys even when the subtitle layout is
// identical. Local rows key on the file row's size and mtime so a re-probed or
// replaced file reads as a miss.
func fontBundleCacheKey(file *models.MediaFile, ffmpegPath string) playback.FontBundleKey {
	if file == nil {
		return playback.FontBundleKey{FFmpegPath: ffmpegPath}
	}
	if isVirtualPlaybackFile(file) {
		return playback.FontBundleKey{
			FileID:          file.ID,
			Virtual:         true,
			ReleaseIdentity: virtualFontReleaseIdentity(file),
			InventoryToken:  playback.SubtitleInventoryToken(file.SubtitleTracks),
			FFmpegPath:      ffmpegPath,
		}
	}
	key := playback.FontBundleKey{FileID: file.ID, FFmpegPath: ffmpegPath}
	key.Size = file.FileSize
	if file.FileModifiedAt != nil {
		key.MtimeUnixNano = file.FileModifiedAt.UnixNano()
	}
	return key
}

// fontBundleKeyForRequest resolves the font-bundle key a serve path and the
// plan-time warm must agree on. A virtual session carries the key frozen when
// its plan was committed; reusing it keeps the warm readable after a background
// probe persists provider identity and inventory onto the mutable catalog row.
// The frozen key is only trusted while it still names the row being served (and,
// when the session carries evidence, while that evidence still anchors the
// bound candidate), so a candidate rotation falls back to live derivation
// instead of serving another release's fonts.
func (h *StreamHandler) fontBundleKeyForRequest(ctx context.Context, session *playback.Session, file *models.MediaFile) playback.FontBundleKey {
	if session != nil && session.VirtualFontBundleKeySet && file != nil && isVirtualPlaybackFile(file) {
		frozen := session.VirtualFontBundleKey
		if frozen.Virtual && frozen.FileID == file.ID {
			if !session.VirtualSubtitleEvidenceSet || virtualEvidenceMatchesBoundFile(ctx, file, session, h.fileResolver) {
				return frozen
			}
		}
	}
	return fontBundleCacheKey(file, h.ffmpegPath())
}

// virtualFontReleaseIdentity serializes a virtual row's durable provider
// identity in the same tiers boundRowSharesRelease compares: video hash, source
// GUID, then normalized release name and size. Fonts belong to the release, not
// to the subtitle layout: a candidate rotation (or a provider re-grab behind
// the same row) can keep an identical track list while changing the attached
// font pack or script, so the warmed bundle must not survive it. Every tier the
// row carries is folded in, and a row that carries none returns "" so the key
// keeps its historical file+inventory identity rather than inventing one.
func virtualFontReleaseIdentity(file *models.MediaFile) string {
	if file == nil {
		return ""
	}
	tiers := resolver.NewPersistedIdentityTiers(
		file.ProviderVideoHash, file.ProviderGUID, file.ProviderReleaseName, file.ProviderReleaseSize,
	)
	if tiers.VideoHash == "" && tiers.GUID == "" && tiers.ReleaseName == "" && tiers.ReleaseSize <= 0 {
		return ""
	}
	return strings.Join([]string{
		tiers.VideoHash,
		tiers.GUID,
		tiers.ReleaseName,
		strconv.FormatInt(tiers.ReleaseSize, 10),
	}, "\x1f")
}

// writeFontBundleResponse writes an encoded font-bundle payload with the
// shared cache headers. Both the cache-hit and cache-miss paths serve the same
// bytes, so the response is identical whichever path produced them.
func writeFontBundleResponse(w http.ResponseWriter, bundle []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "private, max-age=600")
	_, _ = w.Write(bundle)
}

// fontBundlePendingHeader marks a font-bundle response whose extraction is
// still in flight. The web client treats either this header or a no-store
// Cache-Control as pending: it must not persist the empty body as a definitive
// font-less result and instead re-fetches for the completed bundle.
const fontBundlePendingHeader = "X-Vio-Font-Bundle-Pending"

// fontBundleFallbackHeader marks a font-bundle response that fell back to
// system fonts because script-driven subsetting could not produce a bundle
// within the ceiling. Unlike the pending marker, this is the server's answer
// for this source: the client should not present a missing-font state as a
// still-loading bundle. Its value is the machine-readable fallback reason.
const fontBundleFallbackHeader = "X-Vio-Font-Bundle-Fallback"

// writePendingFontBundleResponse writes a valid empty bundle for an extraction
// that is still running. Unlike a definitive font-less file, it is marked
// pending and uncacheable, so a client cannot retain the empty state for ten
// minutes while the real bundle is being produced. fallback, when non-empty,
// additionally names a system-font fallback reason (see
// fontBundleFallbackHeader).
func writePendingFontBundleResponse(w http.ResponseWriter, fallback string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(fontBundlePendingHeader, "true")
	if fallback != "" {
		w.Header().Set(fontBundleFallbackHeader, fallback)
	}
	_, _ = w.Write(emptyFontBundle)
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
	ctx, cancel := teardownContext(ctx)
	defer cancel()

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
	// Teardown reached from a canceled request (a client-canceled transport or
	// a track switch aborting the old stream) must still stop the durable
	// attempt and remove the synced session. Run the durable half detached and
	// bounded instead of inheriting the dead request context.
	ctx, cancel := teardownContext(ctx)
	defer cancel()
	if err := h.sessionMgr.StopSession(session.ID); err != nil {
		return
	}
	claim := claimNativeEnd(ctx, h.PlanStoreV3, h.StreamDeny, session.ID, abortStopID(session.ID))
	h.finalizeSessionAbort(ctx, session, true, "stream_abort")
	// An earlier stop that won the row and never finished is finished as
	// that stop by the playback handler, which has its writers.
	if claim.owned && !claim.ownEnd(abortStopID(session.ID)) && h.FinishOtherEnd != nil {
		h.FinishOtherEnd(ctx, claim, session.ID)
		deleteSessionRecipeV3(ctx, h.SessionRecipeStore, session.ID)
		return
	}
	// The playback handler's finish hook writes the play's history, as it
	// does for every other end, unless another stop already owns it.
	if finisher, ok := h.sessionMgr.(detachedFinisher); ok && claim.owned {
		snapshot := *session
		claim.applyAccepted(&snapshot)
		finisher.FinishDetached(playback.WithEndReason(ctx, string(claim.reason(session.ID, PlaybackEndAborted))), snapshot)
		claim.finish(ctx, session.ID, "")
	}
	deleteSessionRecipeV3(ctx, h.SessionRecipeStore, session.ID)
}

// remuxStoppedServerSide returns a func reporting whether the progressive remux
// abort fired. The channel closes for both a client disconnect and a
// server-initiated stop, and only the request context tells them apart, so the
// closure is only trusted when the context is still live — once it is done the
// abort that follows is the client's own.
func remuxStoppedServerSide(ctx context.Context, abort <-chan struct{}) func() bool {
	if abort == nil {
		return func() bool { return false }
	}
	return func() bool {
		if ctx == nil || ctx.Err() != nil {
			return false
		}
		select {
		case <-abort:
			return true
		default:
			return false
		}
	}
}

// transportStopWatcher is the optional session-manager surface a progressive
// remux uses to learn that its session was stopped. *playback.SessionManager
// implements it; a minimal/test manager without it keeps the request-context
// behavior, in which a server-initiated stop cannot reach the response.
type transportStopWatcher interface {
	WatchTransportStop(sessionID string) (<-chan struct{}, func())
}

// watchProgressiveRemuxAbort returns a channel closed when the request ends
// (client disconnect or write failure) or the session is stopped server-side,
// plus the release that unregisters the watch. The remux is a single long
// response, so both signals must end it: the request context covers the client
// leaving, and the transport-stop watch covers StopSession and an idle reap,
// neither of which cancels an in-flight request whose client keeps reading.
//
// The helper only borrows the manager's watch; it never stops the session, so
// a client disconnect remains distinct from a server stop.
func watchProgressiveRemuxAbort(ctx context.Context, sessionMgr SessionManagerInterface, sessionID string) (<-chan struct{}, func()) {
	abort := make(chan struct{})
	// The abort channel closes at most once no matter which signal wins.
	var once sync.Once
	closer := func() { once.Do(func() { close(abort) }) }

	// The request context ends the response on a client disconnect and, more
	// sharply, interrupts a write blocked on a client that stopped reading —
	// which the remux's own write loop cannot detect until the rolling deadline.
	var stopOnRequestEnd func() bool
	if ctx != nil {
		stopOnRequestEnd = context.AfterFunc(ctx, closer)
	}

	watcher, ok := sessionMgr.(transportStopWatcher)
	if !ok || sessionID == "" {
		return abort, func() {
			if stopOnRequestEnd != nil {
				stopOnRequestEnd()
			}
		}
	}
	stop, release := watcher.WatchTransportStop(sessionID)
	if stop == nil {
		return abort, func() {
			if stopOnRequestEnd != nil {
				stopOnRequestEnd()
			}
			release()
		}
	}

	quit := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case <-stop:
			closer()
		case <-quit:
		}
	}()
	return abort, func() {
		if stopOnRequestEnd != nil {
			stopOnRequestEnd()
		}
		close(quit)
		<-finished
		release()
	}
}

func (h *StreamHandler) handleTransportStartFailure(ctx context.Context, session *playback.Session, file *models.MediaFile, err error) {
	if ctx == nil || session == nil || err == nil {
		return
	}
	// Client cancellation is not a transport failure: the viewer navigated off
	// (or hls.js gave up) while the upstream fetch was in flight. The request
	// context propagates into the virtual provider resolve / relay, so the
	// upstream work is already canceled. Check this before preflight so a
	// canceled request is never rewritten into a missing-file abort, and
	// downgrade to debug; a timeout or provider error stays at WARN.
	if isClientCancellation(ctx, err) {
		slog.DebugContext(ctx, "stream transport canceled by client",
			"component", "api",
			"session", session.ID,
			"file_id", session.MediaFileID,
			"reason", "client_canceled",
			"playback_session_id", session.ID,
		)
		return
	}
	// A request that already ended cannot stat the file; keep the transport's
	// own error for the log instead of the preflight's context error.
	if preflightErr := preflightPlaybackFile(ctx, file, h.MissingMarker, h.EventsHub); preflightErr != nil && ctx.Err() == nil {
		err = preflightErr
	}
	if isPlaybackFileMissing(err) || errors.Is(err, os.ErrNotExist) {
		h.abortPlaybackSession(ctx, session)
		return
	}
	slog.WarnContext(ctx, "stream transport startup failed", "component", "api",
		"session", session.ID,
		"file_id", session.MediaFileID,
		"pinned_candidate_id", virtualCandidateID(file),
		"error", err,
		"playback_session_id", session.ID,
	)
}

// markVirtualCandidateFailed stamps the catalog row for a virtual candidate
// as known-bad after a transport produced no bytes, so the auto-pick skips it
// on the next play while the dropdown still shows it (clickable) for a manual
// retry. Best-effort: a persistence failure must not turn a 502 into a 500.
func (h *StreamHandler) markVirtualCandidateFailed(ctx context.Context, file *models.MediaFile, candidateID string) {
	if h == nil || file == nil || candidateID == "" || candidateID != virtualResultCandidateID(file.FilePath) {
		return
	}
	if h.VirtualCandidateFailMarker == nil {
		return
	}
	markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := h.VirtualCandidateFailMarker(markCtx, file.ID, file.FilePath, file.FailedAt); err != nil {
		slog.WarnContext(ctx, "mark virtual candidate failed", "component", "api", "file_id", file.ID, "candidate", candidateID, "error", err)
	}
}

// clearVirtualCandidateRecovered clears a virtual candidate's known-bad stamp
// after the candidate actually delivered media bytes to a client. This is the
// only evidence that forgives a transport failure: a resolved URL (liveness
// check) is not, because resolution never opens the media, and written
// response headers alone are not either (the relay forwards header-only 204,
// 304, 416, and zero-length 200 responses). The clear is fenced on the
// DELIVERED candidate identity (the file path the transport served, which the
// session retains even after the catalog row rotates) and the failure state
// observed when the transport started, so a late delivery of candidate A never
// clears a rotation to B or a newer failure on A.
// Best-effort: a persistence failure must not fail a delivering stream.
func (h *StreamHandler) clearVirtualCandidateRecovered(ctx context.Context, file *models.MediaFile, deliveredFilePath string, observedFailedAt *time.Time) {
	if h == nil || file == nil || strings.TrimSpace(deliveredFilePath) == "" {
		return
	}
	if h.VirtualCandidateRecoveredMarker == nil {
		return
	}
	clearCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := h.VirtualCandidateRecoveredMarker(clearCtx, file.ID, deliveredFilePath, observedFailedAt); err != nil {
		slog.WarnContext(ctx, "clear virtual candidate recovered", "component", "api", "file_id", file.ID, "delivered", deliveredFilePath, "error", err)
	}
}

// virtualCandidateDeliveryEvidence reports whether a direct-play transfer
// actually delivered media: a 200/206 status AND positive body bytes.
// Header-only responses (204/304/416/zero-length 200) are explicitly forwarded
// by the relay and are not evidence the media endpoint works.
func virtualCandidateDeliveryEvidence(statusCode int, bytesWritten int64) bool {
	if statusCode != http.StatusOK && statusCode != http.StatusPartialContent {
		return false
	}
	return bytesWritten > 0
}

// shouldRetryVirtualSubtitleWithFreshSource reports whether a virtual subtitle
// extraction failed with an upstream 5xx before the response committed. That is
// the one failure a fresh provider URL can recover: the extraction command and
// the relay are fine, but the stored provider URL or its candidate answered
// 5xx. A committed response (notably a PGS .sup 200) must never be retried,
// because the client already owns those bytes. A map failure stays a rotation
// first even when the same attempt also 5xxed, so the existing re-probe path
// keeps that case.
func shouldRetryVirtualSubtitleWithFreshSource(virtual bool, status int, err error) bool {
	return virtual && status == 0 &&
		playback.IsSubtitleUpstreamError(err) &&
		!playback.IsSubtitleStreamMapError(err)
}

// retryVirtualSubtitleWithFreshSource performs the one bounded recovery after
// a virtual subtitle extraction fails on an upstream 5xx before writing bytes.
// It resolves the source again with forceRefresh and a fresh relay
// registration, releases the failed registration, replaces InputPath, and
// retries the extraction once. CacheIdentity is left untouched: it is the
// credential-free file/session/track key, so the retry lands under the same
// entry and only that identity's backoff is bypassed for this call.
//
// retried reports whether the extraction actually ran again. On a resolve
// failure the original release is returned unchanged, retried is false, and
// err describes the resolve failure; the caller keeps its original extraction
// error as the response cause.
func (h *StreamHandler) retryVirtualSubtitleWithFreshSource(
	ctx context.Context,
	file *models.MediaFile,
	session *playback.Session,
	opts *playback.StreamExtractOpts,
	releaseInput func(),
	retry func(playback.StreamExtractOpts) error,
) (release func(), retried bool, err error) {
	if h == nil || file == nil || session == nil || opts == nil || retry == nil {
		return releaseInput, false, errors.New("fresh virtual subtitle retry requires a file, session, options, and retry")
	}
	resolved, cleanup, resolveErr := h.resolveVirtualInputURI(
		withVirtualRelayFreshRegistration(ctx), file, session.UserID, session.ProfileID, true)
	if resolveErr != nil {
		return releaseInput, false, resolveErr
	}
	// The failed registration must not stay live while the retry opens the
	// provider again.
	if releaseInput != nil {
		releaseInput()
	}
	opts.InputPath = resolved.URL
	retryOpts := *opts
	retryOpts.FreshSourceRetry = true
	slog.InfoContext(ctx, "retrying virtual subtitle extraction with a fresh source registration", "component", "api",
		"file_id", file.ID,
		"track", opts.TrackIndex)
	return cleanup, true, retry(retryOpts)
}

// retryVirtualSubtitleAfterFailure applies the one bounded fresh-source retry
// only when the failed extraction is eligible. It returns the release for the
// input that remains current: the original release when the retry did not run
// or could not resolve, otherwise the fresh registration's release. The
// original extraction error is preserved when the retry never ran.
func (h *StreamHandler) retryVirtualSubtitleAfterFailure(
	ctx context.Context,
	file *models.MediaFile,
	session *playback.Session,
	opts *playback.StreamExtractOpts,
	releaseInput func(),
	virtual bool,
	status int,
	extractErr error,
	retry func(playback.StreamExtractOpts) error,
) (func(), error) {
	if ctx.Err() != nil || !shouldRetryVirtualSubtitleWithFreshSource(virtual, status, extractErr) {
		return releaseInput, extractErr
	}
	freshRelease, retried, freshErr := h.retryVirtualSubtitleWithFreshSource(
		ctx, file, session, opts, releaseInput, retry,
	)
	if retried {
		return freshRelease, freshErr
	}
	if freshErr != nil {
		track := 0
		if opts != nil {
			track = opts.TrackIndex
		}
		// Keep the original extraction failure as the response cause; the
		// resolve failure only explains why the retry never ran.
		slog.WarnContext(ctx, "virtual subtitle fresh source resolution failed", "component", "api",
			"file_id", file.ID,
			"track", track,
			"error", logredact.SanitizeURLError(freshErr))
	}
	return releaseInput, extractErr
}

// streamEmbeddedSubtitle runs a dedicated ffmpeg for a single embedded
// track, optionally windowed by explicit client parameters, and pipes its
// stdout directly to w. Because this ffmpeg is independent of the video
// pipeline, it works the same for direct play, remux, and transcode.
func (h *StreamHandler) streamEmbeddedSubtitle(w http.ResponseWriter, r *http.Request, file *models.MediaFile, embeddedIndex int, session *playback.Session, driftSuspected bool, requestedFormat ...string) {
	track := file.SubtitleTracks[embeddedIndex]
	outFormat := "vtt"
	switch {
	case playback.IsASS(track.Codec):
		outFormat = subtitleFormatASS
	case playback.IsPGS(track.Codec):
		outFormat = subtitleFormatSUP
	}

	// A subtitle URL describes the complete track unless the caller supplies
	// an explicit window. Native players fetch once and must retain cues beyond
	// ten minutes and before a resumed playback position. WebVTT and ASS honor
	// explicit ?position/?duration (ASS windows only when a position is given,
	// preserving the whole script for native consumers); PGS window consumers
	// opt in with ?windowed=1.
	allowWindow, seek, duration := subtitleExtractWindow(r, outFormat)
	windowRequested := subtitleWindowRequested(r)
	slog.InfoContext(r.Context(), "subtitle stream requested", "component", "api",
		"file_id", file.ID,
		"embedded_index", embeddedIndex,
		"track_language", track.Language,
		"track_codec", track.Codec,
		"track_probed_index", track.Index,
		"seek_seconds", seek,
		"duration_seconds", duration,
		"virtual_drift", driftSuspected,
	)

	opts := playback.StreamExtractOpts{
		InputPath:       file.FilePath,
		TrackIndex:      embeddedIndex,
		SourceCodec:     track.Codec,
		SeekSeconds:     seek,
		DurationSeconds: duration,
		AllowWindow:     allowWindow,
		WindowRequested: windowRequested,
		Embedded:        playback.EmbeddedSubtitleRefFor(file, embeddedIndex),
		FFmpegPath:      h.ffmpegPath(),
		SourceSizeBytes: file.FileSize,
		// Size, runtime and bitrate are what price a bitmap page's discarded
		// container prefix before it is read (see subtitle_page_cost.go). They
		// come from the catalog row, so a relay page can be refused without
		// opening the provider at all.
		SourceDurationSeconds: float64(file.Duration),
		SourceBitrateKbps:     file.Bitrate,
	}
	// The page wait ledger is per track so a re-anchoring retry cannot restart
	// it, but it belongs to the playback session that opened it: a new session
	// must pay its own measured wait instead of inheriting a spent one.
	if session != nil {
		opts.PageWaitScope = session.ID
	}
	// Virtual sources are provider-neutral URIs, not FFmpeg inputs. Resolve
	// through the relay so ffmpeg reads the real stream. Subtitle extraction
	// spawns its own ffmpeg independent of the video pipeline, so it must
	// resolve separately even though the transcode transport already did.
	releaseInput := func() {}
	virtualResolved := false
	if isVirtualPlaybackFile(file) && hasVirtualMediaResolver(h) && h.RemoteStreamRelay != nil && session != nil {
		resolved, cleanup, resolveErr := h.resolveVirtualInputURI(r.Context(), file, session.UserID, session.ProfileID, false)
		if resolveErr != nil {
			// The pinned source could not be resolved for this extract. Nothing
			// about it is a verdict on the file — the provider or the relay may
			// answer on the next attempt — so the refusal carries the same
			// Retry-After the remux start failure does. Without it a client
			// retries a 502 on its own few-second cadence.
			w.Header().Set("Retry-After", strconv.Itoa(virtualServeResolveRetryAfterSeconds))
			writeError(w, http.StatusBadGateway, subtitleSourceUnavailableErrorCode,
				"Failed to resolve the virtual source for subtitle extraction; the video source is unchanged.")
			return
		}
		opts.InputPath = resolved.URL
		releaseInput = cleanup
		virtualResolved = true
	}
	// Read the release through the variable: the bounded fresh-source retry
	// below replaces it with the retry registration's release.
	defer func() { releaseInput() }()
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
	}

	// Only complete successful extracts enter the cache; explicit windows
	// remain streamed. Keep failures distinguishable from a clean subtitle EOF.
	response := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
	virtualActive := virtualResolved && session != nil && session.VirtualSourceURI != ""

	// Virtual relay inputs never enter the payload cache under their rotating
	// URL; key on the pinned source + effective ordinal instead. The identity
	// is computed before the drift probe so a committed artifact can
	// short-circuit it, and recomputed after a remap to keep the warm and
	// serve keys aligned. The handler owns the detached warm
	// (warmVirtualSubtitleAfterWindowMiss), because this request's relay
	// registration is released when the request ends; the cache's own detached
	// warm is therefore disabled for virtual inputs.
	if virtualActive {
		opts.CacheIdentity = virtualSubtitleCacheIdentity(file, session.VirtualSourceURI, opts.TrackIndex)
		opts.DisableBackgroundWarm = true
	}

	// A param-less request against a virtual relay source can be either the
	// web player's whole-track upgrade probe or a native client's one-shot
	// whole-track fetch; the two are indistinguishable by shape. Only the
	// probe is a detached read: it targets a track that is already committed
	// or whose full-track fill is warming (that fill is what it upgrades), so
	// a shed round costs it nothing and its probe cadence re-issues it. A cold
	// param-less fetch — the native case, with no windowing contract and no
	// probe fallback — is a client-facing read and draws from the request-path
	// budget, where a saturated detached pool cannot shed it. An explicit
	// position/duration/windowed request keeps the request-path budget too.
	// This is captured before applyImplicitVirtualWindow synthesizes a
	// server-side window for the same param-less request.
	if virtualActive && !windowRequested && seek == 0 && duration == 0 && !allowWindow {
		opts.DetachedRelayDemux = h.SubtitleCache.HasCommittedEntry(
			opts.InputPath, opts.CacheIdentity, opts.TrackIndex, opts.SourceCodec, opts.TargetFormat,
		) || h.SubtitleCache.HasWarmingEntry(
			opts.InputPath, opts.CacheIdentity, opts.TrackIndex, opts.SourceCodec, opts.TargetFormat,
		)
	}

	// Row-vs-evidence drift (flagged in handleSubtitle) means the catalog row
	// no longer describes the release this session planned against. Probe the
	// live relay input once before any spawn or header commit and re-map the
	// plan ordinal onto a same-class live track when the pinned release
	// rotated. Mandatory for PGS, whose .sup response commits 200 before
	// ffmpeg spawns and therefore can never be retrofitted after a failed map.
	// A committed full-track text artifact pins the plan-time release, and a
	// cached-text input remaps to its sole stream, so the probe is unnecessary
	// then — skip its multi-second tax and serve the warm.
	// inheritedLayout carries the ordinal the serve established for the exact
	// identity it will hand the detached warm. It is only an ordinal hint: a
	// serve that deliberately skipped its drift probe on plan-time evidence
	// never probed this release's bytes, so it has no verdict to lend. The warm
	// probes its own relay registration regardless (see
	// resolveWarmExtractLayout); the hint merely lets that probe start from the
	// ordinal the serve settled on.
	var inheritedLayout *virtualSubtitleLayoutSeed
	if virtualActive && driftSuspected {
		if artifact, ok := h.resolveCommittedTextSubtitleEntry(&opts); ok {
			// A committed full-track text artifact pins the plan-time release and
			// a windowed extract reads it instead of the container, so the live
			// probe is unnecessary. Pin the exact artifact: the serve uses that
			// path (or reports ErrCommittedTextArtifactGone) rather than
			// re-resolving, so a generation-bucket rollover or eviction cannot
			// turn the validated artifact into an unvalidated source extract.
			opts.PinnedTextArtifact = &artifact
		} else if verdict, ok := h.reuseVirtualSubtitleLayout(&opts, time.Now()); ok {
			// A successful probe for this identity ran seconds ago. The pinned
			// provider result cannot rotate without changing the identity, so
			// reuse the mapping it established instead of paying the ffprobe
			// tax again on every window. Re-key the cache onto the effective
			// ordinal exactly as the probe path does, so a warm or serve lands
			// under the mapped identity.
			planOrdinal := opts.TrackIndex
			opts.TrackIndex = verdict.trackIndex
			opts.SourceCodec = verdict.sourceCodec
			opts.CacheIdentity = virtualSubtitleCacheIdentity(file, session.VirtualSourceURI, opts.TrackIndex)
			inheritedLayout = &virtualSubtitleLayoutSeed{
				cacheIdentity: opts.CacheIdentity,
				trackIndex:    opts.TrackIndex,
				sourceCodec:   opts.SourceCodec,
				verifiedAt:    verdict.verifiedAt,
			}
			slog.DebugContext(r.Context(), "virtual subtitle layout probe reused", "component", "api",
				"file_id", file.ID,
				"plan_ordinal", planOrdinal,
				"track", opts.TrackIndex)
		} else if playback.IsPGS(opts.SourceCodec) && h.SubtitleCache.HasCommittedEntry(
			opts.InputPath, opts.CacheIdentity, opts.TrackIndex, opts.SourceCodec, opts.TargetFormat,
		) {
			// A committed .sup artifact pins the release the same way a
			// committed text artifact does: it was extracted from this exact
			// identity (source candidate, ordinal, inventory token, generation
			// bucket). Probing the live layout first cannot improve bytes the
			// server already holds, and on a slow provider it can fail closed
			// with a retryable 503 even though the seek's re-request could be
			// answered from disk in milliseconds. A seek must not invalidate a
			// completed artifact.
			slog.DebugContext(r.Context(), "virtual subtitle layout probe skipped: committed bitmap artifact", "component", "api",
				"file_id", file.ID,
				"track", opts.TrackIndex)
		} else {
			// The key is captured before the probe: a drift remap changes
			// opts.TrackIndex, but later requests still arrive with the plan
			// ordinal.
			planLayoutKey := virtualSubtitleLayoutKey(opts.CacheIdentity, opts.TrackIndex)
			proceed, probeErr := h.verifyVirtualSubtitleLayout(r.Context(), file, track, file.SubtitleTracks, &opts)
			if !proceed {
				writeSubtitleSourceChanged(w)
				return
			}
			if probeErr != nil {
				// The live layout could not be established (probe error, relay
				// timeout, transient upstream failure). Serving the plan ordinal
				// unverified can silently emit the wrong release's track: the
				// URL names a plan-time ordinal whose validity depended on the
				// release the probe was supposed to confirm. PGS already failed
				// closed here because its .sup commits 200 before ffmpeg spawns.
				// Text/ASS now does the same rather than trusting a post-spawn map
				// error that a same-ordinal different-language rotation would
				// never raise. A retryable error lets the client fall back to
				// subtitles-off, which the server already treats as legitimate.
				//
				// Start the detached fill before answering: the retry's only
				// path to success is a committed artifact (it skips the probe),
				// and leaving the client to repeat the probe against the same
				// slow provider produced the measured run of 503s.
				if r.Method != http.MethodHead && !isClientCancellation(r.Context(), probeErr) {
					h.warmVirtualSubtitleAfterProbeFailure(file, track, session, opts, nil, virtualActive, middleware.GetReqID(r.Context()))
				}
				writeSubtitleProbeFailure(w, r.Context(), probeErr)
				return
			}
			h.rememberVirtualSubtitleLayout(planLayoutKey, opts, time.Now())
			// The probe may have remapped the ordinal; the cache identity must
			// track the effective map so a remapped extraction lands under its
			// own key.
			opts.CacheIdentity = virtualSubtitleCacheIdentity(file, session.VirtualSourceURI, opts.TrackIndex)
			inheritedLayout = &virtualSubtitleLayoutSeed{
				cacheIdentity: opts.CacheIdentity,
				trackIndex:    opts.TrackIndex,
				sourceCodec:   opts.SourceCodec,
				verifiedAt:    time.Now(),
			}
		}
	}

	// A detached warm probes its own relay registration. When that probe
	// completed and found no carryable track for this plan ordinal, it recorded
	// the verdict; without this answer every later page is refused again, starts
	// the same warm, and abandons the same way — a loop with no failure and no
	// progress. A committed artifact still pins the release it was extracted
	// from, so it is served rather than reported as rotated.
	//
	// The answer is retryable, not the 409 the serve's own probe produces. A 409
	// `subtitle_source_changed` tells the client its plan is stale and must be
	// rebuilt; that verdict belongs to the request that probed this release while
	// serving it. A background fill's opinion is ours, not the client's — and
	// production showed the cost of confusing them: one successful page at 2160,
	// then seven 409s, subtitles off, and no way back, because the replan kept
	// re-deriving the same plan. A retry re-reads the cached window or the
	// committed artifact, and the miss expires (virtualSubtitleLayoutMissTTL), so
	// the next serve probes the release again.
	if virtualActive &&
		h.virtualSubtitleLayoutUnmatchable(&opts, time.Now()) &&
		!h.SubtitleCache.HasCommittedEntry(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, opts.SourceCodec, opts.TargetFormat) {
		slog.WarnContext(r.Context(), "virtual subtitle page answered retryably from the warm's layout verdict",
			"component", "api",
			"file_id", file.ID,
			"track", opts.TrackIndex,
			"codec", opts.SourceCodec,
			"language", track.Language)
		writeSubtitleSourceUnavailableRetry(w, subtitleSourceRetryAfterSeconds*time.Second)
		return
	}

	// A HEAD probe (.sup is the only embedded format that does not early-return)
	// exists to read representation headers, not to extract: keep its previous
	// whole-track/committed behavior and never synthesize a window or warm from
	// it.
	if r.Method != http.MethodHead {
		h.applyImplicitVirtualWindow(&opts, file, session, virtualActive)
		// An explicit bitmap window is a page. Snap it onto the same page grid
		// an implicit window and the client's request path use, so the page this
		// request produces answers every later request inside it instead of
		// being a playhead-anchored slice nothing computes a key for.
		if snapped, pageStart, pageEnd := snapBitmapWindowToPage(&opts); snapped {
			slog.InfoContext(r.Context(), "bitmap subtitle window snapped to its page", "component", "api",
				"file_id", file.ID,
				"track", opts.TrackIndex,
				"requested_seek_seconds", seek,
				"requested_duration_seconds", duration,
				"page_start_seconds", pageStart,
				"page_end_seconds", pageEnd)
		}
	}
	// Advertise the bounded range before any header commits. A whole-track
	// serve (committed artifact, small/local source, or a codec that cannot be
	// sliced) omits the header, so a client that asked for the complete track
	// can tell it received only a window.
	playback.SetSubtitleCoverageHeader(w.Header(), opts)

	_, extractErr := h.SubtitleCache.ServeExtractWithResult(response, r, opts, playback.StreamExtractSubtitle)
	if errors.Is(extractErr, playback.ErrCommittedTextArtifactGone) {
		// The artifact that allowed the drift probe to be skipped was evicted
		// before the serve read it. No response has been written, so drop the
		// pin, validate the live layout, and retry rather than stream an
		// unvalidated source extract.
		opts.PinnedTextArtifact = nil
		proceed, probeErr := h.verifyVirtualSubtitleLayout(r.Context(), file, track, file.SubtitleTracks, &opts)
		if !proceed {
			writeSubtitleSourceChanged(w)
			return
		}
		if probeErr != nil {
			// The committed artifact is gone and the live layout cannot be
			// established: fail closed rather than stream an unverified
			// extraction. This is the same retryable response PGS uses.
			writeSubtitleProbeFailure(w, r.Context(), probeErr)
			return
		}
		opts.CacheIdentity = virtualSubtitleCacheIdentity(file, session.VirtualSourceURI, opts.TrackIndex)
		inheritedLayout = &virtualSubtitleLayoutSeed{
			cacheIdentity: opts.CacheIdentity,
			trackIndex:    opts.TrackIndex,
			sourceCodec:   opts.SourceCodec,
			verifiedAt:    time.Now(),
		}
		// The pinned artifact is gone, so the committed-entry check that
		// suppressed the implicit window on the first attempt no longer holds.
		// Re-evaluate against the now-cold source before retrying.
		h.applyImplicitVirtualWindow(&opts, file, session, virtualActive)
		playback.SetSubtitleCoverageHeader(w.Header(), opts)
		_, extractErr = h.SubtitleCache.ServeExtractWithResult(response, r, opts, playback.StreamExtractSubtitle)
	}
	retryWithFreshSource := func(retryOpts playback.StreamExtractOpts) error {
		_, retryServeErr := h.SubtitleCache.ServeExtractWithResult(response, r, retryOpts, playback.StreamExtractSubtitle)
		return retryServeErr
	}
	if extractErr != nil {
		releaseInput, extractErr = h.retryVirtualSubtitleAfterFailure(
			r.Context(), file, session, &opts, releaseInput,
			virtualResolved && session != nil, response.Status(), extractErr,
			retryWithFreshSource,
		)
	}
	if extractErr != nil {
		var pageUnavailable *playback.SubtitleExtractionUnavailableError
		if errors.As(extractErr, &pageUnavailable) {
			// The page's track artifact could not reach it inside its recovery
			// budget (or stopped advancing toward it). Repeating the same page
			// pays the same container prefix again, so this is the one answer the
			// client must stop on: no Retry-After, no further recovery warm.
			if response.Status() != 0 {
				panic(http.ErrAbortHandler)
			}
			playback.SetSubtitleCoverageRange(w.Header(), 0, pageUnavailable.CoveredUntil)
			writeError(w, http.StatusServiceUnavailable, playback.SubtitlePageUnavailableReason,
				"This subtitle page could not be prepared in time. Choose another subtitle track.")
			return
		}
		var extractionRetry *playback.SubtitleExtractionRetryError
		if errors.As(extractErr, &extractionRetry) {
			// The per-identity backoff after an upstream failure is still
			// active: no provider read or ffmpeg spawn happened. This is an
			// expected retryable answer, not an extraction failure worth a
			// warning on every client retry. Answer with the remaining delay
			// so the client waits instead of retrying in its default
			// few-second cadence.
			if extractionRetry.Reason == playback.SubtitlePageTooCostlyReason && extractionRetry.PageRecoveryDue {
				// The page could not be cut from the source at an affordable
				// read cost. The track's progressive artifact is what serves it,
				// and that fill must outlive this request's relay registration,
				// so the handler starts it here (the cache's own detached warm is
				// disabled for virtual inputs). It is single-flighted: a burst of
				// pages starts one fill.
				h.warmVirtualSubtitleForWaitingPage(file, track, session, opts, inheritedLayout, virtualActive, middleware.GetReqID(r.Context()))
			}
			if extractionRetry.WindowBudgetExceeded {
				// The window read its source for its whole budget instead of
				// failing, so the source is reachable and only this page is
				// too costly for one request. Start the detached recovery warm
				// here: it extracts the breached window plus one client cell,
				// so the retry this 503 asks for is covered rather than
				// repeating the read that just timed out.
				h.warmVirtualSubtitleAfterBudgetBreach(file, track, session, opts, inheritedLayout, virtualActive, middleware.GetReqID(r.Context()))
			}
			if response.Status() != 0 {
				// The slice spilled past its hold and part of a 200 is on the
				// wire. Nothing can retract it, so the response is aborted
				// rather than completed as a window that looks whole. The
				// recovery warm above still runs, so the client's retry reads
				// the covering slice instead of repeating the read.
				panic(http.ErrAbortHandler)
			}
			writeSubtitleRetry(w, extractionRetry)
			return
		}
		if errors.Is(extractErr, playback.ErrSubtitleReadsBusy) {
			// The node admits no more whole-track reads. Answer before any byte
			// with the standard busy status and Retry-After, like the download
			// asset path, instead of blaming the extraction.
			clearSubtitleCoverageHeaders(w.Header())
			if response.Status() == 0 {
				writeSubtitleReadsBusy(w)
			} else {
				panic(http.ErrAbortHandler)
			}
			return
		}
		playback.LogSubtitleStreamError(r.Context(), extractErr, file.ID, embeddedIndex)
		if r.Context().Err() != nil {
			clearSubtitleCoverageHeaders(w.Header())
			return
		}
		// A successful HTTP EOF would make clients accept the partial track.
		if response.Status() != 0 {
			panic(http.ErrAbortHandler)
		}
		if !virtualActive || !playback.IsSubtitleStreamMapError(extractErr) {
			writeSubtitleExtractFailure(w, extractErr, opts.InputPath)
			return
		}
		// Post-spawn safety net: the plan ordinal named a subtitle stream the
		// live input does not have, meaning the pinned release rotated between
		// the last probe and this spawn (or no pre-spawn probe ran because the
		// row still matched the plan evidence). Re-probe the already-registered
		// relay URL once — no second resolve — and re-map; a source that still
		// cannot satisfy the requested representation gets a clean retryable 4xx
		// instead of a 500.
		proceed, probeErr := h.verifyVirtualSubtitleLayout(r.Context(), file, track, file.SubtitleTracks, &opts)
		if !proceed {
			writeSubtitleSourceChanged(w)
			return
		}
		if probeErr != nil {
			// A map error already proved the plan ordinal names no live stream;
			// if the re-probe then cannot establish the layout either, there is
			// no verified ordinal to retry with. Fail closed rather than re-spawn
			// the same unverified extraction, and start the detached fill so the
			// next retry can read a committed artifact instead of re-probing.
			if r.Method != http.MethodHead && !isClientCancellation(r.Context(), probeErr) {
				h.warmVirtualSubtitleAfterProbeFailure(file, track, session, opts, inheritedLayout, virtualActive, middleware.GetReqID(r.Context()))
			}
			writeSubtitleProbeFailure(w, r.Context(), probeErr)
			return
		}
		// The retry may have remapped to a different live ordinal; the cache
		// identity must track the effective map so a remapped extraction lands
		// under its own key.
		if virtualActive {
			opts.CacheIdentity = virtualSubtitleCacheIdentity(file, session.VirtualSourceURI, opts.TrackIndex)
			inheritedLayout = &virtualSubtitleLayoutSeed{
				cacheIdentity: opts.CacheIdentity,
				trackIndex:    opts.TrackIndex,
				sourceCodec:   opts.SourceCodec,
				verifiedAt:    time.Now(),
			}
		}
		if retryErr := h.SubtitleCache.ServeExtract(response, r, opts, playback.StreamExtractSubtitle); retryErr != nil {
			var retryBackoff *playback.SubtitleExtractionRetryError
			if errors.As(retryErr, &retryBackoff) {
				if response.Status() != 0 {
					panic(http.ErrAbortHandler)
				}
				writeSubtitleRetry(w, retryBackoff)
				return
			}
			playback.LogSubtitleStreamError(r.Context(), retryErr, file.ID, embeddedIndex)
			if r.Context().Err() != nil {
				clearSubtitleCoverageHeaders(w.Header())
				return
			}
			if response.Status() != 0 {
				panic(http.ErrAbortHandler)
			}
			if playback.IsSubtitleStreamMapError(retryErr) {
				writeSubtitleSourceChanged(w)
				return
			}
			writeSubtitleExtractFailure(w, retryErr, opts.InputPath)
			return
		}
	}

	// The windowed request just read the remote source and, by design,
	// committed nothing. Populate the full-track cache so the next window hits
	// the small artifact instead of re-demuxing the source.
	h.warmVirtualSubtitleAfterWindowMiss(file, track, session, opts, inheritedLayout, virtualActive, middleware.GetReqID(r.Context()))
}

// virtualSubtitleCacheIdentity keys the subtitle cache for a virtual row being
// served. The row's probed subtitle layout is part of the identity: a re-probe
// that established a different inventory names different streams under the same
// ordinals, so artifacts committed against the previous layout must not be
// served for the new one. An unchanged layout keeps the same key, so ordinary
// re-probing does not throw away warm artifacts.
func virtualSubtitleCacheIdentity(file *models.MediaFile, virtualSourceURI string, trackIndex int) string {
	if file == nil {
		return ""
	}
	return playback.VirtualSubtitleCacheIdentity(
		file.ID, virtualSourceURI, trackIndex, playback.SubtitleInventoryToken(file.SubtitleTracks),
	)
}

// resolveCommittedTextSubtitleEntry resolves the committed full-track text
// artifact that a windowed extract for this identity would read, returning a
// binding token. Callers that skip track-identity validation because the
// artifact pins the plan-time release must pin this token on the options: the
// serve then reads that exact path (or reports
// ErrCommittedTextArtifactGone) rather than re-resolving, so a generation-bucket
// rollover or eviction between the identity check and the read cannot turn the
// validated artifact into an unvalidated source extract. Bitmap (PGS) codecs, an
// unkeyable source, and a nil cache read as false.
func (h *StreamHandler) resolveCommittedTextSubtitleEntry(opts *playback.StreamExtractOpts) (playback.CommittedTextArtifact, bool) {
	if h == nil || h.SubtitleCache == nil || opts == nil {
		return playback.CommittedTextArtifact{}, false
	}
	return h.SubtitleCache.ResolveCommittedTextEntry(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, opts.SourceCodec, opts.TargetFormat)
}

// virtualSubtitleWarmResolveTimeout bounds the relay resolution a detached
// virtual subtitle warm performs. It is a child of the extraction context the
// cache hands the warm, so resolution can never outlive extraction; a resolver
// that never returns is canceled with the extraction instead of pinning the
// cache's warm slot and in-flight fill for the whole extraction budget. Tests
// override it to keep the release assertion fast.
var virtualSubtitleWarmResolveTimeout = 2 * time.Minute

// Seek bounds for the detached warm's one-second abort ticker. A text warm may
// run during playback, so resuming is no longer a cancel signal; a seek and a
// session end are. Backward movement is unambiguous, so a small tolerance
// keeps progress-report jitter and a two-second scrub-back from discarding a
// warm that is still useful. Forward movement is bounded by twice the elapsed
// wall clock plus slack, which covers 2x playback and sparse progress samples
// without letting a real seek hide.
const (
	virtualSubtitleWarmSeekBackSlack  = 10.0
	virtualSubtitleWarmSeekAheadSlack = 30.0
	virtualSubtitleWarmSeekRate       = 2.0
)

// virtualSubtitleWarmRekeyAttempts bounds how many times a detached warm
// re-keys itself when its own drift probe settles on an identity other than the
// one its fill was admitted under. A serve that skipped its drift probe (plan
// evidence, or a committed artifact) hands the warm a plan-ordinal identity,
// but the warm's probe is the authority on the bytes it reads: if that probe
// remaps onto a different live ordinal, committing under the plan key would
// publish that track under an identity no serve computes, while a concurrent
// page that probed the same release would use the post-remap key and demux a
// second time. The warm therefore discards the fill and re-runs under the
// probe's identity. A release that keeps moving under the probe gives up
// without committing; the next windowed miss retries.
const virtualSubtitleWarmRekeyAttempts = 3

// virtualSubtitleLayoutCacheTTL bounds how long a successful drift-probe
// verdict is reused for the same (CacheIdentity, plan ordinal). A windowed
// player asks for the same track once per window — 120 s with a 60 s prefetch
// — and a re-probe costs seconds before extraction starts. The TTL must
// comfortably cover several consecutive pages or every fourth request pays
// the probe again even though nothing about the pinned release changed; eight
// minutes covers a 120 s page cadence with room for retries and prefetch
// bursts while still re-probing a long playback periodically. A release
// rotation changes the pinned result and therefore the identity, so the TTL
// only hides residual same-candidate change, which stays bounded by the TTL.
const virtualSubtitleLayoutCacheTTL = 8 * time.Minute

// virtualSubtitleLayoutCacheMaxEntries caps the probe memo. Live entries are
// identities probed inside the TTL; a long-lived server would otherwise
// accumulate one per track ever played. Eviction only costs the next request a
// probe, so the cap is generous.
const virtualSubtitleLayoutCacheMaxEntries = 4096

// virtualSubtitleLayoutVerdict is the mapping a successful drift probe
// established for one (CacheIdentity, plan ordinal): the effective live
// ordinal and codec, which may be the plan values when the layouts matched.
type virtualSubtitleLayoutVerdict struct {
	trackIndex  int
	sourceCodec string
	verifiedAt  time.Time
}

// virtualSubtitleLayoutSeed carries the ordinal the serve request established
// into the detached warm it starts. It is an ordinal hint, never commit
// authority: the warm probes its own relay registration before extracting, and
// a serve that never probed (an evidence-bound ordinal, or a memo hit whose
// verdict belonged to an earlier serve) cannot lend a verdict for bytes this
// warm is about to read. The identity is derived from the pinned provider result
// and inventory token, so a rotated release changes it and the hint is dropped.
type virtualSubtitleLayoutSeed struct {
	cacheIdentity string
	trackIndex    int
	sourceCodec   string
	verifiedAt    time.Time
}

// virtualSubtitleLayoutKey is the memo key for a plan ordinal. The identity is
// derived from the pinned provider result (not the rotating relay URL), so
// adjacent windows on one release share it.
func virtualSubtitleLayoutKey(cacheIdentity string, trackIndex int) string {
	return cacheIdentity + "\x00" + strconv.Itoa(trackIndex)
}

// reuseVirtualSubtitleLayout returns the verified mapping for this request's
// plan ordinal when a successful probe for the same identity ran within
// virtualSubtitleLayoutCacheTTL. Failures are never recorded, so a probe error
// or a positive mismatch is re-evaluated on every request.
func (h *StreamHandler) reuseVirtualSubtitleLayout(opts *playback.StreamExtractOpts, now time.Time) (virtualSubtitleLayoutVerdict, bool) {
	if h == nil || opts == nil || opts.CacheIdentity == "" {
		return virtualSubtitleLayoutVerdict{}, false
	}
	key := virtualSubtitleLayoutKey(opts.CacheIdentity, opts.TrackIndex)
	h.virtualSubtitleLayoutMu.Lock()
	defer h.virtualSubtitleLayoutMu.Unlock()
	verdict, ok := h.virtualSubtitleLayouts[key]
	if !ok {
		return virtualSubtitleLayoutVerdict{}, false
	}
	if now.Sub(verdict.verifiedAt) > virtualSubtitleLayoutCacheTTL {
		delete(h.virtualSubtitleLayouts, key)
		return virtualSubtitleLayoutVerdict{}, false
	}
	playback.ObserveSubtitleLayoutProbeReused()
	return verdict, true
}

// rememberVirtualSubtitleLayout records the mapping a successful probe
// established for planKey. planKey is the pre-probe key: a remap changes
// opts.TrackIndex, but later requests still ask with the plan ordinal.
func (h *StreamHandler) rememberVirtualSubtitleLayout(planKey string, opts playback.StreamExtractOpts, now time.Time) {
	if h == nil || planKey == "" {
		return
	}
	h.virtualSubtitleLayoutMu.Lock()
	defer h.virtualSubtitleLayoutMu.Unlock()
	if h.virtualSubtitleLayouts == nil {
		h.virtualSubtitleLayouts = make(map[string]virtualSubtitleLayoutVerdict)
	}
	if len(h.virtualSubtitleLayouts) >= virtualSubtitleLayoutCacheMaxEntries {
		for key, verdict := range h.virtualSubtitleLayouts {
			if now.Sub(verdict.verifiedAt) > virtualSubtitleLayoutCacheTTL {
				delete(h.virtualSubtitleLayouts, key)
			}
		}
		if len(h.virtualSubtitleLayouts) >= virtualSubtitleLayoutCacheMaxEntries {
			// Still full of fresh entries: drop the memo rather than grow it
			// without bound. A dropped verdict only costs the next request a
			// probe.
			clear(h.virtualSubtitleLayouts)
		}
	}
	h.virtualSubtitleLayouts[planKey] = virtualSubtitleLayoutVerdict{
		trackIndex:  opts.TrackIndex,
		sourceCodec: opts.SourceCodec,
		verifiedAt:  now,
	}
	// A match recorded now is newer evidence than an earlier rotation verdict
	// for the same ordinal.
	delete(h.virtualSubtitleLayoutMisses, planKey)
}

// virtualSubtitleLayoutMissTTL bounds how long a positive "the live release has
// no track this ordinal can carry" verdict answers page requests. It is much
// shorter than the match memo: the verdict exists to end a loop that produces
// nothing, not to hide a release that might be re-listed under it. Every page a
// client asks for inside the window is answered with the same rotation verdict,
// so the client stops retrying and replans. A re-listed release changes the
// pinned result and therefore the identity, which is a different key.
const virtualSubtitleLayoutMissTTL = 60 * time.Second

// rememberVirtualSubtitleLayoutMiss records that a completed probe found no live
// track for this plan ordinal. Only a completed probe that positively found no
// match may call it: a probe error (timeout, provider failure, cancellation) is
// not evidence about the bytes and must leave the next request free to try.
func (h *StreamHandler) rememberVirtualSubtitleLayoutMiss(planKey string, now time.Time) {
	if h == nil || planKey == "" {
		return
	}
	h.virtualSubtitleLayoutMu.Lock()
	defer h.virtualSubtitleLayoutMu.Unlock()
	if h.virtualSubtitleLayoutMisses == nil {
		h.virtualSubtitleLayoutMisses = make(map[string]time.Time)
	}
	if len(h.virtualSubtitleLayoutMisses) >= virtualSubtitleLayoutCacheMaxEntries {
		for key, at := range h.virtualSubtitleLayoutMisses {
			if now.Sub(at) > virtualSubtitleLayoutMissTTL {
				delete(h.virtualSubtitleLayoutMisses, key)
			}
		}
		if len(h.virtualSubtitleLayoutMisses) >= virtualSubtitleLayoutCacheMaxEntries {
			clear(h.virtualSubtitleLayoutMisses)
		}
	}
	h.virtualSubtitleLayoutMisses[planKey] = now
	// A rotation verdict supersedes a mapping recorded against the same ordinal
	// before the release moved.
	delete(h.virtualSubtitleLayouts, planKey)
}

// virtualSubtitleLayoutUnmatchable reports whether a completed probe already
// found no live track for this plan ordinal inside the miss TTL. The serve path
// answers it like an immediate positive mismatch, without starting another fill
// that can only abandon the same way.
func (h *StreamHandler) virtualSubtitleLayoutUnmatchable(opts *playback.StreamExtractOpts, now time.Time) bool {
	if h == nil || opts == nil || opts.CacheIdentity == "" {
		return false
	}
	key := virtualSubtitleLayoutKey(opts.CacheIdentity, opts.TrackIndex)
	h.virtualSubtitleLayoutMu.Lock()
	defer h.virtualSubtitleLayoutMu.Unlock()
	at, ok := h.virtualSubtitleLayoutMisses[key]
	if !ok {
		return false
	}
	if now.Sub(at) > virtualSubtitleLayoutMissTTL {
		delete(h.virtualSubtitleLayoutMisses, key)
		return false
	}
	return true
}

// warmVirtualSubtitleAfterWindowMiss starts detached cache fill work for a
// windowed virtual subtitle request (text, ASS/SSA, or PGS) that could not be
// served from the cache. It is called after the window itself has streamed, so
// the warm cannot contend with the triggering request's own read.
//
// When the live playhead has moved into the file, the fill is a bounded window
// prefetch around that playhead (PrefetchWindowsInBackground): the client's next
// page or two are extracted and kept as last-good windows. A full-track warm
// always extracts from source zero and commits under the whole-track artifact
// key, so starting one here would publish a tail slice as the complete track —
// wrong offsets and every cue before the seek missing. Only a session still at
// the start keeps the full-track shape, where that artifact is what the opening
// playback actually reads.
//
// A bitmap (PGS) window is the exception: it is a page, and a page the source
// could not serve cheaply is what the track's progressive artifact exists to
// answer. That fill is the page path, not an opportunistic second read — it runs
// even while the viewer is playing, and it replaces the bounded window prefetch,
// which would re-read the source for pages the fill is publishing. A whole-track
// bitmap request keeps the paused-only shape; only a bounded bitmap page routes
// here (see bitmapPageWaitingForFill).
//
// The warm resolves and holds its own relay registration: the request's
// registration is released when the request ends, which is exactly why the
// cache's own detached warm is disabled for virtual inputs
// (opts.DisableBackgroundWarm). Resolution happens lazily inside the extract
// closure on a context derived from the extraction context, so the cache's warm
// budget (warmSem) and in-flight coalescing (beginFill) gate it before any
// remote work and a stuck resolver cannot pin either: concurrent window misses
// on the same serve identity resolve once and demux once, and the registration
// is released exactly once when the warm settles.
//
// Because that registration is independent of the serve request's, the warm
// validates it against track and the row's probed inventory before it extracts
// anything: a rotated release either remaps onto the same-class live track (and
// re-keys the identity to the effective ordinal) or abandons the warm. The
// serving request may pass the ordinal it established as a hint (see
// virtualSubtitleLayoutSeed), but the warm always probes its own registration;
// it never commits on a verdict it did not establish itself. When that probe
// settles on an identity other than the one the fill was admitted under, the
// warm discards the fill and re-runs under the probe's identity, so the artifact
// always commits under the same key a serve computes for the track it contains.

// virtualSubtitleWarmSession returns the freshest session copy the warm gate
// should read. The session copy on the serve request predates the latest
// progress sample, so prefer the live session when the handler has a manager.
// A nil manager keeps the copy's state for minimal handlers/tests. ok is false
// when the session is gone.
func (h *StreamHandler) virtualSubtitleWarmSession(session *playback.Session) (*playback.Session, bool) {
	if session == nil {
		return nil, false
	}
	if h.sessionMgr == nil {
		return session, true
	}
	current, err := h.sessionMgr.GetSession(session.ID)
	if err != nil || current == nil {
		return nil, false
	}
	return current, true
}

// virtualSubtitleWarmCancelReason reports why a detached warm must stop now,
// or "" to keep going. Because text warms may run while playing, a pause/resume
// is no longer a cancel condition: the warm stops when its session ends or the
// playhead jumps away from the position the warm was started at. A seek makes
// the window that triggered the warm stale; the next windowed miss starts a
// fresh warm. The forward bound allows twice-normal playback progress plus
// slack, so ordinary progress-report lag never kills a warm that is otherwise
// doing the work the cache needs.
func (h *StreamHandler) virtualSubtitleWarmCancelReason(session *playback.Session, anchorPosition float64, anchorAt, now time.Time) string {
	live, ok := h.virtualSubtitleWarmSession(session)
	if !ok || live == nil {
		return "session ended"
	}
	if live.Position < anchorPosition-virtualSubtitleWarmSeekBackSlack {
		return "seek backward"
	}
	elapsed := now.Sub(anchorAt).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	ahead := anchorPosition + virtualSubtitleWarmSeekRate*elapsed + virtualSubtitleWarmSeekAheadSlack
	if live.Position > ahead {
		return "seek forward"
	}
	return ""
}

// resolveWarmExtractLayout validates the relay registration a detached
// subtitle warm resolved on its own and aligns the extraction with the live
// layout.
//
// The warm always probes its own registration: only that probe establishes what
// its own bytes contain. The triggering serve's verdict is at most an ordinal
// hint — it may start the probe from the ordinal the serve settled on when the
// hint still names a plan track with the requested attributes — but it never
// authorizes a commit. A seed that has aged past the TTL, that names a different
// identity, or that does not describe a usable plan track is dropped, and a
// probe failure fails closed.
func (h *StreamHandler) resolveWarmExtractLayout(warmCtx context.Context, file *models.MediaFile, session *playback.Session, track models.SubtitleTrack, planTracks []models.SubtitleTrack, inherited *virtualSubtitleLayoutSeed, extractOpts *playback.StreamExtractOpts) error {
	if inherited != nil &&
		inherited.cacheIdentity == extractOpts.CacheIdentity &&
		time.Since(inherited.verifiedAt) <= virtualSubtitleLayoutCacheTTL &&
		subtitleWarmHintMatchesPlanTrack(track, planTracks, inherited.trackIndex) {
		extractOpts.TrackIndex = inherited.trackIndex
		extractOpts.SourceCodec = inherited.sourceCodec
	}
	proceed, validateErr := h.verifyVirtualSubtitleLayout(warmCtx, file, track, planTracks, extractOpts)
	if !proceed {
		// The live layout no longer carries a same-class track for this
		// plan track. Nothing was committed (the fill is discarded on
		// this path), and this is a deliberate abandonment rather than a
		// source failure, so it must not arm the warm failure cooldown:
		// the next windowed miss re-evaluates the layout itself.
		//
		// The verdict is recorded, though. This warm probed the live release
		// and found nothing for the plan ordinal: leaving it unrecorded is a
		// loop, because the page path never probes (it trusts the row's
		// evidence) and every page it refuses starts the same warm, which
		// abandons the same way. The next page request is answered with the
		// rotation verdict so the client replans onto the rotated inventory.
		// Only this positive result is recorded; a probe error returns below
		// and records nothing.
		h.rememberVirtualSubtitleLayoutMiss(virtualSubtitleLayoutKey(extractOpts.CacheIdentity, extractOpts.TrackIndex), time.Now())
		slog.WarnContext(context.Background(), "virtual subtitle warm abandoned: live layout has no match for the planned track",
			"component", "api", "file_id", file.ID,
			"plan_codec", track.Codec, "plan_language", track.Language)
		return playback.ErrSubtitleWarmCanceled
	}
	if validateErr != nil {
		if warmCtx.Err() != nil {
			// The abort ticker canceled the probe (seek or session end):
			// same intentional abandonment as a canceled resolve.
			return playback.ErrSubtitleWarmCanceled
		}
		// The live layout could not be established. Fail closed and let
		// the cache handle it like any other warm failure, including its
		// per-identity cooldown, rather than extract an unverified
		// ordinal.
		return fmt.Errorf("validate virtual subtitle warm layout: %w", validateErr)
	}
	// The warm's own probe may remap the ordinal onto a different live
	// track. Keep the extraction aligned with what the probe established; the
	// caller compares this identity with the fill's admitted key and re-keys
	// the warm when they differ, so the artifact can never commit another
	// track's bytes under the plan-ordinal identity.
	extractOpts.CacheIdentity = virtualSubtitleCacheIdentity(file, session.VirtualSourceURI, extractOpts.TrackIndex)
	return nil
}

// subtitleWarmHintMatchesPlanTrack reports whether hintOrdinal names a plan
// track that still matches the requested track's extraction class, language,
// forced flag, and hearing-impaired flag. The warm's own probe remains the
// authority; this only keeps a stale remap from steering it onto a different
// track when the live layout has reverted to the plan inventory.
func subtitleWarmHintMatchesPlanTrack(requested models.SubtitleTrack, planTracks []models.SubtitleTrack, hintOrdinal int) bool {
	if hintOrdinal < 0 || hintOrdinal >= len(planTracks) {
		return false
	}
	candidate := planTracks[hintOrdinal]
	if playback.SubtitleExtractMuxer(requested.Codec, "") != playback.SubtitleExtractMuxer(candidate.Codec, "") {
		return false
	}
	if requested.Forced != candidate.Forced || requested.HearingImpaired != candidate.HearingImpaired {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(requested.Language), strings.TrimSpace(candidate.Language))
}

func (h *StreamHandler) warmVirtualSubtitleAfterWindowMiss(file *models.MediaFile, track models.SubtitleTrack, session *playback.Session, opts playback.StreamExtractOpts, inherited *virtualSubtitleLayoutSeed, virtualActive bool, requestID string) {
	// A bounded bitmap page is the case where the detached fill is the page
	// path: no page extract produces the artifact the next page needs, and the
	// client is already waiting on it. A whole-track bitmap request keeps the
	// paused-only admission below.
	h.warmVirtualSubtitle(file, track, session, opts, inherited, virtualActive, requestID, bitmapPageWaitingForFill(opts), false, false)
}

// warmVirtualSubtitleAfterBudgetBreach starts the detached recovery warm for a
// request-path text window that was answered retryably at its elapsed budget.
// The retry a 503 asks for usually re-anchors a few seconds forward, so the
// warm extracts the breached window plus one client grid cell and stores it as
// the last-good window. That range covers both an exact repeat and the shifted
// window the retry asks for; the ordinary mid-file prefetch derives its windows
// from the playhead grid and misses both, which is why a breach used to repeat
// at full price until the client gave up.
func (h *StreamHandler) warmVirtualSubtitleAfterBudgetBreach(file *models.MediaFile, track models.SubtitleTrack, session *playback.Session, opts playback.StreamExtractOpts, inherited *virtualSubtitleLayoutSeed, virtualActive bool, requestID string) {
	h.warmVirtualSubtitle(file, track, session, opts, inherited, virtualActive, requestID, false, false, true)
}

// warmVirtualSubtitleAfterProbeFailure starts the detached fill when the
// request's live-layout probe could not establish the source. The handler has
// already decided to answer retryably; without a fill, every retry repeats the
// same probe against the same slow provider and fails again (production showed
// 18 probe-timeout 503s before one request finally succeeded). The warm opens
// its own relay registration, probes with the warm budget, and commits the
// artifact under the serve identity, so the next retry is a cache hit instead
// of another probe. force lets it run for a whole-track request (no explicit
// window, which normally fills inline) and for a bitmap track while playback
// runs, because those are exactly the cases no inline fill produced.
func (h *StreamHandler) warmVirtualSubtitleAfterProbeFailure(file *models.MediaFile, track models.SubtitleTrack, session *playback.Session, opts playback.StreamExtractOpts, inherited *virtualSubtitleLayoutSeed, virtualActive bool, requestID string) {
	h.warmVirtualSubtitle(file, track, session, opts, inherited, virtualActive, requestID, bitmapPageWaitingForFill(opts), true, false)
}

// bitmapPageWaitingForFill reports whether this request was a bounded bitmap
// (PGS) page that read its source: the window the client plays next can only be
// produced from the track's progressive artifact, so the fill is what it is
// waiting on. A whole-track bitmap request is not a page and keeps the
// paused-only full-track warm.
func bitmapPageWaitingForFill(opts playback.StreamExtractOpts) bool {
	if !playback.IsPGS(opts.SourceCodec) || !opts.AllowWindow {
		return false
	}
	windowed, _, _, openEnded := opts.WindowCoverage()
	return windowed && !openEnded
}

// warmVirtualSubtitleForWaitingPage starts the track's detached fill because a
// bounded bitmap page is waiting for its progressive artifact. The page cannot
// be cut from the source at an affordable cost, so the fill is the page path,
// not an opportunistic extra read: it runs even while the viewer is playing and
// even when a bounded page prefetch would otherwise take precedence, because
// those pages are exactly what this fill publishes as it parses.
func (h *StreamHandler) warmVirtualSubtitleForWaitingPage(file *models.MediaFile, track models.SubtitleTrack, session *playback.Session, opts playback.StreamExtractOpts, inherited *virtualSubtitleLayoutSeed, virtualActive bool, requestID string) {
	h.warmVirtualSubtitle(file, track, session, opts, inherited, virtualActive, requestID, true, false, false)
}

func (h *StreamHandler) warmVirtualSubtitle(file *models.MediaFile, track models.SubtitleTrack, session *playback.Session, opts playback.StreamExtractOpts, inherited *virtualSubtitleLayoutSeed, virtualActive bool, requestID string, pageWaiting, force, breach bool) {
	if h == nil || h.SubtitleCache == nil || file == nil || session == nil || !virtualActive {
		return
	}
	// Snapshot the row's probed layout. The warm validates its own relay
	// registration against this inventory, and the inventory is also the term
	// SubtitleInventoryToken folds into the cache identity; reading it once
	// keeps the two in agreement even if the row is refreshed mid-warm.
	planTracks := file.SubtitleTracks
	// A full-track warm is a second read of the remote source. Text warms are
	// worth that read even while the viewer is playing: without one, every
	// 120 s window on a large virtual source re-demuxes the relay input and the
	// small artifact never appears, so rendering stops once the client's
	// lookahead runs out. The same continuation argument holds for a bitmap
	// (PGS) track, but only for the bounded page prefetch below: a full .sup is
	// tens of megabytes demuxed from the whole container, so the full-track
	// shape stays paused-only. The warm stays single-flighted through
	// WarmTrackInBackground / PrefetchWindowsInBackground (warmSem + beginFill),
	// so a burst of windows still demuxes once.
	live, liveOK := h.virtualSubtitleWarmSession(session)
	if !liveOK || live == nil {
		slog.DebugContext(context.Background(), "virtual subtitle warm skipped: session ended",
			"component", "api", "session", session.ID, "file_id", file.ID, "codec", opts.SourceCodec)
		return
	}
	if !live.IsPaused {
		slog.InfoContext(context.Background(), "virtual subtitle warm starting during playback",
			"component", "api", "session", session.ID, "file_id", file.ID,
			"codec", opts.SourceCodec, "track", opts.TrackIndex)
	}
	if opts.CacheIdentity == "" {
		return
	}
	// Every sidecar class gets this handler-owned warm. PGS matters most: its
	// whole-track .sup exceeds a client fetch deadline on a large virtual
	// source, so without a warm no committed artifact ever appears and every
	// repeat is a fresh full remote demux. The warm's mandatory drift probe and
	// progressive .sup handling are unchanged — only the populate step is new.
	// Full-track requests already fill the cache inline through ServeExtract's
	// tee; only explicit windows need a detached warm. force is the exception:
	// a failed live-layout probe produced no inline fill at all, so the retry
	// needs the artifact even for a param-less whole-track request.
	if !force && opts.SeekSeconds == 0 && opts.DurationSeconds == 0 {
		return
	}
	// A committed entry means this request served its window from the cached
	// artifact; nothing to warm. Format-aware, so a committed .sup suppresses
	// the warm exactly as a committed VTT/ASS does.
	if h.SubtitleCache.HasCommittedEntry(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, opts.SourceCodec, opts.TargetFormat) {
		slog.DebugContext(context.Background(), "virtual subtitle warm skipped: committed artifact present",
			"component", "api", "session", session.ID, "file_id", file.ID,
			"codec", opts.SourceCodec, "track", opts.TrackIndex)
		return
	}

	h.subtitleWarmWG.Add(1)
	go func() {
		defer h.subtitleWarmWG.Done()
		// The serve request may have pinned a committed artifact to skip its own
		// drift probe. That pin is a claim about one validated serve, and this
		// warm opens its own registration and validates it below, so it must not
		// be carried: inheriting it would let a serve-path eviction race fail the
		// warm and arm the per-identity failure cooldown for bytes the warm never
		// read.
		warmOpts := opts
		warmOpts.PinnedTextArtifact = nil
		// A recovery read is not opportunistic background work: the covering
		// extraction after a budget breach and the fill a refused bitmap page is
		// waiting for are both reads the viewer's next request depends on, so
		// they draw from the reserved recovery budget instead of being shed
		// behind a fill or a window that already holds the relay pipe.
		warmOpts.WaitingRelayDemux = breach || pageWaiting
		// The request that joins this read while it runs answers with the reason
		// that started it, not a generic source failure: the client keeps the
		// page/window state and honors the same Retry-After (see
		// StreamExtractOpts.WaitingRefusalReason).
		if breach {
			warmOpts.WaitingRefusalReason = playback.SubtitleWindowBudgetExceededReason
		} else if pageWaiting {
			warmOpts.WaitingRefusalReason = playback.SubtitlePageNotReadyReason
		}
		seed := inherited

		// A budget breach is recovered by warming the range the retry will ask
		// for, not by the playhead-grid prefetch below. The client re-anchors on
		// the live playhead, so by its retry its window starts later and ends
		// later than the breached one; the recovery warm covers the breached
		// range plus one full client window so the retries the 503 promises
		// actually land inside it (see virtualSubtitleBreachRecoveryWindows). A
		// grid prefetch of the same page length starts earlier and ends before
		// the retry's range does, so the covering lookup rejects it and the
		// retry pays the same read again.
		if breach {
			if windows, ok := virtualSubtitleBreachRecoveryWindows(opts); ok {
				extract, _, release := h.virtualSubtitleWarmExtractor(file, track, session, planTracks, seed, warmOpts, live)
				defer release()
				<-h.SubtitleCache.PrefetchWindowsInBackground(warmOpts, windows, extract, requestID)
				return
			}
		}

		// A known playhead prefers the bounded window prefetch. It covers the
		// pages the viewer is about to need without ever publishing a mid-file
		// slice as the whole track. The full-track warm below stays the
		// from-start/idle shape, where the artifact is what the opening playback
		// reads. A page waiting for the fill inverts that: the prefetch would
		// re-read the source for pages the fill is already publishing, so the
		// fill runs instead.
		if !pageWaiting {
			if windows, prefetch := virtualSubtitleWarmPrefetchWindows(opts, live.Position); prefetch {
				extract, _, release := h.virtualSubtitleWarmExtractor(file, track, session, planTracks, seed, warmOpts, live)
				defer release()
				<-h.SubtitleCache.PrefetchWindowsInBackground(warmOpts, windows, extract, requestID)
				return
			}
		}
		// Past the prefetch, only a text track may warm whole while playing (the
		// artifact is small and the next fetch reads it). A bitmap track falls
		// through only when the viewer is paused: a whole .sup is tens of
		// megabytes read from the entire container, and it is never the page
		// playback needs next. Two exceptions keep that admission from starving
		// a retry: a probe failure produced no page extract at all, so the
		// artifact is the retry's only path, and a page waiting for the fill
		// cannot be cut from the source at an affordable cost, so the fill is
		// what the page is waiting on.
		if !force && !pageWaiting && !live.IsPaused && playback.NeedsBurnIn(opts.SourceCodec) {
			slog.DebugContext(context.Background(), "virtual subtitle warm skipped: bitmap full-track warm stays paused-only",
				"component", "api", "session", session.ID, "file_id", file.ID, "codec", opts.SourceCodec)
			return
		}

		for attempt := 0; attempt < virtualSubtitleWarmRekeyAttempts; attempt++ {
			attemptOpts := warmOpts
			extract, rekeyed, release := h.virtualSubtitleWarmExtractor(file, track, session, planTracks, seed, attemptOpts, live)
			if pageWaiting {
				// A page refused for its read cost must be served by this fill, and
				// it is the page's own ledger that allows exactly one. A previous
				// failed warm's cooldown must not suppress it.
				//
				// The fill is deliberately *not* bounded by the page's wait (the
				// budget stays 0, i.e. the cache's warm timeout). A page wait is
				// ~90 s; a fill for a page deep into a large container parses from
				// byte zero and needs minutes. Bounding it by the wait made it end
				// exactly when the page gave up, so the retry restarted the same
				// read from zero and no page ever arrived — production logged
				// `page_wait_budget_spent` at 1m35s with 2 MB of artifact to show
				// for it. The fill's extent is now the session that is waiting on
				// it: the abort ticker above stops it on a seek or when that session
				// ends, and subtitleCacheWarmTimeout is the outer bound. A page
				// answered without the fill does not stop it either (see
				// pageRecoveryRefusal): its progress is what the next request needs.
				h.SubtitleCache.AllowWarmAfterPageRefusal(attemptOpts)
			}
			<-h.SubtitleCache.WarmTrackInBackgroundWithin(attemptOpts, extract, requestID, 0)
			next := rekeyed()
			release()
			if next == nil {
				return
			}
			slog.InfoContext(context.Background(), "virtual subtitle warm re-keyed after its drift probe",
				"component", "api", "session", session.ID, "file_id", file.ID,
				"from_track", attemptOpts.TrackIndex, "to_track", next.TrackIndex)
			// Start the re-keyed attempt from the identity the probe just
			// established, so its own probe confirms rather than re-derives a
			// different mapping.
			seed = &virtualSubtitleLayoutSeed{
				cacheIdentity: next.CacheIdentity,
				trackIndex:    next.TrackIndex,
				sourceCodec:   next.SourceCodec,
				verifiedAt:    time.Now(),
			}
			warmOpts = *next
		}
		slog.InfoContext(context.Background(), "virtual subtitle warm abandoned: identity kept moving under its drift probe",
			"component", "api", "session", session.ID, "file_id", file.ID,
			"attempts", virtualSubtitleWarmRekeyAttempts)
	}()
}

// virtualSubtitleWarmAtStartPositionSeconds is the playhead below which a
// window-miss warm keeps the full-track shape. A session at the start consumes
// the artifact from source zero, so the artifact is the work playback needs;
// prefetching windows there would only duplicate the slice the client already
// asked for and would never produce the artifact the opening fetch reads.
const virtualSubtitleWarmAtStartPositionSeconds = 1.0

// virtualSubtitleWarmPrefetchWindows returns the bounded windows a mid-file
// warm should prefetch, derived from the request grid the client walks: the
// grid cell the live playhead falls in and the cell after it. ok is false while
// the playhead is still at the start, where the full-track warm is the right
// shape.
//
// Deriving the windows from the playhead's grid cell — not from the triggering
// request's own start, and not from `position + lead` — is what makes a
// prefetched page the page the client asks for next. A playhead-anchored grid
// is offset from the client's by construction: production prefetched
// 1716.6-1836.6 while the next request was 1824.6-1944.6, and 1350-1470 while
// the next request was 1320-1440, so the covering lookup rejected every one of
// seven prefetches. A bitmap client asks for exactly the page containing the
// playhead (playback.SubtitleBitmapPageSeconds), so that page and the one after
// it are its next two requests.
func virtualSubtitleWarmPrefetchWindows(opts playback.StreamExtractOpts, position float64) ([]playback.SubtitlePrefetchWindow, bool) {
	if position <= virtualSubtitleWarmAtStartPositionSeconds {
		return nil, false
	}
	// Only a request that actually windowed establishes the grid the prefetch
	// mirrors. A duration-only ASS request or a PGS request without the windowed
	// opt-in is served as a whole slice, so mirroring it would cache slices no
	// later request computes a key for.
	if windowed, _, _, _ := opts.WindowCoverage(); !windowed {
		return nil, false
	}
	grid := playback.SubtitleRequestGridFor(opts.SourceCodec)
	start := grid.Start(position)
	windows := make([]playback.SubtitlePrefetchWindow, 0, 2)
	for i := 0; i < 2; i++ {
		windows = append(windows, playback.SubtitlePrefetchWindow{
			StartSeconds:    start + float64(i)*grid.CellSeconds,
			DurationSeconds: grid.PageSeconds,
		})
	}
	return windows, true
}

// virtualSubtitleBreachRecoveryWindows returns the bounded range a detached
// warm should extract after a request-path text window is killed at its budget.
//
// The retry a 503 asks for is not the request that breached: both subtitle
// clients re-anchor on the live playhead, which has moved on by the time the
// client retries, so its window starts later and ends later than the breached
// one. Extracting the breached start with a lead on the end therefore covers
// both an exact repeat and the shifted retries, while the ordinary
// playhead-grid prefetch — same page length, earlier start — ends before the
// retry's end and is rejected by the covering lookup.
//
// The lead is one full client window, not one grid cell. The warm's own build
// time is the reason: on the 4K source that produced the measurement a 150 s
// window extracted from the relay took 64.5 s, so by the time it landed the
// client had walked from 1680 s to 1740 s and asked for 1740–1860 — 30 s past
// a one-cell lead (1680–1830). It re-extracted at full price, twice. Since the
// client requests `[grid(playhead), +window)` and playback advances while the
// warm builds, the warm must reach at least one full window past the breached
// end for the retries it exists to serve to be inside it. That doubles the
// warm's span and therefore its read; it is detached, bounded by the cache's
// warm timeout and the two-warm budget, and it replaces repeated 26 s breaches
// with hits on the same bytes.
//
// Only a bounded text/ASS window can be recovered this way. A bitmap page has
// its own progressive artifact path, and an open-ended request has no end to
// extend, so both keep their existing admission.
func virtualSubtitleBreachRecoveryWindows(opts playback.StreamExtractOpts) ([]playback.SubtitlePrefetchWindow, bool) {
	if playback.IsPGS(opts.SourceCodec) {
		return nil, false
	}
	windowed, start, end, openEnded := opts.WindowCoverage()
	if !windowed || openEnded || end <= start {
		return nil, false
	}
	lead := playback.SubtitleRequestGridFor(opts.SourceCodec).PageSeconds
	return []playback.SubtitlePrefetchWindow{{
		StartSeconds:    start,
		DurationSeconds: (end - start) + lead,
	}}, true
}

// virtualSubtitleWarmExtractor builds the extract closure shared by the
// detached full-track warm and the bounded window prefetch. The cache supplies
// the options to extract with, so the full-track warm's zeroed bounds and the
// prefetch's window bounds are both honored, and the closure owns the relay
// registration for exactly the length of one extraction.
//
// admitted is the identity and ordinal the caller's fill was admitted under.
// The closure always probes its own relay registration, and when that probe
// settles on a different identity it reports the effective options through
// rekeyed and returns ErrSubtitleWarmCanceled without extracting. Bytes are
// therefore never published under a stale key. release releases the last
// resolved registration; the caller invokes it once the extraction settles.
func (h *StreamHandler) virtualSubtitleWarmExtractor(file *models.MediaFile, track models.SubtitleTrack, session *playback.Session, planTracks []models.SubtitleTrack, seed *virtualSubtitleLayoutSeed, admitted playback.StreamExtractOpts, live *playback.Session) (playback.SUPExtractFunc, func() *playback.StreamExtractOpts, func()) {
	var cleanup func()
	var rekeyed *playback.StreamExtractOpts
	extract := func(extractCtx context.Context, extractOpts playback.StreamExtractOpts) error {
		// A re-key decision belongs to the invocation that made it. The cache may
		// re-invoke this closure after a page yield (WarmTrackInBackground), and
		// that replay can establish the admitted identity and commit normally.
		// Clearing the decision here keeps a stale pointer from a yielded
		// invocation from re-keying a warm that has already settled under its
		// probe-established identity.
		rekeyed = nil
		// An external cancellation ends this invocation on someone else's terms:
		// an admitted page preempted the fill, or the warm's own abort fired. Its
		// re-key verdict is not ours to act on, because the cache may hand the
		// key to another warm instead of replaying this closure. Drop the verdict
		// on the way out too; otherwise the handler sees it after the warm
		// returns and starts a redundant full-track warm for the remapped track.
		// A verdict reached on an uncanceled invocation (a genuine drift remap)
		// still survives.
		defer func() {
			if extractCtx.Err() != nil {
				rekeyed = nil
			}
		}()
		// The cache may re-invoke this closure after yielding the relay pipe to
		// an admitted page (WarmTrackInBackground), or the warm loop may re-key
		// it. Release the previous attempt's registration before resolving a
		// fresh one so a restart cannot strand it.
		if cleanup != nil {
			cleanup()
			cleanup = nil
		}
		// Resolve on a child of the extraction context, never the detached
		// request context: a resolver that hangs is canceled when the warm budget
		// is exhausted, releasing the warm slot and fill instead of holding them
		// forever.
		warmCtx, cancelWarm := context.WithCancel(extractCtx)
		defer cancelWarm()
		if h.sessionMgr != nil {
			anchorPosition := live.Position
			anchorAt := time.Now()
			go func() {
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-warmCtx.Done():
						return
					case now := <-ticker.C:
						if reason := h.virtualSubtitleWarmCancelReason(session, anchorPosition, anchorAt, now); reason != "" {
							slog.DebugContext(warmCtx, "virtual subtitle warm canceled",
								"component", "api", "session", session.ID, "file_id", file.ID, "reason", reason)
							cancelWarm()
							return
						}
					}
				}
			}()
		}
		resolveCtx, cancel := context.WithTimeout(warmCtx, virtualSubtitleWarmResolveTimeout)
		defer cancel()
		resolved, resolvedCleanup, err := h.resolveVirtualInputURI(resolveCtx, file, session.UserID, session.ProfileID, false)
		if err != nil {
			if warmCtx.Err() != nil {
				// The abort ticker canceled an in-flight resolve: an
				// intentional abandonment, not a source failure, and it must
				// not arm the failure cooldown.
				return playback.ErrSubtitleWarmCanceled
			}
			logVirtualStreamFailure(resolveCtx, session.ID, file, err)
			return fmt.Errorf("resolve virtual input for subtitle warm: %w", err)
		}
		cleanup = resolvedCleanup
		extractOpts.InputPath = resolved.URL
		// Validate the warm's own relay registration against the live layout.
		// The serve's ordinal is at most a hint; this probe is the only authority
		// for the bytes this fill will hold.
		if err := h.resolveWarmExtractLayout(warmCtx, file, session, track, planTracks, seed, &extractOpts); err != nil {
			return err
		}
		if extractOpts.CacheIdentity != admitted.CacheIdentity ||
			extractOpts.TrackIndex != admitted.TrackIndex {
			// The probe settled on a different effective identity than the one
			// this fill was admitted under. Do not publish these bytes under the
			// stale key: abandon the fill (ErrSubtitleWarmCanceled is an
			// intentional yield, so it arms no cooldown) and report the identity
			// the probe established so the full-track warm can re-key onto it. A
			// prefetch simply drops the kick; the next window miss re-attempts it.
			next := admitted
			next.InputPath = extractOpts.InputPath
			next.CacheIdentity = extractOpts.CacheIdentity
			next.TrackIndex = extractOpts.TrackIndex
			next.SourceCodec = extractOpts.SourceCodec
			next.PinnedTextArtifact = nil
			rekeyed = &next
			return playback.ErrSubtitleWarmCanceled
		}
		err = playback.StreamExtractSubtitle(warmCtx, extractOpts)
		if warmCtx.Err() != nil {
			return playback.ErrSubtitleWarmCanceled
		}
		return err
	}
	release := func() {
		if cleanup != nil {
			cleanup()
			cleanup = nil
		}
	}
	return extract, func() *playback.StreamExtractOpts { return rekeyed }, release
}

// Defaults for the implicit first window served to a cold whole-track subtitle
// request against a large/unknown virtual/remote source. The web player asks
// for its own 120 s windows; this implicit bound is the same page for text
// clients that request a whole track and have no windowing contract. A 600 s
// text page measured 41-63 s per extract on a virtual source because ffmpeg
// demuxes the container up to the window end and discards it: the output is a
// few kilobytes, but the read is hundreds of megabytes. 120 s matches the
// window length the web player already requests, so the implicit range is one
// the client can adopt without learning a new cadence. The window starts on a
// span-aligned page that contains the session position, so a retry from the
// same page asks for the window the previous request produced instead of a
// range shifted a few seconds later (see implicitVirtualWindowStart).
const (
	virtualSubtitleImplicitWindowSeconds = 120.0
	// virtualSubtitleBitmapImplicitWindowSeconds is the implicit page for a
	// bitmap (PGS) track. A bitmap page can only be produced by demuxing the
	// container, so its cost scales with the page length: on a 50 GB / 2h52m
	// remux a 600 s page reads ~2.9 GB, which measured 65-105 s per request
	// against a client fetch deadline of the same order. Those pages were
	// abandoned before they completed, and an abandoned page is never stored,
	// so every retry repeated the whole read. 120 s is the page the web player
	// requests, and it completes in ~13 s on the same source: fast enough to
	// finish inside a fetch, and therefore able to answer the next request
	// from the covering-window cache. The span is shared with the request grid
	// (playback.SubtitleBitmapPageSeconds) so an explicit window, an implicit
	// window, and a prefetch all land on the same page.
	virtualSubtitleBitmapImplicitWindowSeconds = playback.SubtitleBitmapPageSeconds
	// virtualSubtitleImplicitWindowMinSourceBytes is the source size above
	// which a whole-track virtual read is considered untenable and is bounded
	// to the implicit window. A small known source is read whole (cheap and
	// complete); an unknown size (0) is treated as large, because a virtual
	// row's size is not always populated.
	virtualSubtitleImplicitWindowMinSourceBytes = 256 << 20
)

// applyImplicitVirtualWindow bounds a cold whole-track subtitle fetch against a
// large/unknown virtual/remote source to the first playback-sized window
// instead of an unbounded whole-container extract. It applies to every
// sidecar class the server can serve — converted text (WebVTT), lossless
// ASS/SSA, and PGS — because a virtual source is served over HTTP from a
// provider and a complete extract of any class requires demuxing every byte of
// the container. A 6.6 GB remux therefore costs ~100 s of sequential reads
// before the response can complete, while the client's fetch deadline is
// ~30-60 s; the request is aborted and the partial fill is discarded, so
// retrying never makes progress. Local files are cheap to read whole and keep
// the existing whole-track behavior.
//
// The window is only synthesized when the caller did not ask for a specific
// window, the source is not small/known, and no committed full-track artifact
// exists. A windowed serve is fast (ffmpeg seeks near the requested position)
// and the handler starts the usual detached full-track warm afterwards, so this
// request completes in seconds and later requests — or a client that fetched
// this window — read the small cached artifact. When an artifact is already
// present the whole track is served, so an established virtual track still
// returns every cue in one response.
//
// Correctness: this changes what a cold, implicit whole-track virtual response
// contains — a bounded window rather than every cue — so a client that never
// re-requests will stop seeing cues past the window. The response advertises
// the covered range with X-Subtitle-Coverage/X-Subtitle-Windowed so such a
// client can tell and request subsequent windows. Explicitly-windowed callers
// (the web player) are unaffected, and local and small/known sources keep their
// whole-track paths. An explicit position/duration is honored exactly as asked
// and bypasses this default; a client-managed window is never widened.
//
// This deliberately bends the documented default in
// docs/architecture/playback-protocol-v3.md §4.2/§8 ("embedded
// URLs return the complete track from source time zero by default; consumers
// that maintain a sliding window may explicitly supply position and duration").
// A remote multi-GB source makes that default unservable within a client fetch
// deadline. The fully contract-conformant fix is for every client to request
// windows the way the web player does (or a v3 contract amendment making
// windowed sidecars the default for virtual sources); this helper is a
// server-side stopgap until that lands. It is gated to large/unknown virtual
// sources so nothing else changes.
//
// Which cases stay whole-track, and why:
//   - local files: cheap to read whole, and bounding would silently drop cues;
//   - small known sources (< virtualSubtitleImplicitWindowMinSourceBytes):
//     cheap to read whole;
//   - an already-committed full-track artifact: cheap to serve whole and the
//     validated bytes a pinned text artifact promised;
//   - an explicit client window with a duration (position+duration, or PGS
//     ?windowed=1 with a duration): authoritative, never overridden.
//
// A position without a duration is window intent but open-ended: its missing
// cap is filled with the implicit window (the caller's position is kept) so it
// cannot demux to EOF and advertise `start-*`. A PGS request is granted
// AllowWindow for this server-authored window even without the client's
// ?windowed=1 opt-in; the response coverage headers are what tell such a
// client to re-fetch at the window boundary.
func (h *StreamHandler) applyImplicitVirtualWindow(opts *playback.StreamExtractOpts, file *models.MediaFile, session *playback.Session, virtualActive bool) {
	if h == nil || opts == nil || !virtualActive {
		return
	}
	// A small known source costs little to read whole, so keep the complete
	// artifact. Unknown size (0) stays windowed: virtual rows do not always
	// carry a populated size.
	if file != nil && file.FileSize > 0 && file.FileSize < virtualSubtitleImplicitWindowMinSourceBytes {
		return
	}
	// An explicit duration (with or without a position) is authoritative;
	// leave it exactly as the client asked.
	if opts.DurationSeconds > 0 {
		return
	}
	// A committed full-track artifact is cheap to serve whole, so do not slice
	// it. (A pinned text artifact resolves as committed here, so the
	// drift-probe skip path also serves whole.)
	if h.SubtitleCache.HasCommittedEntry(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, opts.SourceCodec, opts.TargetFormat) {
		return
	}
	// The page length is a per-class decision: a text page is cheap to slice
	// 600 s at a time, a bitmap page is not (its cost is the container read).
	spanSeconds := virtualSubtitleImplicitWindowSeconds
	if playback.IsPGS(opts.SourceCodec) {
		spanSeconds = virtualSubtitleBitmapImplicitWindowSeconds
	}
	// A position without a duration is window intent but open-ended: fill in the
	// implicit window so it cannot extract to EOF (advertised as `start-*`). The
	// caller's position is kept; only the missing cap is supplied.
	if opts.ClampOpenEndedWindow(spanSeconds) {
		slog.Info("virtual subtitle open window clamped to implicit window",
			"component", "api",
			"codec", opts.SourceCodec,
			"seek_seconds", opts.SeekSeconds,
			"duration_seconds", opts.DurationSeconds,
			"track", opts.TrackIndex)
		return
	}
	// Any other explicit intent is authoritative. A PGS `?windowed=1` opt-in
	// with no position or duration is not: it only selects the windowed serve
	// path, and PGSWindowRequest parses bounds only alongside that flag, so a
	// bare flag would otherwise demux the whole container to EOF. Fall through
	// and give it the same implicit window a cold whole-track fetch gets; the
	// coverage headers tell the client to re-fetch.
	if opts.WindowRequested || opts.SeekSeconds > 0 {
		return
	}

	opts.WindowRequested = true
	opts.SeekSeconds = implicitVirtualWindowStart(session, spanSeconds)
	opts.DurationSeconds = spanSeconds
	if playback.IsPGS(opts.SourceCodec) {
		// PGS routes through ServeSUPExtract, which windows only when
		// AllowWindow is set (the client's ?windowed=1 opt-in or a server-
		// authored window like this one), so grant it here.
		opts.AllowWindow = true
	}
	slog.Info("virtual subtitle served as an implicit window",
		"component", "api",
		"codec", opts.SourceCodec,
		"seek_seconds", opts.SeekSeconds,
		"duration_seconds", opts.DurationSeconds,
		"track", opts.TrackIndex)
}

// implicitVirtualWindowStart returns the source-time start of the implicit
// window: the span-aligned page that contains the session position, or zero
// when the session has no meaningful position yet (a fresh start, so the page
// covers the opening of the track).
//
// Alignment is what makes a repeated whole-track fetch cheap. The covering
// window cache only answers a request whose range a stored page fully
// contains, so a page anchored to the live playhead (position minus a small
// backoff) is never reused: the client's next fetch is a few seconds later, its
// range extends past the page it just received, and the server pays the whole
// extraction again. Snapping to the page grid makes every request inside one
// page name the same window, so the first completed page serves the rest of
// them — and so the prefetch grid derived from the request mirrors real pages.
func implicitVirtualWindowStart(session *playback.Session, spanSeconds float64) float64 {
	if session == nil || spanSeconds <= 0 || session.Position <= 0 {
		return 0
	}
	start := math.Floor(session.Position/spanSeconds) * spanSeconds
	if start < 0 {
		return 0
	}
	return start
}

// snapBitmapWindowToPage moves an explicit bitmap window request onto the page
// grid the client's request path walks, so the page this request produces is
// the page every later request inside it asks for.
//
// A playhead-anchored bitmap request is never reusable: the next request starts
// a few seconds later, its range runs past the slice the previous one produced,
// and the covering-window lookup correctly rejects it. Production showed the
// cost of leaving it anchored: two identical 229,103-byte extracts 5 s apart
// (zero reuse) and a blank gap at every page boundary while the new page was
// read.
//
// The snap keeps the caller's range covered. Extraction starts on the page
// containing the requested start and ends on the last page boundary the
// requested range reaches, so an aligned client (the web player asks for
// exactly one page) is unchanged, and an older client that still anchors on the
// playhead gets whole pages instead of a slice nothing can reuse.
// X-Subtitle-Coverage then advertises exactly the range ffmpeg produced, and the
// slice is stored under that range only — a page is never published under the
// whole-track artifact key.
//
// It is a no-op for every non-bitmap codec, for a whole-track request, and for
// an open-ended (start-only) window, which has no page end to snap to.
func snapBitmapWindowToPage(opts *playback.StreamExtractOpts) (bool, float64, float64) {
	if opts == nil || !playback.IsPGS(opts.SourceCodec) {
		return false, 0, 0
	}
	windowed, start, end, openEnded := opts.WindowCoverage()
	if !windowed || openEnded {
		return false, 0, 0
	}
	grid := playback.SubtitleRequestGridFor(opts.SourceCodec)
	pageStart := grid.Start(start)
	pageEnd := pageStart + grid.PageSeconds
	if end > pageEnd {
		// The request reaches into a later page: cover it, and stop on a page
		// boundary so what is stored is whole pages a later request can still
		// be measured against.
		pageEnd = math.Ceil(end/grid.PageSeconds) * grid.PageSeconds
	}
	if pageStart == start && pageEnd == end {
		return false, 0, 0
	}
	opts.SeekSeconds = pageStart
	opts.DurationSeconds = pageEnd - pageStart
	opts.WindowRequested = true
	return true, pageStart, pageEnd
}

// verifyVirtualSubtitleLayout probes the live relay input once and, when its
// subtitle layout drifted from the layout this request's ordinal was resolved
// against (expected), re-maps the extract options onto a same-class live track.
// It reports whether extraction may proceed, plus the probe error when the
// probe itself could not run. proceed=false means a successful probe positively
// found that the live source cannot satisfy the requested representation —
// rotation to a different subtitle class, or an ambiguous or absent match — and
// the caller must answer with a clean retryable 4xx before ffmpeg spawns or
// headers commit. A non-nil probeErr means the live layout could not be
// established: the caller must fail closed for every codec, because serving the
// plan ordinal unverified can emit the wrong release's track and a
// same-ordinal rotation never trips the post-spawn map net. Virtual inputs are
// request-local probe state; the session's published evidence is never
// rewritten.
func (h *StreamHandler) verifyVirtualSubtitleLayout(ctx context.Context, file *models.MediaFile, requestedTrack models.SubtitleTrack, expected []models.SubtitleTrack, opts *playback.StreamExtractOpts) (bool, error) {
	if opts == nil || strings.TrimSpace(opts.InputPath) == "" {
		return true, nil
	}
	budget := remoteProbeBudget(ctx, file, playback.RemoteProbeFloor, playback.SubtitleProbeMax)
	if budget > playback.RemoteProbeFloor {
		slog.InfoContext(ctx, "virtual subtitle layout probe extended for a slow provider link",
			"component", "api",
			"track_codec", requestedTrack.Codec,
			"budget", budget,
			"observed_link_latency", recentVirtualProviderLinkLatency(virtualProviderLinkLatencyKey(file)))
	}
	liveTracks, err := playback.ProbeSubtitleLayoutWithin(ctx, h.ffmpegPath(), opts.InputPath, budget)
	if err != nil {
		// A probe failure (context canceled, relay timeout, transient upstream
		// error) is not evidence that the pinned source rotated, but without
		// the live layout there is nothing to validate the plan ordinal
		// against. The caller fails closed with a retryable response; a client
		// falls back to subtitles-off, which the server already treats as
		// legitimate. The error is returned so the caller can distinguish it
		// from a positive mismatch.
		//
		// A cancellation is the viewer leaving (a replan or a navigation away
		// aborts the probe on its request context), so it is debug detail, not
		// a source warning.
		if isClientCancellation(ctx, err) {
			slog.DebugContext(ctx, "virtual subtitle layout probe abandoned by the client", "component", "api",
				"track_codec", requestedTrack.Codec,
				"error", err)
			return true, err
		}
		slog.WarnContext(ctx, "virtual subtitle layout probe failed", "component", "api",
			"track_codec", requestedTrack.Codec,
			"error", err)
		return true, err
	}
	if playback.SubtitleLayoutsEqual(liveTracks, expected) {
		// The release the ordinal was resolved against is unchanged. The plan
		// ordinal already names the live layout.
		return true, nil
	}
	liveOrdinal, liveTrack, matched := playback.MatchEmbeddedSubtitleTrack(requestedTrack, liveTracks)
	if !matched {
		// A requested language that no live track declares is not by itself a
		// rotation: the plan's language came from the provider's metadata, and a
		// container tag can say `und`, or the plan inventory can carry a
		// downloaded sidecar the live probe cannot see. Extraction addresses the
		// ordinal, so if the plan ordinal still names a live track of the same
		// extraction class, the bytes this track produces are the bytes it
		// produced before. Refusing here discarded a successful extract: the page
		// at position=2160 answered 200, the next page's warm probe found "no
		// match for the planned track", and every later page was answered
		// `subtitle_source_changed` — the viewer's only recovery was to replan,
		// and the replan kept producing the same metadata.
		if liveTrack, ok := planOrdinalStillCarries(requestedTrack, liveTracks, opts.TrackIndex, opts.TargetFormat); ok {
			slog.InfoContext(ctx, "virtual subtitle layout differs only in metadata; keeping the plan ordinal", "component", "api",
				"plan_ordinal", opts.TrackIndex,
				"plan_codec", requestedTrack.Codec,
				"plan_language", requestedTrack.Language,
				"live_codec", liveTrack.Codec,
				"live_language", liveTrack.Language)
			return true, nil
		}
		// "No track in this class" and "the class is there in other languages"
		// are different problems — one is a rotated layout, the other is a
		// selection that no longer exists — and they used to log identically.
		classCount, classLanguages := playback.SubtitleLayoutClassInventory(requestedTrack.Codec, liveTracks)
		slog.WarnContext(ctx, "virtual subtitle layout rotated without a usable match", "component", "api",
			"requested_codec", requestedTrack.Codec,
			"requested_language", requestedTrack.Language,
			"requested_muxer_class", playback.SubtitleLayoutClass(requestedTrack.Codec),
			"live_subtitle_count", len(liveTracks),
			"live_class_track_count", classCount,
			"live_class_languages", strings.Join(classLanguages, ","))
		return false, nil
	}
	// Class preservation is the hard rule: the URL extension was minted at
	// plan time, so a re-map may only land on a codec whose extraction uses
	// the same output muxer.
	if playback.SubtitleExtractMuxer(requestedTrack.Codec, opts.TargetFormat) != playback.SubtitleExtractMuxer(liveTrack.Codec, opts.TargetFormat) {
		slog.WarnContext(ctx, "virtual subtitle remap rejected: output muxer mismatch", "component", "api",
			"plan_codec", requestedTrack.Codec,
			"live_codec", liveTrack.Codec)
		return false, nil
	}
	planOrdinal := opts.TrackIndex
	opts.TrackIndex = liveOrdinal
	opts.SourceCodec = liveTrack.Codec
	slog.InfoContext(ctx, "virtual subtitle track remapped onto live layout", "component", "api",
		"plan_ordinal", planOrdinal,
		"live_ordinal", liveOrdinal,
		"codec", liveTrack.Codec,
		"language", liveTrack.Language)
	return true, nil
}

// planOrdinalStillCarries reports whether the plan ordinal still names a live
// track whose extraction produces the same output class as the requested plan
// track, with no language that contradicts the plan. It is the fallback for a
// live layout that differs from the plan only in metadata: the provider listing
// says `fre` while the container tag says `und` or nothing, or the plan
// inventory carries a sidecar the embedded probe cannot see. `MatchEmbeddedSubtitleTrack`
// applies language as a hard constraint, so it finds no candidate there and the
// rotation verdict called a metadata gap a release rotation.
//
// The ordinal is what ffmpeg is told to map (`0:s:N`), so a same-class track at
// that ordinal yields the subtitles the plan promised. A live track that
// *contradicts* the plan's language is a rotation, not drift, and is not
// carried: serving English where French was selected is a real mis-serve. Nor
// is a class or muxer difference, which would change the response's format
// under a URL minted at plan time.
func planOrdinalStillCarries(requested models.SubtitleTrack, live []models.SubtitleTrack, ordinal int, targetFormat string) (models.SubtitleTrack, bool) {
	class := playback.SubtitleLayoutClass(requested.Codec)
	if class == "" || ordinal < 0 || ordinal >= len(live) {
		return models.SubtitleTrack{}, false
	}
	liveTrack := live[ordinal]
	if playback.SubtitleLayoutClass(liveTrack.Codec) != class {
		return models.SubtitleTrack{}, false
	}
	if playback.SubtitleExtractMuxer(requested.Codec, targetFormat) != playback.SubtitleExtractMuxer(liveTrack.Codec, targetFormat) {
		return models.SubtitleTrack{}, false
	}
	requestedLanguage := lang.Canonical(requested.Language)
	liveLanguage := lang.Canonical(liveTrack.Language)
	// `und` is the container saying "no language tagged", not a language that
	// contradicts the plan. Only a *named* language that differs is a rotation.
	if subtitleLanguageDiffers(requestedLanguage, liveLanguage) {
		return models.SubtitleTrack{}, false
	}
	return liveTrack, true
}

// subtitleLanguageDiffers reports whether two canonical language tags name
// different languages. An absent or `und`-tagged side is unknown, not different.
func subtitleLanguageDiffers(a, b string) bool {
	known := func(tag string) bool { return tag != "" && tag != audioLanguageUndetermined }
	return known(a) && known(b) && a != b
}

// isRelayTokenNotFoundError reports whether an ffmpeg extraction failure is
// a relay-own 404: the relay URL (always loopback) answered 404, which means
// our registration lapsed (LRU eviction, 24h expiry) — never a dead release,
// because upstream 404s arrive collapsed as 502. Callers answer retryable so
// the client's next fetch re-resolves a fresh token instead of spending the
// terminal budget on our bookkeeping.
func isRelayTokenNotFoundError(err error, inputPath string) bool {
	if err == nil {
		return false
	}
	parsed, parseErr := url.Parse(strings.TrimSpace(inputPath))
	if parseErr != nil || parsed == nil {
		return false
	}
	switch parsed.Hostname() {
	case "127.0.0.1", "::1":
	default:
		return false
	}
	return strings.Contains(err.Error(), "Server returned 404")
}

// clearSubtitleCoverageHeaders removes the bounded-window markers before an
// error response is written. SetSubtitleCoverageHeader records the window
// before the serve so a successful response advertises its covered range, but
// an error that never committed a 200 must not be readable as a bounded window
// by a header-only classifier. Safe to call before the headers are set.
func clearSubtitleCoverageHeaders(h http.Header) {
	if h == nil {
		return
	}
	h.Del(playback.SubtitleCoverageHeader)
	h.Del(playback.SubtitleWindowedHeader)
}

// writeVirtualAudioSelectionInvalid answers a retryable 409 when a virtual
// session's committed audio selection is no longer present in the inventory a
// deferred probe verified. The session marks the selection invalid rather than
// keeping a stale positional index that now names another track, so the
// transport must fail closed instead of muxing whatever stream occupies the old
// slot; the client replans and re-selects. Retryable 4xx, never a 500 or an
// ambiguous partial stream, matching the subtitle rotation response.
func writeVirtualAudioSelectionInvalid(w http.ResponseWriter) {
	writeError(w, http.StatusConflict, "audio_selection_invalid",
		"The selected audio track is no longer available; replan and choose another")
}

// writeSubtitleExtractFailure answers an extraction that produced no response
// bytes and is not a layout rotation.
//
// A source read that ran out its own extract deadline, an upstream 5xx under
// ffmpeg, or a relay token that lapsed mid-flight are all the same shape from the
// client's side: the extraction command is fine and a retry may succeed. They
// answer the retryable 503 the client already backs off from, never a 500, so a
// slow provider does not spend the client's terminal failure budget. Anything
// else is a real extraction failure and stays loud.
func writeSubtitleExtractFailure(w http.ResponseWriter, extractErr error, inputPath string) {
	clearSubtitleCoverageHeaders(w.Header())
	if errors.Is(extractErr, context.DeadlineExceeded) ||
		playback.IsSubtitleUpstreamError(extractErr) ||
		isRelayTokenNotFoundError(extractErr, inputPath) {
		writeSubtitleSourceUnavailable(w)
		return
	}
	writeError(w, http.StatusInternalServerError, "subtitle_extract_failed", "Failed to extract subtitles")
}

// writeSubtitleSourceChanged answers a clean retryable 4xx when a virtual
// release rotated so the requested subtitle representation can no longer be
// produced from the live source. Clients already retry through the
// sliding-window fetcher / replan flow, so the response is deliberately a
// retryable 4xx, never a 500 or an ambiguous partial stream.
func writeSubtitleReadsBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(playback.SubtitleReadsRetryAfter))
	writeError(w, http.StatusServiceUnavailable, "service_unavailable", "The server is busy; try again in a moment")
}

func writeSubtitleSourceChanged(w http.ResponseWriter) {
	clearSubtitleCoverageHeaders(w.Header())
	// A compliant client waits before replaying the same subtitle request; the
	// rotation verdict is final for that ordinal, so an immediate retry can only
	// re-earn it. Clients that ignore the header keep their existing cadence.
	w.Header().Set("Retry-After", strconv.Itoa(subtitleSourceRetryAfterSeconds))
	writeError(w, http.StatusConflict, "subtitle_source_changed",
		"The selected subtitle track changed on the media source; retry")
}

// writeSubtitleSourceUnavailable answers a retryable 503 when a virtual bitmap
// (PGS) source's live layout could not be established before its .sup response
// would commit 200. A .sup commits its status before ffmpeg spawns and has no
// post-spawn recovery, so the server fails closed rather than stream a
// possibly-truncated track; the client can retry once the source settles.
func writeSubtitleSourceUnavailable(w http.ResponseWriter) {
	writeSubtitleSourceUnavailableRetry(w, subtitleSourceRetryAfterSeconds*time.Second)
}

// subtitleSourceRetryAfterSeconds is the default Retry-After for a retryable
// subtitle source failure that did not come with an explicit backoff (for
// example a failed live-layout probe). The client already retries on its own
// cadence; the header tells a compliant client how long the server prefers to
// wait before re-opening the provider.
const subtitleSourceRetryAfterSeconds = 5

// writeSubtitleSourceUnavailableRetry answers a retryable 503 with a
// server-chosen Retry-After. RetryAfter is rounded up to whole seconds and
// floored at one so the header is always a positive delay.
func writeSubtitleSourceUnavailableRetry(w http.ResponseWriter, retryAfter time.Duration) {
	writeSubtitleRetry(w, &playback.SubtitleExtractionRetryError{RetryAfter: retryAfter})
}

// writeSubtitleRetry answers a subtitle extraction that must be retried: the
// retryable 503 with Retry-After that clients already back off from. Two
// additive parts carry the reason and the state:
//
//   - Reason replaces the default problem code when the extraction knows a more
//     specific cause (a page whose read cost was refused);
//   - CoveredUntil advertises how far the track's progressive bitmap artifact
//     has parsed, so a client can tell that the page it asked for is not ready
//     yet rather than unavailable.
//
// Neither changes the status, the header shape or the client's retry loop.
func writeSubtitleRetry(w http.ResponseWriter, retry *playback.SubtitleExtractionRetryError) {
	clearSubtitleCoverageHeaders(w.Header())
	seconds := int(math.Ceil(retry.RetryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	if retry.CoveredUntil > 0 || isSubtitlePageReason(retry.Reason) {
		// The requested page is not ready; the artifact behind it has parsed
		// this range and will answer once it reaches the window. A page reason
		// with nothing parsed yet advertises 0 so the header reflects what is
		// actually covered instead of disappearing and reading as unknown.
		playback.SetSubtitleCoverageRange(w.Header(), 0, retry.CoveredUntil)
	}
	if retry.Reason == playback.SubtitlePageTooCostlyReason {
		writeError(w, http.StatusServiceUnavailable, playback.SubtitlePageTooCostlyReason,
			"The subtitle page costs more to read than the server allows; retry to be served from the prefetched track")
		return
	}
	if retry.Reason == playback.SubtitlePageNotReadyReason {
		writeError(w, http.StatusServiceUnavailable, playback.SubtitlePageNotReadyReason,
			"The subtitle page is not parsed yet; retry to be served from the prefetched track")
		return
	}
	if retry.Reason == playback.SubtitleWindowBudgetExceededReason {
		writeError(w, http.StatusServiceUnavailable, playback.SubtitleWindowBudgetExceededReason,
			"The subtitle window read past its time budget; retry to be served from the recovery extraction")
		return
	}
	writeError(w, http.StatusServiceUnavailable, "subtitle_source_unavailable",
		"Unable to verify the subtitle source; retry")
}

// isSubtitlePageReason reports whether the reason names a bitmap page whose
// progressive artifact is what answers it. Those refusals always advertise the
// covered range, even when the artifact has parsed nothing yet.
func isSubtitlePageReason(reason string) bool {
	return reason == playback.SubtitlePageTooCostlyReason || reason == playback.SubtitlePageNotReadyReason
}

// writeSubtitleProbeFailure answers a live-layout probe that could not
// establish the source's subtitle layout. A probe the client canceled is the
// viewer leaving, not a source failure: the probe runs on the request context
// and a player that replanned or navigated away aborts it within milliseconds.
// Answering 503 there recorded a dependency failure (and an ERROR) for a 74 ms
// request nobody was waiting on, so a cancellation is classified as an abandoned
// request exactly like the media proxy's (see virtualProxyFailureStatus).
// Everything else keeps the retryable 503 the client backs off from.
func writeSubtitleProbeFailure(w http.ResponseWriter, ctx context.Context, probeErr error) {
	if isClientCancellation(ctx, probeErr) {
		clearSubtitleCoverageHeaders(w.Header())
		writeErrorCause(w, http.StatusRequestTimeout, virtualProxyDispositionClientCanceled,
			"Client closed the request", probeErr)
		return
	}
	writeSubtitleSourceUnavailable(w)
}

// subtitleExtractWindow resolves the extraction window for an embedded
// subtitle request from its explicit query parameters. WebVTT and ASS slices
// read ?position/?duration directly; the explicit window intent is carried
// separately on StreamExtractOpts.WindowRequested (see subtitleWindowRequested)
// so a position=0 window is not mistaken for a whole-track fetch. PGS requires
// the explicit ?windowed=1 opt-in via PGSWindowRequest so a whole-track consumer
// never silently loses cues outside an implicit window.
func subtitleExtractWindow(r *http.Request, outFormat string) (allowWindow bool, seek, duration float64) {
	switch outFormat {
	case "vtt", subtitleFormatASS:
		return false, subtitleSeekPosition(r), subtitleWindowDuration(r)
	case subtitleFormatSUP:
		return playback.PGSWindowRequest(r.URL.Query())
	}
	return false, 0, 0
}

// subtitleWindowRequested reports whether the caller explicitly asked for a
// bounded window by supplying a position parameter. A present position counts
// even at zero: position=0&duration=600 is the first bounded slice, whereas a
// request that omits position (and duration) is a whole-track fetch. Only an
// explicit position sets the intent; a duration-only request keeps its existing
// behavior (text is still capped by DurationSeconds, ASS stays whole-track), so
// this cannot silently turn an ordinary artifact fetch into a window.
func subtitleWindowRequested(r *http.Request) bool {
	raw := strings.TrimSpace(r.URL.Query().Get("position"))
	if raw == "" {
		return false
	}
	v, err := strconv.ParseFloat(raw, 64)
	return err == nil && v >= 0 && !math.IsInf(v, 0) && !math.IsNaN(v)
}

// subtitleSeekPosition uses only the caller's explicit position. Session
// progress must never silently remove cues from a complete subtitle artifact.
func subtitleSeekPosition(r *http.Request) float64 {
	if raw := r.URL.Query().Get("position"); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 && !math.IsInf(v, 0) && !math.IsNaN(v) {
			return v
		}
	}
	return 0
}

// subtitleWindowDuration bounds extraction only when the client explicitly
// requests a valid duration. Ordinary artifact consumers fetch the whole track.
func subtitleWindowDuration(r *http.Request) float64 {
	const maxDuration = 3600.0
	if raw := r.URL.Query().Get("duration"); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 && v <= maxDuration {
			return v
		}
	}
	return 0
}
