package playback

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
// RegisterReconstructedWithLimits additionally enforces the per-user admission
// caps so replaying a token cannot reconstruct past the concurrent stream /
// transcode limits a fresh StartSession would reject.
type sessionReconstructor interface {
	RegisterReconstructed(s *Session) *Session
	RegisterReconstructedWithLimits(ctx context.Context, s *Session) (*Session, error)
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
	// the card, so a resume can respawn). dead is the exact session that crashed;
	// the handler passes it back through CloseTranscodeSessionIf so a successor
	// reconstructed under the same id between the exit and teardown is not killed.
	// No-op when nil.
	OnFFmpegCrash func(ctx context.Context, sessionID string, dead *TranscodeSession)
	// StartThrottler optionally starts the segment throttler for a (re)started
	// transcode, reading the embedding handler's settings. No-op when nil.
	StartThrottler func(ctx context.Context, ts *TranscodeSession)

	transcodeMu sync.RWMutex
	transcodes  map[string]*TranscodeSession

	// inFlightMu guards reconstructInFlight, the set of session ids whose ffmpeg
	// is mid-reconstruct. Cleanup unions it with the live map so a dir being
	// rebuilt right now is never reaped (token-carried reconstruction has no
	// durable card index to consult instead).
	inFlightMu          sync.Mutex
	reconstructInFlight map[string]struct{}

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
		transcodes:          make(map[string]*TranscodeSession),
		reconstructInFlight: make(map[string]struct{}),
	}
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

// markReconstructing records that sessionID's ffmpeg is mid-reconstruct and
// returns a release func to clear it. Cleanup unions this set with the live map
// so a dir being rebuilt is never reaped before it registers.
func (m *TranscodeManager) markReconstructing(sessionID string) func() {
	if m == nil || sessionID == "" {
		return func() {}
	}
	m.inFlightMu.Lock()
	if m.reconstructInFlight == nil {
		m.reconstructInFlight = make(map[string]struct{})
	}
	m.reconstructInFlight[sessionID] = struct{}{}
	m.inFlightMu.Unlock()
	return func() {
		m.inFlightMu.Lock()
		delete(m.reconstructInFlight, sessionID)
		m.inFlightMu.Unlock()
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
//
// card is the reconstruction recipe the caller decoded from the verified stream
// token the client presented (nil when the request carried no usable token).
// Under token-carried reconstruction it is the sole descriptor source — there is
// no shared per-session store to fall back on — so a not-found session with a nil
// card is a genuine miss.
func (m *TranscodeManager) LoadOrReconstructSession(ctx context.Context, getSession func(string) (*Session, error), sessionID string, requestUserID int, card *RecipeCard) (*Session, SessionLoadStatus) {
	session, err := getSession(sessionID)
	if err != nil {
		if !errors.Is(err, ErrSessionNotFound) {
			return nil, SessionLoadFailed
		}
		// A nil manager (documented optional on StreamHandler) cannot reconstruct,
		// so a missing session is simply not-found rather than a panic.
		if m == nil || card == nil {
			return nil, SessionMissing
		}
		// Lost the in-memory session (e.g. restart): rebuild it from the token's
		// recipe. ReconstructSession re-binds the session to the card owner and
		// refuses a non-zero caller that mismatches it (a zero caller is allowed for
		// the authless bearer routes), so a nil result here is a genuine not-found.
		session = m.ReconstructSession(ctx, sessionID, requestUserID, *card)
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
func (m *TranscodeManager) ReconstructSession(ctx context.Context, sessionID string, requestUserID int, card RecipeCard) *Session {
	if m == nil || m.Sessions == nil {
		return nil
	}
	if card.SessionID == "" || card.SessionID != sessionID {
		// The token's recipe must be for the session id in the URL; a mismatch is
		// a forged or stale request.
		return nil
	}
	// Re-bind ownership to the card owner. A zero caller is allowed (the authless
	// transcode delivery routes — HLS master.m3u8 / segment — treat the session
	// UUID as the bearer credential when auth is optional); a non-zero caller that
	// mismatches the card owner is refused. Either way the reconstructed session is
	// bound to card.UserID, never to the request's user.
	if requestUserID != 0 && requestUserID != card.UserID {
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
	// Enforce the same per-user concurrency caps a fresh StartSession would, so a
	// replayed token cannot reconstruct past the user's limit. Reconstructing the
	// user's own surviving sessions still succeeds up to the cap; only the over-cap
	// replay is rejected.
	session, err := m.Sessions.RegisterReconstructedWithLimits(ctx, s)
	if err != nil {
		slog.Warn("playback session reconstruct refused by admission cap",
			"session", sessionID, "playback_session_id", sessionID,
			"user", card.UserID, "method", method, "error", err)
		return nil
	}
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
//
// This constraint is currently documented, not enforced: a robust fix needs a
// per-session owning-instance claim in a store shared across front-ends (e.g.
// a shared Redis or the recipe store), so a front-end refuses to
// reconstruct an integrated session it does not own. The TranscodeManager has no
// such shared handle wired today — only per-process config/secret closures and
// in-memory maps — so the claim cannot be made cheaply here. Until a topology
// signal reaches the manager, deploy integrated transcode single-front-end or
// behind sticky session affinity. See M8.
// card is the reconstruction recipe decoded from the client's verified stream
// token; it carries the encode parameters formerly read from the Postgres store.
// Returns the live session, or nil if reconstruct was not possible.
func (m *TranscodeManager) ReconstructTranscode(ctx context.Context, sessionID string, requestedSegment int, card RecipeCard) *TranscodeSession {
	if m == nil {
		return nil
	}
	if card.SessionID == "" || card.SessionID != sessionID {
		return nil
	}

	// A concurrent reconstruct may already have registered the session; serve it
	// directly so we never enter single-flight only to discard a duplicate.
	if existing := m.GetTranscodeSession(sessionID); existing != nil {
		return existing
	}

	v, err, _ := m.reconstructGroup.Do(sessionID, func() (interface{}, error) {
		return m.doReconstructTranscode(ctx, sessionID, requestedSegment, card), nil
	})
	if err != nil || v == nil {
		return nil
	}
	session, _ := v.(*TranscodeSession)
	return session
}

// fastResumeSeek decides whether a reconstructed ffmpeg should be spawned at the
// segment the client is actually requesting instead of the card's original
// start. Resuming near requestedSegment avoids a wait-then-seek-restart stall
// when the client has already played past the card position.
//
// The returned (segment, seekSeconds) maps via seg×SegmentDuration, which is
// ONLY valid for ENCODED transcodes: their forced keyframes make every segment
// exactly SegmentDuration long. COPY-mode segments inherit the source's variable
// GOP boundaries, so seg×dur lands on the wrong source time and desyncs A/V after
// a restart — so for copy-mode cards this returns ok=false and the caller keeps
// the card's original start, letting the manifest-driven segment recovery
// (RestartSeekTarget) seek forward once the rebuilt manifest exposes the real
// per-segment timing. A negative requestedSegment (manifest path, no segment
// context) and a non-advanced client also return ok=false.
func fastResumeSeek(card RecipeCard, requestedSegment int) (segment int, seekSeconds float64, ok bool) {
	if strings.EqualFold(card.TargetCodecVideo, "copy") {
		return 0, 0, false
	}
	if requestedSegment > card.StartSegmentNumber && card.SegmentDuration > 0 {
		return requestedSegment, float64(requestedSegment * card.SegmentDuration), true
	}
	return 0, 0, false
}

// doReconstructTranscode performs the actual rebuild for a single reconstruct
// leader. It is only ever invoked inside reconstructGroup.Do, so it is the sole
// writer racing to register sessionID for this session.
func (m *TranscodeManager) doReconstructTranscode(ctx context.Context, sessionID string, requestedSegment int, card RecipeCard) *TranscodeSession {
	// Only transcode cards drive ffmpeg reconstruction. Direct/remux sessions
	// reconstruct without a runtime and must never reach here; guard so a
	// direct/remux card ID cannot accidentally spawn an encode. An empty
	// PlayMethod is back-compat for a token minted before the discriminator
	// (transcode).
	if card.PlayMethod != "" && card.PlayMethod != PlayTranscode {
		return nil
	}

	// Mark in-flight for the whole rebuild so a concurrent cleanup never reaps the
	// output dir between spawn and map registration.
	release := m.markReconstructing(sessionID)
	defer release()

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
	//
	if seg, seek, ok := fastResumeSeek(card, requestedSegment); ok {
		opts.StartSegmentNumber = seg
		opts.SeekSeconds = seek
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

		// ffmpeg crash — tear the session down; a client holding a valid token can
		// reconstruct it on the next request. Pass the dead session so teardown is a
		// compare-and-delete: a reconstruct that registered a successor under this id
		// between the current!=session check above and teardown must not be killed.
		if m.OnFFmpegCrash != nil {
			m.OnFFmpegCrash(context.Background(), sessionID, session)
		}
	}()
}

// CloseTranscodeSession stops a transcode session. If transcodeNodeURL is
// non-empty, sends DELETE to the remote transcode node. Otherwise closes the
// local session.
//
// Under token-carried reconstruction there is no durable card to drop: a stopped
// session simply stops being served, and its segment dir is reaped by the
// in-memory-liveness + age cleanup once no live token could still reconstruct it
// (see CleanupOrphanedTranscodes). A deliberate, sub-TTL kill of an abusive
// stream additionally writes a deny lease (see the central revalidator) so the
// node withholds bytes before the token expires.
func (m *TranscodeManager) CloseTranscodeSession(sessionID, transcodeNodeURL string) {
	// Clean up local session if one exists (defensive).
	m.transcodeMu.Lock()
	session := m.transcodes[sessionID]
	delete(m.transcodes, sessionID)
	m.transcodeMu.Unlock()
	if session != nil {
		_ = session.Close()
	}

	m.deleteRemoteTranscode(sessionID, transcodeNodeURL)
}

// CloseTranscodeSessionIf tears down a transcode session only when the live map
// still holds the exact session the caller observed dying (expected). This is
// the crash path: between a local ffmpeg's error exit and this teardown, a
// concurrent reconstruct can register a fresh successor under the same id. An
// unconditional close would delete+Close() that live successor — and Close()
// removes the shared output dir out from under it. Comparing under the same lock
// that reconstruct registers through makes the swap atomic: a non-matching entry
// is left untouched. The remote-DELETE still fires for the matched case (and is
// skipped entirely when the local successor already won, since there is nothing
// of ours to stop).
//
// Returns true iff the live entry still matched expected and was torn down;
// false iff a different (successor) or nil session held the slot and was left
// untouched. Callers MUST treat this return as the authoritative gate for any
// further teardown (e.g. stopping the upstream playback session): when it is
// false, a successor owns the id and must not be disturbed.
func (m *TranscodeManager) CloseTranscodeSessionIf(sessionID string, expected *TranscodeSession, transcodeNodeURL string) bool {
	m.transcodeMu.Lock()
	current := m.transcodes[sessionID]
	if current != expected {
		// A successor (or an already-completed close) holds the slot; leave it.
		m.transcodeMu.Unlock()
		return false
	}
	delete(m.transcodes, sessionID)
	m.transcodeMu.Unlock()
	if expected != nil {
		_ = expected.Close()
	}

	m.deleteRemoteTranscode(sessionID, transcodeNodeURL)
	return true
}

// deleteRemoteTranscode sends DELETE to the assigned transcode node if any
// (synchronous with timeout). A no-op for local/integrated sessions.
func (m *TranscodeManager) deleteRemoteTranscode(sessionID, transcodeNodeURL string) {
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
// transcodes that are no longer reconstructable. Under token-carried
// reconstruction there is no durable card index to consult, so the liveness
// signal is: the in-process live transcode map, the set of sessions currently
// mid-reconstruct, and directory age. A dir is reaped only when it is absent from
// both sets AND older than the maximum token lifetime — past which no surviving
// token could reconstruct it. Each process owns its own TranscodeDir, so there is
// no cross-process enumeration-failure mode to fail safe against.
func (m *TranscodeManager) CleanupOrphanedTranscodes() (int, error) {
	// Snapshot the live map and the in-flight set under both locks held at once.
	// A reconstruct registers into m.transcodes and clears m.reconstructInFlight
	// at different moments; snapshotting the two sets separately could miss a
	// session that migrated between them, leaving its live dir absent from active
	// and exposed to reaping. inFlightMu is taken first to match the only other
	// site that holds both (none nests the reverse order).
	m.inFlightMu.Lock()
	m.transcodeMu.RLock()
	active := make(map[string]struct{}, len(m.transcodes)+len(m.reconstructInFlight))
	for sessionID := range m.transcodes {
		active[sessionID] = struct{}{}
	}
	// Spare sessions mid-reconstruct: their dir is being written right now but is
	// not yet registered in the live map.
	for sessionID := range m.reconstructInFlight {
		active[sessionID] = struct{}{}
	}
	m.transcodeMu.RUnlock()
	m.inFlightMu.Unlock()

	return CleanupOrphanedTranscodeDirs(m.runtimeConfig().TranscodeDir, active, MaxTokenTTL)
}
