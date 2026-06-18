package playback

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// TranscodeRuntimeConfig is the subset of playback configuration the transcode
// manager needs to (re)start ffmpeg. It is a small, config-package-free struct so
// internal/playback does not import internal/config (avoiding an import cycle);
// each embedding handler adapts its own config snapshot into this shape.
type TranscodeRuntimeConfig struct {
	TranscodeDir string
	FFmpegPath   string
	HWAccel      string
	HWDevice     string
}

// sessionReconstructor is the SessionManager capability used to re-register a
// session under an existing ID during reconstruct. *SessionManager implements it.
type sessionReconstructor interface {
	RegisterReconstructed(s *Session) *Session
}

// TranscodeManager owns the transcode-session lifecycle shared by every playback
// front end (native API and jellycompat): the live in-memory transcode map, the
// recipe-card persistence used to reconstruct a session after a server restart,
// and the reconstruct machinery (single-flight + concurrency cap) that rebuilds a
// lost ffmpeg from a card. Both PlaybackHandlers embed one and delegate to it so
// the card lifetime rules, the reconstruct cap, and the node-affinity constraint
// live in exactly one place.
//
// Dependencies are injected as function fields so an embedding handler can wire
// them lazily from its own (often late-set) fields without an ordering hazard.
type TranscodeManager struct {
	// StoreFn returns the recipe-card store, or nil when persistence is not
	// wired (reconstruct disabled — behavior identical to before the feature).
	StoreFn func() RecipeStore
	// Sessions re-registers a reconstructed session under its existing id.
	Sessions sessionReconstructor
	// Config returns the current transcode runtime config (ffmpeg path, dir,
	// hwaccel) so operator changes apply to newly (re)started transcodes.
	Config func() TranscodeRuntimeConfig
	// LogSinkFn returns the ffmpeg log sink for reconstructed processes.
	LogSinkFn func() FFmpegLogSink
	// JWTSecretFn returns the bearer used for remote transcode-node DELETEs.
	JWTSecretFn func() string
	// OnFFmpegCrash is invoked when a reconstructed/local ffmpeg exits with an
	// error so the embedding handler can tear down the playback session (keeping
	// the card, so a resume can respawn). No-op when nil.
	OnFFmpegCrash func(ctx context.Context, sessionID string)
	// StartThrottler optionally starts the segment throttler for a (re)started
	// transcode, reading the embedding handler's settings. No-op when nil.
	StartThrottler func(ctx context.Context, ts *TranscodeSession)

	transcodeMu sync.RWMutex
	transcodes  map[string]*TranscodeSession

	recipeRefreshMu sync.Mutex
	recipeRefreshAt map[string]time.Time

	// reconstructGroup single-flights transcode reconstruction per session id so
	// concurrent manifest/segment requests for a lost session spawn exactly one
	// ffmpeg writing to the shared output directory, never a racing duplicate.
	reconstructGroup singleflight.Group
	// reconstructSem bounds how many transcodes may be reconstructed (ffmpeg
	// re-spawned) at once. After a restart, every buffered client re-requests at
	// once; without a cap that is a thundering herd of simultaneous cold-start
	// ffmpeg launches. The semaphore paces the burst — sessions still all
	// reconstruct, just not all in the same instant. Lazily sized on first use.
	reconstructSemOnce sync.Once
	reconstructSem     chan struct{}
}

// NewTranscodeManager returns a manager with its internal maps initialized. The
// caller wires the dependency function fields before use.
func NewTranscodeManager() *TranscodeManager {
	return &TranscodeManager{
		transcodes:      make(map[string]*TranscodeSession),
		recipeRefreshAt: make(map[string]time.Time),
	}
}

func (m *TranscodeManager) store() RecipeStore {
	if m.StoreFn == nil {
		return nil
	}
	return m.StoreFn()
}

func (m *TranscodeManager) jwtSecret() string {
	if m.JWTSecretFn == nil {
		return ""
	}
	return m.JWTSecretFn()
}

func (m *TranscodeManager) logSink() FFmpegLogSink {
	if m.LogSinkFn == nil {
		return nil
	}
	return m.LogSinkFn()
}

func (m *TranscodeManager) runtimeConfig() TranscodeRuntimeConfig {
	if m.Config == nil {
		return TranscodeRuntimeConfig{TranscodeDir: filepath.Join(os.TempDir(), "silo-transcode")}
	}
	return m.Config()
}

// defaultReconstructConcurrency caps simultaneous transcode reconstructs when no
// explicit limit is configured. One in-flight ffmpeg launch per CPU paces the
// post-restart spawn burst without starving a host that genuinely ran many
// concurrent transcodes before the restart.
func defaultReconstructConcurrency() int {
	if n := runtime.NumCPU(); n > 0 {
		return n
	}
	return 4
}

// acquireReconstructSlot blocks until a reconstruct slot is free or the request
// context is canceled. It returns a release func and true on success, or a nil
// func and false if the caller gave up (so the burst does not queue work no one
// is waiting for). The semaphore is lazily initialized so struct-literal-built
// managers (tests) work without a constructor.
func (m *TranscodeManager) acquireReconstructSlot(ctx context.Context) (func(), bool) {
	m.reconstructSemOnce.Do(func() {
		if m.reconstructSem == nil {
			m.reconstructSem = make(chan struct{}, defaultReconstructConcurrency())
		}
	})
	select {
	case m.reconstructSem <- struct{}{}:
		return func() { <-m.reconstructSem }, true
	case <-ctx.Done():
		return nil, false
	}
}

// GetTranscodeSession returns the live in-memory transcode session for sessionID,
// or nil if none is registered.
func (m *TranscodeManager) GetTranscodeSession(sessionID string) *TranscodeSession {
	if m == nil {
		return nil
	}
	m.transcodeMu.RLock()
	defer m.transcodeMu.RUnlock()
	return m.transcodes[sessionID]
}

// RegisterTranscodeSession inserts a freshly started transcode session into the
// live map. Used by the normal (non-reconstruct) start paths.
func (m *TranscodeManager) RegisterTranscodeSession(sessionID string, ts *TranscodeSession) {
	m.transcodeMu.Lock()
	m.transcodes[sessionID] = ts
	m.transcodeMu.Unlock()
}

// GetOrRegisterTranscodeSession atomically returns the existing session for
// sessionID, or registers and returns newSession when none exists. The bool
// reports whether newSession was stored; false means an existing session won the
// race and the caller should close newSession. This lets concurrent start paths
// (e.g. multiple compat manifest requests) avoid registering duplicate ffmpegs.
func (m *TranscodeManager) GetOrRegisterTranscodeSession(sessionID string, newSession *TranscodeSession) (*TranscodeSession, bool) {
	m.transcodeMu.Lock()
	defer m.transcodeMu.Unlock()
	if existing := m.transcodes[sessionID]; existing != nil {
		return existing, false
	}
	m.transcodes[sessionID] = newSession
	return newSession, true
}

// recipeEnabled reports whether transcode reconstruct persistence is wired.
func (m *TranscodeManager) recipeEnabled() bool {
	s := m.store()
	return s != nil && s.Enabled()
}

// SaveRecipeCard persists the recipe card for a freshly started transcode so it
// can be reconstructed after a restart. Best-effort: a store error must not fail
// playback start.
func (m *TranscodeManager) SaveRecipeCard(ctx context.Context, session *Session, transcodeNodeURL string, opts TranscodeOpts) {
	if !m.recipeEnabled() || session == nil {
		return
	}
	card := NewRecipeCard(session.UserID, session.ProfileID, session.MediaFileID, transcodeNodeURL, opts)
	if err := m.store().Save(ctx, card); err != nil {
		slog.Warn("persist transcode recipe card failed", "error", err, "session", opts.SessionID, "playback_session_id", opts.SessionID)
	}
}

// SaveCard persists an already-built recipe card (used by callers that build a
// non-transcode card, e.g. direct/remux). Best-effort.
func (m *TranscodeManager) SaveCard(ctx context.Context, card RecipeCard) {
	if !m.recipeEnabled() || card.SessionID == "" {
		return
	}
	if err := m.store().Save(ctx, card); err != nil {
		slog.Warn("persist recipe card failed", "error", err, "session", card.SessionID, "playback_session_id", card.SessionID)
	}
}

// deleteRecipeCard removes the persisted card on a clean session stop.
func (m *TranscodeManager) deleteRecipeCard(sessionID string) {
	if !m.recipeEnabled() || sessionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.store().Delete(ctx, sessionID); err != nil {
		slog.Warn("delete transcode recipe card failed", "error", err, "session", sessionID, "playback_session_id", sessionID)
	}
}

// recipeRefreshThrottle bounds how often a card's TTL is re-armed so the
// per-segment request path does not hammer the store. One refresh per minute per
// session is enough given a 30-minute TTL.
const recipeRefreshThrottle = time.Minute

// RefreshRecipeCard re-arms a live session's card TTL, throttled in-memory so the
// hot segment path issues at most one store write per minute per session.
func (m *TranscodeManager) RefreshRecipeCard(sessionID string) {
	if !m.recipeEnabled() || sessionID == "" {
		return
	}
	now := time.Now()
	m.recipeRefreshMu.Lock()
	if last, ok := m.recipeRefreshAt[sessionID]; ok && now.Sub(last) < recipeRefreshThrottle {
		m.recipeRefreshMu.Unlock()
		return
	}
	m.recipeRefreshAt[sessionID] = now
	m.recipeRefreshMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.store().Refresh(ctx, sessionID); err != nil {
		slog.Warn("refresh transcode recipe card failed", "error", err, "session", sessionID, "playback_session_id", sessionID)
	}
}

// SessionLoadStatus is the outcome of LoadOrReconstructSession, letting each
// handler render its own error shape (native vs jellycompat) without the manager
// touching the http response.
type SessionLoadStatus int

const (
	// SessionLoaded: a live or reconstructed session is returned, ownership ok.
	SessionLoaded SessionLoadStatus = iota
	// SessionMissing: no live session and no usable card (genuine not-found).
	SessionMissing
	// SessionLoadFailed: the session backend errored (not a clean miss).
	SessionLoadFailed
	// SessionForbidden: a live session exists but belongs to another user.
	SessionForbidden
)

// LoadOrReconstructSession is the single front door every serve handler uses to
// obtain a playback Session: it looks the session up via getSession and, on a
// not-found miss (e.g. after a restart), reconstructs it from the recipe card,
// re-binding ownership to the live caller. The two-factor ownership rule is
// preserved exactly — a live session with a non-zero, mismatched caller is
// refused; reconstruct itself refuses a zero/mismatched caller — so this widens
// no access. getSession is supplied by the caller (its SessionManager.GetSession)
// so the manager needs no direct handle on the manager type.
func (m *TranscodeManager) LoadOrReconstructSession(ctx context.Context, getSession func(string) (*Session, error), sessionID string, requestUserID int) (*Session, SessionLoadStatus) {
	session, err := getSession(sessionID)
	if err != nil {
		if !errors.Is(err, ErrSessionNotFound) {
			return nil, SessionLoadFailed
		}
		// A nil manager (documented optional on StreamHandler) has no card store,
		// so a missing session is simply not-found rather than a panic.
		if m == nil {
			return nil, SessionMissing
		}
		// Lost the in-memory session (e.g. restart): rebuild it from the card.
		// ReconstructSession re-binds ownership and refuses a zero/mismatched
		// caller, so a nil result here is a genuine not-found.
		session = m.ReconstructSession(ctx, sessionID, requestUserID)
		if session == nil {
			return nil, SessionMissing
		}
		return session, SessionLoaded
	}
	// Live session: enforce the existing ownership check. A zero caller is
	// allowed (these routes treat the session UUID as a bearer when auth is
	// optional); a non-zero mismatch is refused.
	if requestUserID != 0 && session.UserID != requestUserID {
		return nil, SessionForbidden
	}
	return session, SessionLoaded
}

// ReconstructSession rebuilds the in-memory playback Session from a persisted
// recipe card after the server lost its state (restart). It re-binds the session
// to the live authenticated caller and refuses if ownership cannot be confirmed.
// Returns the (re)registered session, or nil if reconstruct is not possible (no
// card, ownership mismatch, or unsupported session manager).
func (m *TranscodeManager) ReconstructSession(ctx context.Context, sessionID string, requestUserID int) *Session {
	if m == nil || !m.recipeEnabled() || m.Sessions == nil {
		return nil
	}
	card, found, err := m.store().Get(ctx, sessionID)
	if err != nil {
		slog.Warn("load transcode recipe card failed", "error", err, "session", sessionID, "playback_session_id", sessionID)
		return nil
	}
	if !found {
		return nil
	}
	// Re-bind ownership to the live caller. These routes run under RequireAuth, so
	// a real request carries a non-zero userID. Refuse if it is absent or does not
	// match the card owner — never trust the card's user_id alone.
	if requestUserID == 0 || requestUserID != card.UserID {
		slog.Warn("transcode reconstruct ownership rejected",
			"session", sessionID, "playback_session_id", sessionID,
			"request_user", requestUserID, "card_user", card.UserID)
		return nil
	}

	// An empty PlayMethod is a card written before direct/remux were
	// reconstructable; treat it as a transcode (the only kind then persisted).
	method := card.PlayMethod
	if method == "" {
		method = PlayTranscode
	}

	s := &Session{
		ID:                card.SessionID,
		UserID:            card.UserID,
		ProfileID:         card.ProfileID,
		MediaFileID:       card.MediaFileID,
		PlayMethod:        method,
		BasePlayMethod:    method,
		TranscodeNodeURL:  card.TranscodeNodeURL,
		AudioTrackIndex:   card.AudioTrackIndex,
		TranscodeAudio:    card.TranscodeAudio,
		TargetResolution:  card.TargetResolution,
		TargetVideoCodec:  card.TargetCodecVideo,
		TargetAudioCodec:  card.TargetCodecAudio,
		TargetBitrateKbps: card.TargetBitrateKbps,
		TranscodeHWAccel:  card.HWAccel,
	}
	session := m.Sessions.RegisterReconstructed(s)
	slog.Info("playback session reconstructed from recipe card",
		"session", sessionID, "playback_session_id", sessionID, "user", card.UserID, "method", method)
	return session
}

// ReconstructTranscode rebuilds the in-memory TranscodeSession (and, if
// necessary, the ffmpeg process) for a session whose card survived a restart. It
// is only used for local/integrated transcodes (no transcode node URL).
//
// requestedSegment is the segment number the caller is fetching, or a negative
// value when there is no segment context (manifest path). When the client has
// advanced past the card's original start position, the rebuilt ffmpeg is spawned
// at that position so playback resumes near the requested segment instead of
// restarting from the original seek point and stalling while the segment-recovery
// machinery seeks forward.
//
// Reconstruction is single-flighted per session id: concurrent manifest and
// segment requests for the same lost session share one ffmpeg process rather than
// racing to spawn duplicates against the shared output directory. Spawns are
// additionally bounded by reconstructSem so a post-restart wave of buffered
// clients paces its ffmpeg launches instead of stampeding the host.
//
// NODE AFFINITY CONSTRAINT: this re-spawns ffmpeg on the LOCAL host. The playback
// SessionManager is per-process and not shared across API front-ends, but recipe
// cards are shared (Postgres). For an integrated transcode (empty
// TranscodeNodeURL) the card carries no owning-node identity, so if requests for
// one session are spread across multiple API front-ends WITHOUT sticky session
// affinity, each front-end that misses the in-memory session will reconstruct its
// OWN local ffmpeg — a split-brain with divergent segment dirs. Integrated
// transcode is therefore only safe single-front-end or with session affinity at
// the load balancer. Remote transcode-node sessions are unaffected: their
// non-empty TranscodeNodeURL routes every front-end to the same ffmpeg via the
// proxy path, so ReconstructTranscode is never reached for them.
// Returns the live session, or nil if reconstruct was not possible.
func (m *TranscodeManager) ReconstructTranscode(ctx context.Context, sessionID string, requestedSegment int) *TranscodeSession {
	if m == nil || !m.recipeEnabled() {
		return nil
	}

	// A concurrent reconstruct may already have registered the session; serve it
	// directly so we never enter single-flight only to discard a duplicate.
	if existing := m.GetTranscodeSession(sessionID); existing != nil {
		return existing
	}

	v, err, _ := m.reconstructGroup.Do(sessionID, func() (interface{}, error) {
		return m.doReconstructTranscode(ctx, sessionID, requestedSegment), nil
	})
	if err != nil || v == nil {
		return nil
	}
	session, _ := v.(*TranscodeSession)
	return session
}

// doReconstructTranscode performs the actual rebuild for a single reconstruct
// leader. It is only ever invoked inside reconstructGroup.Do, so it is the sole
// writer racing to register sessionID for this session.
func (m *TranscodeManager) doReconstructTranscode(ctx context.Context, sessionID string, requestedSegment int) *TranscodeSession {
	card, found, err := m.store().Get(ctx, sessionID)
	if err != nil || !found {
		return nil
	}
	// Only transcode cards drive ffmpeg reconstruction. Direct/remux sessions
	// reconstruct without a runtime and must never reach here; guard so a
	// direct/remux card ID cannot accidentally spawn an encode. An empty
	// PlayMethod is a legacy card written before the discriminator (transcode).
	if card.PlayMethod != "" && card.PlayMethod != PlayTranscode {
		return nil
	}

	cfg := m.runtimeConfig()
	outputDir := filepath.Join(cfg.TranscodeDir, sessionID)
	opts := card.TranscodeOpts(outputDir, cfg.FFmpegPath, m.logSink())
	// Re-resolve environment-specific encode knobs from current config so an
	// operator config change applies to reconstructed sessions too.
	opts.HWAccel = cfg.HWAccel
	opts.HWDevice = cfg.HWDevice

	// Resume near the segment the client is actually requesting. The card records
	// the original start; if the client has played past it, spawning ffmpeg at the
	// old position forces a wait-then-seek-restart cycle (a visible stall). Seeking
	// straight to requestedSegment avoids it. A negative requestedSegment (manifest
	// path) carries no segment context, so the card position stands.
	if requestedSegment > card.StartSegmentNumber && card.SegmentDuration > 0 {
		opts.StartSegmentNumber = requestedSegment
		opts.SeekSeconds = float64(requestedSegment * card.SegmentDuration)
	}

	// Pace the spawn so a post-restart wave of reconstructs does not launch a
	// thousand cold-start ffmpeg processes at once. A client that disconnects while
	// waiting releases its place rather than queueing dead work.
	release, ok := m.acquireReconstructSlot(ctx)
	if !ok {
		return nil
	}
	transcodeSession, err := StartTranscode(context.WithoutCancel(ctx), opts)
	release()
	if err != nil {
		slog.Error("reconstruct transcode start failed", "error", err, "session", sessionID, "playback_session_id", sessionID)
		return nil
	}

	// Insert under the map lock, yielding to a winner registered by another path
	// (e.g. a fresh start). Close only the duplicate ffmpeg process here, never the
	// shared output directory the winner is actively serving.
	m.transcodeMu.Lock()
	if existing := m.transcodes[sessionID]; existing != nil {
		m.transcodeMu.Unlock()
		_ = transcodeSession.CloseProcess()
		return existing
	}
	m.transcodes[sessionID] = transcodeSession
	m.transcodeMu.Unlock()

	if m.StartThrottler != nil {
		m.StartThrottler(ctx, transcodeSession)
	}
	m.MonitorLocalTranscodeExit(sessionID, transcodeSession)
	slog.Info("transcode process reconstructed from recipe card",
		"session", sessionID, "playback_session_id", sessionID,
		"requested_segment", requestedSegment, "start_segment_number", opts.StartSegmentNumber)
	return transcodeSession
}

// MonitorLocalTranscodeExit watches a local ffmpeg process and, on an error exit,
// invokes OnFFmpegCrash so the embedding handler tears down the playback session.
// A clean exit (no error) leaves the segments servable until the client stops.
func (m *TranscodeManager) MonitorLocalTranscodeExit(sessionID string, session *TranscodeSession) {
	if m == nil || sessionID == "" || session == nil {
		return
	}

	done := session.Done()
	if done == nil {
		return
	}

	go func() {
		<-done
		time.Sleep(2 * time.Second)

		m.transcodeMu.RLock()
		current := m.transcodes[sessionID]
		m.transcodeMu.RUnlock()
		if current != session {
			return
		}
		if session.IsRunning() {
			return
		}

		// When ffmpeg exits cleanly (no error), the segments are fully written and
		// should remain servable until the client stops the session. This is
		// critical for copy-mode where ffmpeg finishes writing all content much
		// faster than real-time playback. Only tear down the session on error exits.
		if session.WaitError() == nil {
			return
		}

		// ffmpeg crash — system teardown, keep the card so a resume can respawn.
		if m.OnFFmpegCrash != nil {
			m.OnFFmpegCrash(context.Background(), sessionID)
		}
	}()
}

// CloseTranscodeSession stops a transcode session. If transcodeNodeURL is
// non-empty, sends DELETE to the remote transcode node. Otherwise closes the
// local session.
//
// deleteCard controls whether the persisted recipe card is also removed. Pass
// true ONLY for a genuine, user/admin-initiated stop. For liveness-driven
// teardown (idle/paused reap, ffmpeg crash, connection abort, transcode restart
// under the same id) pass false: the in-memory session and ffmpeg are rebuildable,
// and keeping the card lets the client reconstruct on resume. An abandoned card is
// reaped by its own store TTL, so it never leaks a slot.
func (m *TranscodeManager) CloseTranscodeSession(sessionID, transcodeNodeURL string, deleteCard bool) {
	// Clean up local session if one exists (defensive).
	m.transcodeMu.Lock()
	session := m.transcodes[sessionID]
	delete(m.transcodes, sessionID)
	m.transcodeMu.Unlock()
	if session != nil {
		_ = session.Close()
	}

	// Drop the throttle bookkeeping for this session so the map does not grow
	// unbounded across the server's lifetime (one entry per distinct session id).
	m.recipeRefreshMu.Lock()
	delete(m.recipeRefreshAt, sessionID)
	m.recipeRefreshMu.Unlock()

	if deleteCard {
		// Session is truly over — drop its recipe card so it is not reconstructed
		// and so its segment dir becomes eligible for cleanup.
		m.deleteRecipeCard(sessionID)
	}

	// Send DELETE to remote transcode node if assigned (synchronous with timeout).
	if transcodeNodeURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		deleteURL := transcodeNodeURL + "/transcode/" + sessionID
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, deleteURL, nil)
		if err != nil {
			slog.Error("remote transcode delete: build request", "error", err, "session", sessionID, "playback_session_id", sessionID)
			return
		}
		req.Header.Set("Authorization", "Bearer "+m.jwtSecret())

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			slog.Warn("remote transcode delete failed", "error", err, "session", sessionID, "node", transcodeNodeURL, "playback_session_id", sessionID)
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= http.StatusMultipleChoices {
			slog.Warn("remote transcode delete returned non-success status",
				"status", resp.StatusCode, "session", sessionID, "node", transcodeNodeURL, "playback_session_id", sessionID)
		}
	}
}

// CleanupOrphanedTranscodes removes stale per-session temp directories for
// transcodes that are no longer tracked in memory. Dirs whose recipe card still
// exists are spared (those sessions are reconstructable after a restart).
func (m *TranscodeManager) CleanupOrphanedTranscodes() (int, error) {
	m.transcodeMu.RLock()
	active := make(map[string]struct{}, len(m.transcodes))
	for sessionID := range m.transcodes {
		active[sessionID] = struct{}{}
	}
	m.transcodeMu.RUnlock()

	// Spare dirs whose recipe card still exists: those sessions are
	// reconstructable after a restart, so their segments must not be wiped.
	if m.recipeEnabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Reclaim physically-expired rows first (filter-on-read already hides them;
		// this just bounds table growth). Best-effort.
		if n, err := m.store().DeleteExpired(ctx); err != nil {
			slog.Warn("recipe card expiry sweep failed", "error", err)
		} else if n > 0 {
			slog.Info("recipe card expiry sweep removed rows", "count", n)
		}
		if cardIDs, err := m.store().ActiveSessionIDs(ctx); err != nil {
			// Fail safe: if we cannot enumerate cards, skip the wipe rather than
			// risk deleting a live session's segments.
			slog.Warn("recipe card enumeration failed; skipping orphan cleanup", "error", err)
			return 0, nil
		} else {
			for id := range cardIDs {
				active[id] = struct{}{}
			}
		}
	}

	return CleanupOrphanedTranscodeDirs(m.runtimeConfig().TranscodeDir, active)
}
