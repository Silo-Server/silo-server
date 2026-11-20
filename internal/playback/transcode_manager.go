package playback

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/telemetry"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// TranscodeRuntimeConfig is the subset of playback configuration the transcode
// manager needs to (re)start ffmpeg. It is a small, config-package-free struct so
// internal/playback does not import internal/config (avoiding an import cycle);
// each embedding handler adapts its own config snapshot into this shape.
type TranscodeRuntimeConfig struct {
	TranscodeDir            string
	FFmpegPath              string
	HWAccel                 string
	HWDevice                string
	SegmentRetentionSeconds int
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

type atomicSessionReconstructor interface {
	RollbackReconstructedToneMap(expected *Session) bool
	CaptureReconstructedExecution(sessionID string) (*Session, uint64)
	ConfirmReconstructedExecution(expected *Session, revision uint64, mode tonemap.Mode, encoderHWAccel string) *Session
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
	// JWTSecretFn returns the shared secret that signs remote transcode-node
	// DELETEs.
	JWTSecretFn func() string
	// OnFFmpegCrash is invoked when a reconstructed/local ffmpeg exits with an
	// error so the embedding handler can tear down the playback session (keeping
	// the card, so a resume can respawn). dead is the exact session that crashed;
	// the handler passes it back through CloseTranscodeSessionIf so a successor
	// reconstructed under the same id between the exit and teardown is not killed.
	// No-op when nil.
	OnFFmpegCrash func(ctx context.Context, sessionID string, dead *TranscodeSession)
	// OnDemuxFailure is forwarded into every local transcode this manager starts
	// or reconstructs so repeated input demux failures can stamp the source
	// candidate known-bad. No-op when nil.
	OnDemuxFailure func(ctx context.Context, mediaFileID int, canonicalPath string) error
	// OnSourceRejected is forwarded alongside OnDemuxFailure so a decoder that
	// rejects the source stamps the same candidate known-bad. It shares the
	// callback shape and the one failed_at mechanism. No-op when nil.
	OnSourceRejected func(ctx context.Context, mediaFileID int, canonicalPath string) error
	// StartThrottler optionally starts the segment throttler for a (re)started
	// transcode, reading the embedding handler's settings. No-op when nil.
	StartThrottler func(ctx context.Context, ts *TranscodeSession)
	// ResolveInput turns a durable canonical source into the concrete,
	// short-lived input used by FFmpeg during reconstruction. The returned
	// cleanup function lives with the transcode session. This keeps provider
	// credentials out of recipe cards while still allowing restart/resume.
	ResolveInput func(ctx context.Context, mediaFileID int, ownerInstallationID int, userID int, profileID string, canonicalPath string) (resolvedPath string, cleanup func(), err error)

	// Package-private execution seams keep reconstruction lifecycle tests
	// deterministic without invoking a host FFmpeg binary. Production always
	// falls through to ResolveToneMapExecutor and StartTranscode.
	resolveToneMapExecutor func(context.Context, TranscodeOpts) (TranscodeOpts, error)
	startTranscode         func(context.Context, TranscodeOpts) (*TranscodeSession, error)
	autoTranscodePipeline  func(context.Context, TranscodeOpts) *AutoTranscodePipeline

	transcodeMu sync.RWMutex
	transcodes  map[string]*TranscodeSession
	// retired holds displaced generations kept servable for a bounded overlap
	// after a same-session replan publishes a successor. Guarded by transcodeMu.
	retired map[string]*RetainedGeneration
	// shuttingDown is set under transcodeMu before the shutdown drain takes the
	// live map. Every publication path checks it under the same lock so a late
	// FFmpeg process cannot escape the drain and leave its cache behind.
	shuttingDown bool

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
	// rehomeGroup single-flights moving a session's transcode off an executor
	// that stopped answering, so every request that finds the node dead waits
	// on one move instead of each starting its own replacement.
	rehomeGroup singleflight.Group

	// lifecycleMu guards lifecycleLocks, the per-session mutexes that serialize
	// every path which spawns ffmpeg into a session's output directory (fresh
	// start, quality/audio restart, and reconstruct). reconstructGroup only
	// single-flights reconstructs against each other; without this a reconstruct
	// racing a fresh start could run two ffmpeg writers against the same dir.
	lifecycleMu    sync.Mutex
	lifecycleLocks map[string]*lifecycleLock

	// asyncRestartMu guards asyncRestarts, the per-session coalesced far-seek
	// restart workers. Each entry records the latest requested segment so two
	// far-seeks landing on the same session converge on one restart at the
	// newest target instead of racing to be the last writer.
	asyncRestartMu sync.Mutex
	asyncRestarts  map[string]*pendingAsyncRestart

	// asyncRestartFinishHook, when non-nil, runs once the coalescing worker has
	// atomically decided to leave its loop but before it returns. A test uses it
	// to inject a request in the completion window, where a late target must
	// start a fresh worker rather than update an entry that is being removed.
	// Production leaves it nil.
	asyncRestartFinishHook func()
}

// lifecycleLock is a refcounted per-session lock. The refcount lets the manager
// drop the map entry once no path holds or waits on it, so the map does not grow
// unbounded across the lifetime of a long-running server.
//
// The lock is a one-slot channel rather than a sync.Mutex so a waiter can give
// up on a deadline: a stop finalization fenced by a lease must not block past it
// behind an unrelated holder and then run teardown after another replica has
// taken the work over. newLifecycleLock seeds the single token.
type lifecycleLock struct {
	token chan struct{}
	refs  int
}

func newLifecycleLock() *lifecycleLock {
	lk := &lifecycleLock{token: make(chan struct{}, 1)}
	lk.token <- struct{}{}
	return lk
}

// NewTranscodeManager returns a manager with its internal maps initialized. The
// caller wires the dependency function fields before use.
func NewTranscodeManager() *TranscodeManager {
	return &TranscodeManager{
		transcodes:          make(map[string]*TranscodeSession),
		retired:             make(map[string]*RetainedGeneration),
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

// WithReconstructSlot runs fn while holding one of the slots that pace
// transcode reconstruction. Work that starts an FFmpeg process on another node
// on a session's behalf takes the same slot as a local reconstruct, so a dead
// node with many sessions is replaced at the same pace as a restart wave. It
// returns the context error when ctx ends before a slot frees up.
func (m *TranscodeManager) WithReconstructSlot(ctx context.Context, fn func() error) error {
	release, ok := m.acquireReconstructSlot(ctx)
	if !ok {
		return ctx.Err()
	}
	defer release()
	return fn()
}

// TranscodeRehomeTimeout bounds one move of a transcode off an unreachable
// executor, including the wait for a reconstruct slot and the new executor's
// first manifest. The move is detached from the request that triggered it,
// because every concurrent request for the session waits on the same move.
const TranscodeRehomeTimeout = 2 * time.Minute

// RehomeTranscode runs move once for all concurrent callers of the same
// session. move receives a context detached from any single request and bounded
// by TranscodeRehomeTimeout. A caller whose own context ends stops waiting
// without canceling the move the others are waiting on.
func (m *TranscodeManager) RehomeTranscode(ctx context.Context, sessionID string, move func(context.Context) error) error {
	if m == nil || sessionID == "" {
		return errors.New("transcode rehome unavailable")
	}
	detached := context.WithoutCancel(ctx)
	results := m.rehomeGroup.DoChan(sessionID, func() (interface{}, error) {
		moveCtx, cancel := context.WithTimeout(detached, TranscodeRehomeTimeout)
		defer cancel()
		return nil, move(moveCtx)
	})
	select {
	case result := <-results:
		return result.Err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ReconstructResumePoint reports where a rebuilt transcode should start for a
// client fetching requestedSegment. Every reconstruction uses this rule — after
// a restart, on a restarted node, or on a new executor — so segment numbers and
// timestamps continue the same way on each.
func ReconstructResumePoint(card RecipeCard, requestedSegment int) (segment int, seekSeconds float64, ok bool) {
	return fastResumeSeek(card, requestedSegment)
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
// live map. It returns false and closes the session when shutdown has begun.
func (m *TranscodeManager) RegisterTranscodeSession(sessionID string, ts *TranscodeSession) bool {
	m.transcodeMu.Lock()
	if m.shuttingDown {
		m.transcodeMu.Unlock()
		if ts != nil {
			_ = ts.Close()
		}
		return false
	}
	m.transcodes[sessionID] = ts
	m.transcodeMu.Unlock()
	return true
}

// SwapTranscodeSession atomically publishes a prepared successor and returns
// the predecessor without closing it. The boolean is false when shutdown has
// begun; in that case the successor is closed. Protocol-v3 replans use plan-scoped
// output directories, so the caller can commit state first, publish the new
// process, and only then reap the old process without either process writing to
// the other's directory.
func (m *TranscodeManager) SwapTranscodeSession(sessionID string, successor *TranscodeSession) (*TranscodeSession, bool) {
	m.transcodeMu.Lock()
	if m.shuttingDown {
		m.transcodeMu.Unlock()
		if successor != nil {
			_ = successor.Close()
		}
		return nil, false
	}
	predecessor := m.transcodes[sessionID]
	m.transcodes[sessionID] = successor
	m.transcodeMu.Unlock()
	return predecessor, true
}

// SwapTranscodeSessionIf publishes successor only while the live map still
// contains expected. Callers use it after staging a process in a distinct
// output directory so a stale replacement cannot overwrite a newer process.
func (m *TranscodeManager) SwapTranscodeSessionIf(
	sessionID string,
	expected *TranscodeSession,
	successor *TranscodeSession,
) bool {
	m.transcodeMu.Lock()
	if m.shuttingDown {
		m.transcodeMu.Unlock()
		if successor != nil {
			_ = successor.Close()
		}
		return false
	}
	defer m.transcodeMu.Unlock()
	if m.transcodes[sessionID] != expected {
		return false
	}
	m.transcodes[sessionID] = successor
	return true
}

// StopRemoteTranscode removes only the remote node process. It deliberately
// leaves the local live-map entry untouched for an atomic remote-to-local v3
// replacement.
func (m *TranscodeManager) StopRemoteTranscode(sessionID, transcodeNodeURL string) {
	if err := m.deleteRemoteTranscode(context.Background(), sessionID, transcodeNodeURL, true); err != nil {
		slog.Warn("remote transcode delete failed", "error", err, "session", sessionID, "node", transcodeNodeURL, "playback_session_id", sessionID)
	}
}

// StopRemoteTranscodeAfterFailure stops a remote job after a call to its node
// failed. When the node could not be reached, the stop runs in the background:
// waiting on an unreachable host would hold up the next candidate, and a node
// that answers again by then still drops a job it may have started.
func (m *TranscodeManager) StopRemoteTranscodeAfterFailure(sessionID, transcodeNodeURL string, err error) {
	if nodepool.IsConnectFailure(err) {
		go m.StopRemoteTranscode(sessionID, transcodeNodeURL)
		return
	}
	m.StopRemoteTranscode(sessionID, transcodeNodeURL)
}

// CancelRemoteTranscode synchronously confirms that a remote process no longer
// exists. User-visible stop paths use this before removing the playback session
// so a node partition leaves a live session the caller can retry stopping.
func (m *TranscodeManager) CancelRemoteTranscode(ctx context.Context, sessionID, transcodeNodeURL string) error {
	return m.deleteRemoteTranscode(ctx, sessionID, transcodeNodeURL, true)
}

// TranscodeStopFenceQueryParam set to "0" on a node stop asks the node not to
// fence the transport against reconstruction.
// TranscodeStopStartIDQueryParam on a release names the start it releases;
// a job another start published under the ID is left running.
const TranscodeStopStartIDQueryParam = "start_id"

const TranscodeStopFenceQueryParam = "fence"

// ReleaseRemoteTranscode stops a remote job whose transport id lives on: the
// API is moving it between nodes. The node does not fence the id, so a replica
// or a stream token whose route still names that node can rebuild it there,
// and a release that lands late cannot lock out a successor started under the
// same id.
func (m *TranscodeManager) ReleaseRemoteTranscode(ctx context.Context, sessionID, transcodeNodeURL string) error {
	return m.deleteRemoteTranscode(ctx, sessionID, transcodeNodeURL, false)
}

// LockSessionLifecycle acquires the per-session lifecycle mutex and returns a
// release func. Every path that spawns ffmpeg into a session's output directory
// (fresh start, restart, reconstruct) must hold it across "check existing → spawn
// → register" so two paths never run concurrent writers against the same dir. The
// lock is refcounted: the map entry is dropped once the last holder/waiter
// releases, so the map stays bounded.
func (m *TranscodeManager) LockSessionLifecycle(sessionID string) func() {
	unlock, _ := m.lockSessionLifecycle(context.Background(), sessionID)
	return unlock
}

// LockSessionLifecycleContext is LockSessionLifecycle bounded by ctx: it returns
// ctx.Err() instead of blocking once the context ends. Teardown paths that must
// finish inside a lease use it so a holder they cannot outlast makes them give
// up rather than start work after the deadline.
func (m *TranscodeManager) LockSessionLifecycleContext(ctx context.Context, sessionID string) (func(), error) {
	return m.lockSessionLifecycle(ctx, sessionID)
}

func (m *TranscodeManager) lockSessionLifecycle(ctx context.Context, sessionID string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.lifecycleMu.Lock()
	if m.lifecycleLocks == nil {
		m.lifecycleLocks = make(map[string]*lifecycleLock)
	}
	lk := m.lifecycleLocks[sessionID]
	if lk == nil {
		lk = newLifecycleLock()
		m.lifecycleLocks[sessionID] = lk
	}
	lk.refs++
	m.lifecycleMu.Unlock()

	select {
	case <-lk.token:
	case <-ctx.Done():
		// The ref taken above is released, but the lock is never held: a waiter
		// that gave up does not own the critical section.
		m.releaseLifecycleLock(sessionID, lk)
		return nil, ctx.Err()
	}
	return func() {
		lk.token <- struct{}{}
		m.releaseLifecycleLock(sessionID, lk)
	}, nil
}

func (m *TranscodeManager) releaseLifecycleLock(sessionID string, lk *lifecycleLock) {
	m.lifecycleMu.Lock()
	lk.refs--
	if lk.refs == 0 {
		delete(m.lifecycleLocks, sessionID)
	}
	m.lifecycleMu.Unlock()
}

// RestartSessionLocked re-spawns ts under the per-session lifecycle lock so a
// restart (audio-switch or segment-recovery) can never race a fresh start,
// reconstruct, or another restart into the same output directory — the
// concurrent-writer corruption the lifecycle lock exists to prevent. It holds
// the lock only across the cancel→respawn transition inside Restart and
// releases it before the caller waits on segments. Under the lock it confirms
// ts is still the live mapped session; if a concurrent teardown or reconstruct
// replaced it, the stale handle is not re-spawned and ErrSessionSuperseded is
// returned.
func (m *TranscodeManager) RestartSessionLocked(ctx context.Context, sessionID string, ts *TranscodeSession, seekSeconds float64, startSegment int) error {
	return m.restartSessionLocked(sessionID, ts, func() error {
		return ts.Restart(ctx, seekSeconds, startSegment)
	})
}

// RestartSegmentLocked resolves and restarts one missing HLS segment under the
// per-session lifecycle lock. Copy-video resolution reads the current manifest,
// so keeping resolution and restart inside the same lock preserves the mapping
// it observed.
func (m *TranscodeManager) RestartSegmentLocked(
	ctx context.Context,
	sessionID string,
	ts *TranscodeSession,
	segNum int,
) (SegmentRecoveryTarget, bool, error) {
	return m.restartSegmentLockedWithTarget(ctx, sessionID, ts, func() int { return segNum })
}

// restartSegmentLockedWithTarget is RestartSegmentLocked with the segment read
// inside the lifecycle critical section. The coalescing worker uses it so two
// queued far-seeks collapse onto the target that is current when the lock is
// actually held, instead of the one that was current when the goroutine started.
func (m *TranscodeManager) restartSegmentLockedWithTarget(
	ctx context.Context,
	sessionID string,
	ts *TranscodeSession,
	segNum func() int,
) (SegmentRecoveryTarget, bool, error) {
	var target SegmentRecoveryTarget
	var ok bool
	err := m.restartSessionLocked(sessionID, ts, func() error {
		var restartErr error
		target, ok, restartErr = ts.RestartSegment(ctx, segNum())
		return restartErr
	})
	if err != nil {
		ok = false
	}
	return target, ok, err
}

// restartSegmentAsyncTimeout bounds one detached seek restart so a wedged
// resolver cannot leak the goroutine forever.
const restartSegmentAsyncTimeout = 2 * time.Minute

// pendingAsyncRestart coalesces the detached far-seek restarts for one session.
// A request that arrives while a restart runs records the newest segment here
// instead of starting a competing worker, so the running worker re-reads the
// target after each restart and converges on the latest request. Without it two
// far-seeks serialized by the lifecycle lock would apply in whatever order they
// happened to acquire the lock, and the older target could win.
type pendingAsyncRestart struct {
	// mu guards ts, segNum and adoptions.
	mu sync.Mutex
	// ts is the generation the worker restarts. It starts as the generation the
	// request named and is adopted forward to a live successor when a replan
	// replaces that generation while the restart waits for the lifecycle lock.
	ts *TranscodeSession
	// segNum is the latest requested segment.
	segNum int
	// adoptions counts the successors this worker has followed, so a replan
	// storm cannot keep one detached seek restart alive indefinitely.
	adoptions int
}

// maxAsyncRestartSuccessors bounds how many replaced generations one detached
// far-seek restart follows before it gives up. One replan superseding the seek
// is the shape it is for; a longer chain means the session is churning and the
// client's next playlist reload should decide the position.
const maxAsyncRestartSuccessors = 2

func (p *pendingAsyncRestart) session() *TranscodeSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ts
}

// RestartSegmentAsync runs RestartSegmentLocked on its own goroutine so a
// far-seek restart is not serialized behind the request that triggered it. The
// generation rotates on restart, so the triggering request's token no longer
// names the bytes the restart produces; the serve handler waits for the
// restarted generation's segment and serves it through the absolute-segment
// continuity rule (see HandleGetTranscodeSegment). The context is detached from
// the request so a client that disconnects does not cancel a restart another
// request may still depend on.
//
// Concurrent far-seeks to different positions on one session coalesce onto a
// single worker that ends at the latest requested segment, so a burst of seeks
// cannot leave ffmpeg parked at an older position than the player's last one.
func (m *TranscodeManager) RestartSegmentAsync(sessionID string, ts *TranscodeSession, segNum int) {
	if m == nil || ts == nil {
		return
	}
	m.asyncRestartMu.Lock()
	if m.asyncRestarts == nil {
		m.asyncRestarts = make(map[string]*pendingAsyncRestart)
	}
	if pending := m.asyncRestarts[sessionID]; pending != nil {
		pending.mu.Lock()
		sameGeneration := pending.ts == ts
		if sameGeneration {
			// A restart for this generation is already running: record the
			// newest target and let the worker converge on it.
			pending.segNum = segNum
		}
		pending.mu.Unlock()
		if sameGeneration {
			m.asyncRestartMu.Unlock()
			return
		}
	}
	pending := &pendingAsyncRestart{ts: ts, segNum: segNum}
	m.asyncRestarts[sessionID] = pending
	m.asyncRestartMu.Unlock()

	go m.runAsyncRestart(sessionID, pending)
}

// runAsyncRestart applies far-seek restarts until the coalesced target stops
// moving. A newer target arriving mid-restart makes the loop run once more, so
// the final applied position is the last one the player asked for.
//
// The decision to leave the loop and the removal of the coalescing entry happen
// in one critical section (see tryFinishAsyncRestart). If they did not, a
// request landing after the "did the target move?" read but before the removal
// would find the entry still present, record its target, and return without a
// worker — and the exiting worker would then delete the entry, dropping the
// newest target. Under one lock the request either lands before the read (the
// worker loops onto it) or after the removal (it starts a fresh worker).
func (m *TranscodeManager) runAsyncRestart(sessionID string, pending *pendingAsyncRestart) {
	defer func() {
		m.asyncRestartMu.Lock()
		if m.asyncRestarts[sessionID] == pending {
			delete(m.asyncRestarts, sessionID)
		}
		m.asyncRestartMu.Unlock()
	}()
	for {
		// Read the target inside the lifecycle critical section: a request that
		// queued behind another worker's restart is collapsed onto whatever the
		// latest requested segment is when the lock is actually held.
		applied := 0
		nextTarget := func() int {
			pending.mu.Lock()
			defer pending.mu.Unlock()
			applied = pending.segNum
			return applied
		}

		generation := pending.session()
		ctx, cancel := context.WithTimeout(context.Background(), restartSegmentAsyncTimeout)
		target, ok, err := m.restartSegmentLockedWithTarget(ctx, sessionID, generation, nextTarget)
		cancel()
		if errors.Is(err, ErrSessionSuperseded) && m.followAsyncRestartSuccessor(sessionID, pending, generation) {
			// The seek outlived the generation it was queued on: a replan
			// replaced that generation while this restart waited for the
			// lifecycle lock. The successor is a fresh generation at the old
			// position, so dropping the restart here would abandon the
			// player's newer seek and leave every later request on the
			// successor anchored where the replan started. Adopt it and apply
			// the same target.
			continue
		}
		m.recordAsyncRestartOutcome(sessionID, generation, applied, target, ok, err)

		// A terminal failure cannot be fixed by a different target on the same
		// dead generation, so stop rather than loop; the latched verdict is what
		// the next request answers.
		if err != nil {
			return
		}
		if m.tryFinishAsyncRestart(sessionID, pending, applied) {
			if m.asyncRestartFinishHook != nil {
				m.asyncRestartFinishHook()
			}
			return
		}
	}
}

// tryFinishAsyncRestart atomically ends the coalescing worker when no newer
// target arrived while it applied applied. It returns true when the worker
// should leave the loop, having removed its entry under asyncRestartMu so a
// request landing immediately after this check starts a fresh worker instead of
// updating an entry about to be deleted. It returns false when the coalesced
// target moved, so the caller loops and applies it.
func (m *TranscodeManager) tryFinishAsyncRestart(sessionID string, pending *pendingAsyncRestart, applied int) bool {
	m.asyncRestartMu.Lock()
	defer m.asyncRestartMu.Unlock()
	pending.mu.Lock()
	advanced := pending.segNum != applied
	pending.mu.Unlock()
	if advanced {
		return false
	}
	if m.asyncRestarts[sessionID] == pending {
		delete(m.asyncRestarts, sessionID)
	}
	return true
}

func (m *TranscodeManager) recordAsyncRestartOutcome(sessionID string, ts *TranscodeSession, segNum int, target SegmentRecoveryTarget, ok bool, err error) {
	switch {
	case errors.Is(err, ErrSessionSuperseded):
		// Routine teardown: the session was closed while the seek restart
		// waited for the lifecycle lock, so nothing was re-spawned. A replaced
		// generation is followed instead (see followAsyncRestartSuccessor).
		slog.Debug("restart transcode at missing segment skipped, session superseded (async)",
			"component", "playback", "segment", segNum, "session", sessionID)
	case err != nil && !errors.Is(err, ErrManifestNotReady):
		slog.Warn("restart transcode at missing segment (async)", "component", "playback", "error", err, "segment", segNum, "session", sessionID)
		// Latch the verdict so the retry surfaces the real failure instead of
		// stale_generation again (the scheduled restart never rotated the
		// generation it promised).
		ts.RecordAsyncRestartFailure(err)
	case ok:
		slog.Info("transcode seek restart", "component", "playback", "segment", segNum,
			"seek_seconds", target.SeekSeconds, "stream_origin_seconds", target.StreamOriginSeconds,
			"resolved_start_segment", target.StartSegmentNumber, "session", sessionID)
	}
}

// followAsyncRestartSuccessor adopts the live generation that replaced the one
// the far-seek restart was queued on, so the seek is applied to the successor
// instead of being dropped with its predecessor.
//
// A replan holds the per-session lifecycle lock across "spawn → register", and
// a detached seek restart waits on that same lock. When the replan commits, the
// live generation has changed and restartSessionLocked answers
// ErrSessionSuperseded. Treating that as teardown is only right when the
// session is gone: a replacement successor is a fresh generation anchored at
// the position the replan started from (usually the session's last reported
// position, which trails the seek), so dropping the restart leaves every later
// request for the seek's segment on a generation that cannot reach it.
//
// The successor is not rebuilt when it already starts at or past the target —
// the seek is already its position — and one worker follows a bounded number of
// successors so a replan storm cannot keep it alive.
func (m *TranscodeManager) followAsyncRestartSuccessor(sessionID string, pending *pendingAsyncRestart, superseded *TranscodeSession) bool {
	if pending == nil {
		return false
	}
	successor := m.GetTranscodeSession(sessionID)
	if successor == nil || successor == superseded {
		return false
	}
	pending.mu.Lock()
	target := pending.segNum
	pending.mu.Unlock()
	if target <= 0 {
		return false
	}
	if start := successor.Opts().StartSegmentNumber; start > 0 && start >= target {
		return false
	}
	m.asyncRestartMu.Lock()
	defer m.asyncRestartMu.Unlock()
	if m.asyncRestarts[sessionID] != pending {
		// A newer request already replaced this worker's coalescing entry, so
		// that worker owns the successor; adopting here would restart it twice.
		return false
	}
	pending.mu.Lock()
	defer pending.mu.Unlock()
	if pending.ts == successor || pending.adoptions >= maxAsyncRestartSuccessors {
		return false
	}
	pending.adoptions++
	pending.ts = successor
	return true
}

func (m *TranscodeManager) restartSessionLocked(sessionID string, ts *TranscodeSession, restart func() error) error {
	unlock := m.LockSessionLifecycle(sessionID)
	defer unlock()
	if live := m.GetTranscodeSession(sessionID); live != ts {
		return ErrSessionSuperseded
	}
	// Install a liveness predicate for the restart's cancel→respawn section.
	// A stop that unregisters this session while the restart is in flight makes
	// the predicate reject, so the restart aborts before spawning (or stops the
	// process it just spawned) instead of leaving ffmpeg running after the close.
	ts.setLifecycleLiveness(func() bool { return m.GetTranscodeSession(sessionID) == ts })
	defer ts.setLifecycleLiveness(nil)
	return restart()
}

// RestartSessionLockedWithCopySeekAnchor is RestartSessionLocked with the
// resolved keyframe origin for a legacy copy-video seek restart.
func (m *TranscodeManager) RestartSessionLockedWithCopySeekAnchor(
	ctx context.Context,
	sessionID string,
	ts *TranscodeSession,
	seekSeconds float64,
	startSegment int,
	streamOriginSeconds float64,
) error {
	return m.restartSessionLocked(sessionID, ts, func() error {
		return ts.RestartWithCopySeekAnchor(ctx, seekSeconds, startSegment, streamOriginSeconds)
	})
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
	// SessionUnavailable: a playback session was recoverable from its signed
	// recipe, but its local transcode runtime could not be reconstructed. The
	// failure is retryable and no provisional playback session remains visible.
	SessionUnavailable
	// SessionUnauthorized: the live session negotiated authenticated media
	// requests, but this request has no authenticated user identity.
	SessionUnauthorized
)

// LoadOrReconstructSession is the single front door every serve handler uses to
// obtain a playback Session: it looks the session up via getSession and, on a
// not-found miss (e.g. after a restart), reconstructs it from the recipe card,
// re-binding ownership to the live caller. The two-factor ownership rule is
// preserved exactly — a live session with a non-zero, mismatched caller is
// refused; reconstruct itself refuses a zero/mismatched caller — so this widens
// no access. A live session that requires media authorization additionally
// refuses a caller with no identity at all. getSession is supplied by the caller
// (its SessionManager.GetSession) so the manager needs no direct handle on the
// manager type.
//
// card is the reconstruction recipe the caller decoded from the verified stream
// token the client presented, or, for a header-authenticated session, the API's
// validated server-side recovery card (nil when neither exists). It is the sole
// descriptor source, so a not-found session with a nil card is a genuine miss.
func (m *TranscodeManager) LoadOrReconstructSession(ctx context.Context, getSession func(string) (*Session, error), sessionID string, requestUserID int, card *RecipeCard) (*Session, SessionLoadStatus) {
	session, status, _ := m.LoadOrReconstructSessionDetail(ctx, getSession, sessionID, requestUserID, card)
	return session, status
}

// LoadOrReconstructSessionDetail is LoadOrReconstructSessionDetailWithError
// with the underlying failure dropped, for callers that render the status only.
func (m *TranscodeManager) LoadOrReconstructSessionDetail(ctx context.Context, getSession func(string) (*Session, error), sessionID string, requestUserID int, card *RecipeCard) (*Session, SessionLoadStatus, bool) {
	session, status, reconstructed, _ := m.LoadOrReconstructSessionDetailWithError(ctx, getSession, sessionID, requestUserID, card)
	return session, status, reconstructed
}

// LoadOrReconstructSessionDetailWithError is LoadOrReconstructSession plus
// whether the session it returned was rebuilt from the card rather than found
// live, and the backend error behind SessionLoadFailed. A serve handler needs
// the error so its request log names the cause while its response body stays
// generic.
//
// A handler needs the distinction when the card pins a route that may have been
// withdrawn since it was signed: a live session was already re-decided by
// whatever withdrew it, while a reconstruction replays the recipe verbatim and
// has to re-check it. See the copy-safety refusal on the stream serve path.
func (m *TranscodeManager) LoadOrReconstructSessionDetailWithError(ctx context.Context, getSession func(string) (*Session, error), sessionID string, requestUserID int, card *RecipeCard) (*Session, SessionLoadStatus, bool, error) {
	session, err := getSession(sessionID)
	if err != nil {
		if !errors.Is(err, ErrSessionNotFound) {
			return nil, SessionLoadFailed, false, err
		}
		// A nil manager (documented optional on StreamHandler) cannot reconstruct,
		// so a missing session is simply not-found rather than a panic.
		if m == nil || card == nil {
			return nil, SessionMissing, false, nil
		}
		// Lost the in-memory session (e.g. restart): rebuild it from the token's
		// recipe. ReconstructSession re-binds the session to the card owner and
		// refuses a non-zero caller that mismatches it (a zero caller is allowed for
		// the authless bearer routes), so a nil result here is a genuine not-found.
		session = m.ReconstructSession(ctx, sessionID, requestUserID, *card)
		if session == nil {
			return nil, SessionMissing, false, nil
		}
		return session, SessionLoaded, true, nil
	}
	// Live session: enforce the existing ownership check. A zero caller is
	// allowed (these routes treat the session UUID as a bearer when auth is
	// optional); a non-zero mismatch is refused.
	//
	// A session that negotiated header-authenticated media is the exception: its
	// UUID is a route identifier and never a credential, so an unauthenticated
	// caller is refused here — at the front door every serve handler shares —
	// rather than at each handler's own fast path. Sessions that never
	// negotiated the mode (legacy v3 and jellycompat alike) keep the bearer
	// behavior unchanged.
	if requestUserID == 0 && session.RequireMediaAuthorization {
		return nil, SessionUnauthorized, false, nil
	}
	if requestUserID != 0 && session.UserID != requestUserID {
		return nil, SessionForbidden, false, nil
	}
	return session, SessionLoaded, false, nil
}

type transcodeLoadResult struct {
	session *Session
	runtime *TranscodeSession
	status  SessionLoadStatus
	err     error
}

// LoadOrReconstructTranscode atomically recovers the playback Session and its
// local TranscodeSession for native manifest/segment serving. A failed runtime
// rebuild rolls back only the exact playback Session inserted by that attempt;
// concurrent legitimate successors survive. Concurrent serve requests
// serialize the whole operation, so each re-checks state only after the prior
// attempt has either committed or rolled back.
func (m *TranscodeManager) LoadOrReconstructTranscode(
	ctx context.Context,
	getSession func(string) (*Session, error),
	sessionID string,
	requestUserID int,
	requestedSegment int,
	card *RecipeCard,
) (*Session, *TranscodeSession, SessionLoadStatus) {
	session, runtime, status, _ := m.LoadOrReconstructTranscodeWithError(ctx, getSession, sessionID, requestUserID, requestedSegment, card)
	return session, runtime, status
}

func (m *TranscodeManager) LoadOrReconstructTranscodeWithError(
	ctx context.Context,
	getSession func(string) (*Session, error),
	sessionID string,
	requestUserID int,
	requestedSegment int,
	card *RecipeCard,
) (*Session, *TranscodeSession, SessionLoadStatus, error) {
	if m == nil {
		return nil, nil, SessionMissing, nil
	}
	if card != nil {
		if card.SessionID == "" || card.SessionID != sessionID {
			return nil, nil, SessionMissing, nil
		}
		if requestUserID != 0 && requestUserID != card.UserID {
			return nil, nil, SessionMissing, nil
		}
	}

	// Serialize the whole playback+runtime attempt separately from the runtime's
	// own lifecycle lock. A waiter re-checks state after rollback/success and
	// therefore never observes a failed leader's provisional session as loaded.
	// This is deliberately separate from the runtime-only singleflight below: a
	// waiter must re-check the playback Session after the prior attempt finishes.
	unlock := m.LockSessionLifecycle("tone-map-reconstruct\x00" + sessionID)
	defer unlock()
	result := m.doLoadOrReconstructTranscode(ctx, getSession, sessionID, requestUserID, requestedSegment, card)
	if result.session != nil && requestUserID != 0 && result.session.UserID != requestUserID {
		return nil, nil, SessionForbidden, nil
	}
	return result.session, result.runtime, result.status, result.err
}

func (m *TranscodeManager) doLoadOrReconstructTranscode(
	ctx context.Context,
	getSession func(string) (*Session, error),
	sessionID string,
	requestUserID int,
	requestedSegment int,
	card *RecipeCard,
) transcodeLoadResult {
	atomicSessions, ok := m.Sessions.(atomicSessionReconstructor)
	if !ok {
		return transcodeLoadResult{status: SessionLoadFailed}
	}
	// Capture before reading the snapshot so a concurrent replacement cannot
	// authorize this reconstruction to publish stale facts onto its successor.
	expected, revision := atomicSessions.CaptureReconstructedExecution(sessionID)
	session, err := getSession(sessionID)
	var inserted *Session
	if err != nil {
		if !errors.Is(err, ErrSessionNotFound) {
			return transcodeLoadResult{status: SessionLoadFailed}
		}
		if card == nil {
			return transcodeLoadResult{status: SessionMissing}
		}
		var ok bool
		session, inserted, ok = m.reconstructSession(ctx, sessionID, requestUserID, *card, true)
		if !ok || session == nil {
			return transcodeLoadResult{status: SessionMissing}
		}
		if inserted != nil {
			expected, revision = inserted, 0
		}
	}
	if requestUserID != 0 && session.UserID != requestUserID {
		return transcodeLoadResult{status: SessionForbidden}
	}
	if runtime := m.GetTranscodeSession(sessionID); runtime != nil {
		return m.completeTranscodeLoad(atomicSessions, getSession, session, expected, revision, runtime)
	}
	// Remote transcodes keep running on their owning node; only the playback
	// Session needs reconstructing on this API process. A reconstructed session
	// still needs its tone-map mode confirmed from the card: the reconstruct
	// defers it so the live executor's confirmation wins when a runtime exists.
	if session.TranscodeNodeURL != "" {
		if inserted != nil {
			current := atomicSessions.ConfirmReconstructedExecution(inserted, 0, card.ToneMapMode, card.EffectiveEncoderHWAccel())
			if current == nil {
				return transcodeLoadResult{status: SessionMissing}
			}
			session = current
		}
		return transcodeLoadResult{session: session, status: SessionLoaded}
	}
	if session.PlayMethod != PlayTranscode {
		if inserted != nil {
			atomicSessions.ConfirmReconstructedExecution(inserted, 0, card.ToneMapMode, card.EffectiveEncoderHWAccel())
		}
		return transcodeLoadResult{session: session, status: SessionLoaded}
	}
	if card == nil {
		return transcodeLoadResult{session: session, status: SessionMissing}
	}

	runtime, reconstructErr := m.ReconstructTranscodeWithError(ctx, sessionID, requestedSegment, *card)
	if runtime == nil {
		if inserted != nil {
			atomicSessions.RollbackReconstructedToneMap(inserted)
		}
		return transcodeLoadResult{status: SessionUnavailable, err: reconstructErr}
	}
	return m.completeTranscodeLoad(atomicSessions, getSession, session, expected, revision, runtime)
}

func (m *TranscodeManager) completeTranscodeLoad(
	atomicSessions atomicSessionReconstructor,
	getSession func(string) (*Session, error),
	session, expected *Session,
	revision uint64,
	runtime *TranscodeSession,
) transcodeLoadResult {
	if expected != nil {
		opts := runtime.Opts()
		current := atomicSessions.ConfirmReconstructedExecution(expected, revision, opts.ToneMapMode, opts.EffectiveEncoderHWAccel())
		if current == nil {
			m.CloseTranscodeSessionIf(session.ID, runtime, "")
			return transcodeLoadResult{status: SessionMissing}
		}
	}
	if current, err := getSession(session.ID); err == nil {
		session = current
	}
	return transcodeLoadResult{session: session, runtime: runtime, status: SessionLoaded}
}

// ReconstructSession rebuilds the in-memory playback Session from a persisted
// recipe card after the server lost its state (restart). It re-binds the session
// to the live authenticated caller and refuses if ownership cannot be confirmed.
// Returns the (re)registered session, or nil if reconstruct is not possible (no
// card, ownership mismatch, or unsupported session manager).
func (m *TranscodeManager) ReconstructSession(ctx context.Context, sessionID string, requestUserID int, card RecipeCard) *Session {
	session, _, ok := m.reconstructSession(ctx, sessionID, requestUserID, card, false)
	if !ok {
		return nil
	}
	return session
}

func (m *TranscodeManager) reconstructSession(ctx context.Context, sessionID string, requestUserID int, card RecipeCard, deferToneMapConfirmation bool) (*Session, *Session, bool) {
	if m == nil || m.Sessions == nil {
		return nil, nil, false
	}
	if card.SessionID == "" || card.SessionID != sessionID {
		// The token's recipe must be for the session id in the URL; a mismatch is
		// a forged or stale request.
		return nil, nil, false
	}
	if err := ValidateCopyFMP4RecipeCard(card); err != nil {
		slog.WarnContext(ctx, "transcode reconstruct copy recipe rejected", "component", "playback",
			"session", sessionID, "playback_session_id", sessionID, "error", err)
		return nil, nil, false
	}
	// Re-bind ownership to the card owner. A zero caller is allowed (the authless
	// transcode delivery routes — HLS master.m3u8 / segment — treat the session
	// UUID as the bearer credential when auth is optional); a non-zero caller that
	// mismatches the card owner is refused. Either way the reconstructed session is
	// bound to card.UserID, never to the request's user.
	if requestUserID != 0 && requestUserID != card.UserID {
		slog.WarnContext(ctx, "transcode reconstruct ownership rejected", "component", "playback",
			"session", sessionID, "playback_session_id", sessionID,
			"request_user", requestUserID, "card_user", card.UserID)
		return nil, nil, false
	}

	// An empty PlayMethod is a card written before direct/remux were
	// reconstructable; treat it as a transcode (the only kind then persisted).
	method := card.PlayMethod
	if method == "" || method == playMethodCopyFMP4V1 {
		method = PlayTranscode
	}

	toneMapMode := card.ToneMapMode
	if deferToneMapConfirmation && method == PlayTranscode {
		toneMapMode = ""
	}
	s := &Session{
		ID:                            card.SessionID,
		UserID:                        card.UserID,
		ProfileID:                     card.ProfileID,
		MediaFileID:                   card.MediaFileID,
		StartedAt:                     card.OriginalStartedAt,
		PlayMethod:                    method,
		BasePlayMethod:                method,
		TranscodeNodeURL:              card.TranscodeNodeURL,
		TranscodeTransportID:          card.TranscodeTransportID,
		RoutingNetworkProvider:        card.RoutingNetworkProvider,
		StreamLocation:                card.StreamLocation,
		RoutingWorkload:               card.RoutingWorkload,
		RoutingExecution:              card.RoutingExecution,
		RoutingExecutionNodeID:        card.RoutingExecutionNodeID,
		RoutingEgress:                 card.RoutingEgress,
		RoutingEgressNodeID:           card.RoutingEgressNodeID,
		AudioTrackIndex:               card.AudioTrackIndex,
		TranscodeAudio:                card.TranscodeAudio,
		RemuxDVMode:                   card.RemuxDVMode,
		RemuxResumeLeadingPictureDrop: card.RemuxResumeLeadingPictureDrop,
		TargetResolution:              card.TargetResolution,
		TargetVideoCodec:              card.TargetCodecVideo,
		TargetAudioCodec:              card.TargetCodecAudio,
		SourceAudioChannels:           card.SourceAudioChannels,
		TargetAudioChannels:           card.TargetAudioChannels,
		TargetAudioBitrateKbps:        card.TargetAudioBitrateKbps,
		TargetBitrateKbps:             card.TargetBitrateKbps,
		TranscodeHWAccel:              card.EffectiveEncoderHWAccel(),
		ToneMapMode:                   toneMapMode,
		// Client metadata survives the restart so the admin views keep the
		// client label and Jellyfin identification for the session's lifetime.
		ClientName:       normalizeClientMetadataValue(card.ClientName, 128),
		ClientVersion:    normalizeClientMetadataValue(card.ClientVersion, 64),
		ClientBuild:      normalizeClientMetadataValue(card.ClientBuild, 64),
		ClientChannel:    normalizeClientMetadataValue(card.ClientChannel, 32),
		ClientUserAgent:  normalizeClientMetadataValue(card.ClientUserAgent, 512),
		DeviceID:         normalizeClientMetadataValue(card.DeviceID, 128),
		IsJellyfinCompat: card.IsJellyfinCompat,
		// The card carries no address; the media request rebuilding the
		// session comes from the viewer's player.
		ClientIP: clientip.FromContext(ctx),
		// Preserve the byte-affecting recipe so an audio switch after a restart
		// rebuilds the same stream (subtitles/cadence) instead of dropping them.
		SubtitleTrackIndex: card.SubtitleTrackIndex,
		SubtitleBurnIn:     card.SubtitleBurnIn,
		SegmentDuration:    card.SegmentDuration,
		// Only a server-side card for a header-authenticated session sets these;
		// a token's claims never do, so token reconstruction is unchanged.
		RequireMediaAuthorization:  card.RequireMediaAuthorization,
		RequestedMediaFileID:       card.RequestedMediaFileID,
		DisableProgressPersistence: card.DisableProgressPersistence,
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(card.InputPath)), "virtual://") {
		s.VirtualSourceURI = card.InputPath
		s.VirtualSourceOwnerInstallationID = card.VirtualSourceOwnerInstallationID
	}
	if card.IsTranscodeRecipe() {
		s.OutputContainer = HLSOutputContainer(card.TranscodeOpts("", "", nil))
		s.OutputProtocol = OutputProtocolHLS
	} else if method == PlayRemux {
		s.OutputContainer, s.OutputProtocol = OutputContainerFMP4, OutputProtocolHTTP
	}
	// Enforce the same per-user concurrency caps a fresh StartSession would, so a
	// replayed token cannot reconstruct past the user's limit. Reconstructing the
	// user's own surviving sessions still succeeds up to the cap; only the over-cap
	// replay is rejected.
	session, err := m.Sessions.RegisterReconstructedWithLimits(ctx, s)
	if err != nil {
		// Admission denials must still refuse: a replayed token cannot reconstruct
		// past a current cap or after transcoding has been disabled for the user.
		if errors.Is(err, ErrTooManyStreams) || errors.Is(err, ErrTooManyTranscodes) || errors.Is(err, ErrTranscodingDisabled) || errors.Is(err, ErrAudioTranscodingDisabled) {
			slog.WarnContext(ctx, "playback session reconstruct refused by admission policy", "component", "playback",
				"session", sessionID, "playback_session_id", sessionID,
				"user", card.UserID, "method", method, "error", err)
			return nil, nil, false
		}
		// Otherwise the limit provider itself could not be evaluated (e.g. a
		// transient Postgres error during a post-restart reconstruct wave). Fail
		// open and admit the session WITHOUT the limit gate: denying here would
		// collapse a recoverable dependency error into a permanent 404 and stop
		// playback for a user who is within their limits. The cap will re-apply on
		// the next fresh StartSession once the provider recovers.
		slog.WarnContext(ctx, "playback session reconstruct admitting despite unevaluated limits (degraded; limit provider unavailable)", "component", "playback",
			"session", sessionID, "playback_session_id", sessionID,
			"user", card.UserID, "method", method, "error", err)
		session = m.Sessions.RegisterReconstructed(s)
	}
	slog.InfoContext(ctx, "playback session reconstructed from recipe card", "component", "playback",
		"session", sessionID, "playback_session_id", sessionID, "user", card.UserID, "method", method)
	var inserted *Session
	if session == s {
		inserted = s
	}
	return session, inserted, true
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
	session, _ := m.ReconstructTranscodeWithError(ctx, sessionID, requestedSegment, card)
	return session
}

// ReconstructTranscodeWithError preserves execution-time recipe failures for
// callers that must distinguish stale source facts from transient validation.
func (m *TranscodeManager) ReconstructTranscodeWithError(ctx context.Context, sessionID string, requestedSegment int, card RecipeCard) (*TranscodeSession, error) {
	if m == nil {
		return nil, nil
	}
	if card.SessionID == "" || card.SessionID != sessionID {
		return nil, nil
	}

	// A concurrent reconstruct may already have registered the session; serve it
	// directly so we never enter single-flight only to discard a duplicate.
	if existing := m.GetTranscodeSession(sessionID); existing != nil {
		return existing, nil
	}

	v, err, _ := m.reconstructGroup.Do(sessionID, func() (interface{}, error) {
		return m.doReconstructTranscode(ctx, sessionID, requestedSegment, card)
	})
	if err != nil || v == nil {
		return nil, err
	}
	session, _ := v.(*TranscodeSession)
	return session, nil
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
func (m *TranscodeManager) doReconstructTranscode(ctx context.Context, sessionID string, requestedSegment int, card RecipeCard) (*TranscodeSession, error) {
	// Only transcode cards drive ffmpeg reconstruction. Direct/remux sessions
	// reconstruct without a runtime and must never reach here; guard so a
	// direct/remux card ID cannot accidentally spawn an encode. An empty
	// PlayMethod is back-compat for a token minted before the discriminator
	// (transcode).
	if !card.IsTranscodeRecipe() {
		return nil, nil
	}
	if err := ValidateCopyFMP4RecipeCard(card); err != nil {
		return nil, err
	}

	// Mark in-flight for the whole rebuild so a concurrent cleanup never reaps the
	// output dir between spawn and map registration.
	release := m.markReconstructing(sessionID)
	defer release()

	cfg := m.runtimeConfig()
	outputDir := reconstructionOutputDir(cfg.TranscodeDir, sessionID, card.OutputSubdir)
	opts := card.TranscodeOpts(outputDir, cfg.FFmpegPath, m.logSink())
	opts.Reconstructed = true
	// Recipe cards preserve the original launch tuning, but a reconstruction is
	// not a fresh generation: restore the conservative manifest lead so recovery
	// never exposes a hardware encoder after only one fragment.
	opts.FastStart = false
	opts.OnDemuxFailure = m.OnDemuxFailure
	opts.OnSourceRejected = m.OnSourceRejected

	if m.ResolveInput != nil {
		resolved, cleanup, err := m.ResolveInput(ctx, card.MediaFileID, card.VirtualSourceOwnerInstallationID, card.UserID, card.ProfileID, opts.InputPath)
		if err != nil {
			slog.ErrorContext(ctx, "reconstruct transcode input resolution failed", "component", "playback", "error", err, "session", sessionID, "playback_session_id", sessionID)
			return nil, err
		}
		if resolved != "" {
			opts.CanonicalInputPath = opts.InputPath
			opts.InputPath = resolved
			opts.InputCleanup = cleanup
			canonicalPath := opts.CanonicalInputPath
			opts.RefreshInput = func(refreshCtx context.Context) (string, func(), error) {
				return m.ResolveInput(refreshCtx, card.MediaFileID, card.VirtualSourceOwnerInstallationID, card.UserID, card.ProfileID, canonicalPath)
			}
		}
	}
	inputTransferred := false
	defer func() {
		if !inputTransferred && opts.InputCleanup != nil {
			opts.InputCleanup()
		}
	}()
	// Re-resolve environment-specific encode knobs from current config so an
	// operator config change applies to reconstructed sessions too.
	opts.HWAccel = cfg.HWAccel
	opts.HWDevice = cfg.HWDevice
	opts.SegmentRetentionSeconds = cfg.SegmentRetentionSeconds

	// Serialize before taking a global pacing slot. A reconstruct stalled behind
	// another start for this session must not consume capacity needed by unrelated
	// sessions after a restart.
	unlock := m.LockSessionLifecycle(sessionID)
	defer unlock()
	if existing := m.GetTranscodeSession(sessionID); existing != nil {
		return existing, nil
	}

	// Bound executor probing and any child validation commands under the same
	// pacing slot as the eventual ffmpeg spawn. A canceled request must be able
	// to leave the queue before doing that work.
	slotRelease, ok := m.acquireReconstructSlot(ctx)
	if !ok {
		return nil, ctx.Err()
	}
	defer slotRelease()
	var toneMapErr error
	resolveToneMapExecutor := m.resolveToneMapExecutor
	if resolveToneMapExecutor == nil {
		resolveToneMapExecutor = ResolveToneMapExecutor
	}
	opts, toneMapErr = resolveToneMapExecutor(ctx, opts)
	if toneMapErr != nil {
		slog.ErrorContext(ctx, "reconstruct tone-map recipe unavailable", "component", "playback", "error", toneMapErr,
			"session", sessionID, "playback_session_id", sessionID)
		return nil, toneMapErr
	}

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

	startTranscode := m.startTranscode
	if startTranscode == nil {
		startTranscode = StartTranscode
	}
	newPipeline := m.autoTranscodePipeline
	if newPipeline == nil {
		newPipeline = NewAutoTranscodePipeline
	}
	// Under hw_accel=auto a reconstruct walks the same safer paths as a fresh
	// start, keeping a slow process rather than duplicating it. Every other
	// recipe starts once without waiting, as segment requests already wait for
	// a reconstructed process.
	transcodeSession, err := StartReconstructTranscode(ctx, newPipeline(ctx, opts), TranscodeStartup{Start: startTranscode})
	if err != nil {
		slog.ErrorContext(ctx, "reconstruct transcode start failed", "component", "playback", "error", err, "session", sessionID, "playback_session_id", sessionID)
		return nil, err
	}
	inputTransferred = true

	// Register under the map lock. The lifecycle lock guarantees no other path
	// registered since the re-check above; the existing-check is kept as defensive
	// belt-and-braces, closing only the duplicate ffmpeg process (never the shared
	// output dir the winner serves) on the should-be-impossible race.
	m.transcodeMu.Lock()
	if m.shuttingDown {
		m.transcodeMu.Unlock()
		_ = transcodeSession.Close()
		return nil, context.Canceled
	}
	if existing := m.transcodes[sessionID]; existing != nil {
		m.transcodeMu.Unlock()
		_ = transcodeSession.CloseProcess()
		return existing, nil
	}
	m.transcodes[sessionID] = transcodeSession
	m.transcodeMu.Unlock()

	// Mirror the handler's start path: re-arm the throttler and exit monitor
	// after every Restart of this reconstructed session, so seek/audio-switch
	// restarts keep the same wiring as a freshly started transcode.
	transcodeSession.SetRestartHook(func(ctx context.Context) {
		if m.StartThrottler != nil {
			m.StartThrottler(ctx, transcodeSession)
		}
		m.MonitorLocalTranscodeExit(sessionID, transcodeSession)
	})

	if m.StartThrottler != nil {
		m.StartThrottler(ctx, transcodeSession)
	}
	m.MonitorLocalTranscodeExit(sessionID, transcodeSession)
	slog.InfoContext(ctx, "transcode process reconstructed from recipe card", "component", "playback",
		"session", sessionID, "playback_session_id", sessionID,
		"requested_segment", requestedSegment, "start_segment_number", opts.StartSegmentNumber)
	return transcodeSession, nil
}

func reconstructionOutputDir(root, sessionID, signedSubdir string) string {
	outputSubdir := sessionID
	if candidate := filepath.Clean(signedSubdir); candidate != "." && !filepath.IsAbs(candidate) && candidate != ".." && !strings.HasPrefix(candidate, ".."+string(filepath.Separator)) {
		first, _, _ := strings.Cut(candidate, string(filepath.Separator))
		if first == sessionID || strings.HasPrefix(first, sessionID+"-") {
			outputSubdir = candidate
		}
	}
	return filepath.Join(root, outputSubdir)
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
// A concurrent restart's cancel→respawn critical section is protected by a
// liveness predicate the manager installs around Restart (see
// restartSessionLocked): the restart re-checks that this session is still the
// live mapped one before and after spawning, so a stop that unregisters it
// during the window cannot leave the replacement ffmpeg running. The close
// itself therefore needs no lifecycle lock — several stop paths already hold it.
//
// Under token-carried reconstruction there is no durable card to drop: a stopped
// session simply stops being served, and its segment dir is reaped by the
// in-memory-liveness + age cleanup once no live token could still reconstruct it
// (see CleanupOrphanedTranscodes). A remote node fences the stopped transport
// for the token lifetime, so its still-valid tokens cannot rebuild it there.
func (m *TranscodeManager) CloseTranscodeSession(sessionID, transcodeNodeURL string) {
	m.closeTranscodeSessionLocal(sessionID)
	m.StopRemoteTranscode(sessionID, transcodeNodeURL)
}

// closeTranscodeSessionLocal removes and closes the local runtime and retained
// generation for sessionID.
func (m *TranscodeManager) closeTranscodeSessionLocal(sessionID string) {
	// Clean up local session if one exists (defensive).
	m.transcodeMu.Lock()
	session := m.transcodes[sessionID]
	delete(m.transcodes, sessionID)
	retained := m.retired[sessionID]
	delete(m.retired, sessionID)
	stopRetainedTimerLocked(retained)
	reaped := retained == nil || claimRetainedReapedLocked(retained)
	m.transcodeMu.Unlock()
	if session != nil {
		_ = session.Close()
	}
	if reaped && retained != nil && retained.Session != nil {
		_ = retained.Session.Close()
	}
}

// RetainedGeneration is a displaced local transcode generation kept servable
// for a bounded overlap window after a same-session replan publishes its
// successor. The predecessor's process is stopped but its output directory is
// left in place, so manifest/segment requests landing before the client adopts
// the new playlist are served from already-produced bytes instead of 503.
type RetainedGeneration struct {
	Session   *TranscodeSession
	ExpiresAt time.Time
	// reaped is closed exactly once by whichever path (timer, read, close, or
	// shutdown) wins the entry. Every path Closes the session outside the map
	// lock, and time.AfterFunc may already be running when another path wins
	// the entry (Stop does not wait for a running callback), so the loser must
	// skip its Close rather than double-closing the session. Guarded by
	// transcodeMu.
	reaped chan struct{}
	// timer reaps the entry at ExpiresAt even when no serve request ever reads
	// it. Read-time reaping alone would leave a displaced directory pinned past
	// its window when a switch is lost before a successor is registered (for
	// example the switching worker dying between retire and publish), until the
	// coarse orphan sweep hours later. Guarded by transcodeMu.
	timer *time.Timer
}

// RetainedGenerationRetention bounds how long a displaced generation stays
// servable. It covers the client's playlist-reload latency after a track or
// version switch without pinning a stale directory indefinitely.
const RetainedGenerationRetention = 30 * time.Second

// RetireTranscodeSessionPredecessor displaces the predecessor without deleting
// its output directory and keeps it servable for the bounded overlap. The
// previous generation's process is stopped immediately (CloseProcess) so it
// cannot compete for CPU; only its files are retained. A nil or already-retired
// predecessor is a no-op. When retention is non-positive the predecessor is
// closed outright, restoring the pre-overlap behavior.
func (m *TranscodeManager) RetireTranscodeSessionPredecessor(sessionID string, predecessor *TranscodeSession, retention time.Duration) {
	if m == nil || sessionID == "" || predecessor == nil {
		return
	}
	if retention <= 0 {
		_ = predecessor.Close()
		return
	}
	// Stop the process but leave the output directory for the overlap window.
	// This stop must NOT advance the generation: a retained predecessor has to
	// keep matching the exact sgen token its in-flight playlist was minted with,
	// or every one-behind segment request is refused with 412 and the client
	// loops on a dead URL (the switchover regression). CloseProcess would bump
	// the generation and break that match.
	_ = predecessor.CloseProcessRetained()
	m.transcodeMu.Lock()
	if m.shuttingDown {
		m.transcodeMu.Unlock()
		_ = predecessor.Close()
		return
	}
	if m.retired == nil {
		m.retired = make(map[string]*RetainedGeneration)
	}
	// Replace any prior retained generation: only the most recently displaced
	// bytes can still match the client's in-flight playlist. Its timer is
	// stopped so a replaced entry cannot fire later. Its reaped channel is
	// claimed here, so a timer already running when this replacement wins
	// cannot double-close the displaced session. The entry is swapped under
	// the same lock acquisition that claimed the prior, so a racing timer
	// that already passed its map check finds neither entry and reaps
	// nothing.
	if prior := m.retired[sessionID]; prior != nil {
		stopRetainedTimerLocked(prior)
		reaped := claimRetainedReapedLocked(prior)
		delete(m.retired, sessionID)
		if reaped && prior.Session != nil && prior.Session != predecessor {
			m.transcodeMu.Unlock()
			_ = prior.Session.Close()
			m.transcodeMu.Lock()
		}
	}
	entry := &RetainedGeneration{Session: predecessor, ExpiresAt: time.Now().Add(retention), reaped: make(chan struct{})}
	m.retired[sessionID] = entry
	// Reap at expiry even if no serve request ever reads the entry, so a switch
	// lost before its successor is published cannot pin the directory past the
	// bounded window.
	entry.timer = time.AfterFunc(retention, func() {
		m.reapRetainedIfCurrent(sessionID, entry)
	})
	m.transcodeMu.Unlock()
}

// reapRetainedIfCurrent removes and closes a retained generation only while the
// map still holds this exact entry, so a replacement or an earlier read that
// already reaped it is left untouched. It is the timer-driven counterpart of
// the read-time reap in GetRetainedTranscodeSession. The entry's reaped channel
// fences the Close against a racing read: time.AfterFunc may already be running
// when a read wins the entry, and Stop does not wait for it, so the loser must
// skip its Close rather than double-closing the session.
func (m *TranscodeManager) reapRetainedIfCurrent(sessionID string, entry *RetainedGeneration) {
	if m == nil || entry == nil {
		return
	}
	m.transcodeMu.Lock()
	if m.retired[sessionID] != entry {
		m.transcodeMu.Unlock()
		return
	}
	delete(m.retired, sessionID)
	reaped := claimRetainedReapedLocked(entry)
	m.transcodeMu.Unlock()
	if !reaped {
		return
	}
	if entry.Session != nil {
		_ = entry.Session.Close()
	}
}

// stopRetainedTimerLocked stops an entry's expiry timer. Callers already hold
// transcodeMu; the AfterFunc callback only ever takes the lock, so stopping
// under it cannot deadlock (Stop does not wait for a running callback).
func stopRetainedTimerLocked(entry *RetainedGeneration) {
	if entry != nil && entry.timer != nil {
		entry.timer.Stop()
		entry.timer = nil
	}
}

// claimRetainedReapedLocked closes the entry's reaped channel iff this path is
// the first to reap it, reporting whether the caller owns the Close. Callers
// already hold transcodeMu.
func claimRetainedReapedLocked(entry *RetainedGeneration) bool {
	if entry.reaped == nil {
		entry.reaped = make(chan struct{})
		close(entry.reaped)
		return true
	}
	select {
	case <-entry.reaped:
		return false
	default:
		close(entry.reaped)
		return true
	}
}

// GetRetainedTranscodeSession returns a displaced generation still inside its
// overlap window, or nil. It reaps an expired entry (deleting its output dir)
// on read; the entry's own timer reaps it out-of-band when no read arrives.
// The reaped channel fences the Close against a racing timer (see
// reapRetainedIfCurrent).
func (m *TranscodeManager) GetRetainedTranscodeSession(sessionID string) *TranscodeSession {
	if m == nil {
		return nil
	}
	m.transcodeMu.Lock()
	entry := m.retired[sessionID]
	if entry == nil {
		m.transcodeMu.Unlock()
		return nil
	}
	if !time.Now().Before(entry.ExpiresAt) {
		delete(m.retired, sessionID)
		stopRetainedTimerLocked(entry)
		reaped := claimRetainedReapedLocked(entry)
		m.transcodeMu.Unlock()
		if reaped && entry.Session != nil {
			_ = entry.Session.Close()
		}
		return nil
	}
	session := entry.Session
	m.transcodeMu.Unlock()
	return session
}

// StartShutdownCleanup closes every local transcode when ctx is canceled and
// returns a channel closed after cleanup finishes.
func (m *TranscodeManager) StartShutdownCleanup(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	if m == nil || ctx == nil {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		<-ctx.Done()

		m.transcodeMu.Lock()
		m.shuttingDown = true
		transcodes := m.transcodes
		m.transcodes = make(map[string]*TranscodeSession)
		retired := m.retired
		m.retired = make(map[string]*RetainedGeneration)
		reaped := make([]*RetainedGeneration, 0, len(retired))
		for _, entry := range retired {
			stopRetainedTimerLocked(entry)
			if entry != nil && claimRetainedReapedLocked(entry) {
				reaped = append(reaped, entry)
			}
		}
		m.transcodeMu.Unlock()
		for _, session := range transcodes {
			if session != nil {
				_ = session.Close()
			}
		}
		for _, entry := range reaped {
			if entry.Session != nil {
				_ = entry.Session.Close()
			}
		}
	}()
	return done
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

	m.StopRemoteTranscode(sessionID, transcodeNodeURL)
	return true
}

// CloseTranscodeSessionIfLocked is CloseTranscodeSessionIf for callers that
// already own the session lifecycle lock.
func (m *TranscodeManager) CloseTranscodeSessionIfLocked(sessionID string, expected *TranscodeSession, transcodeNodeURL string) bool {
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

	m.StopRemoteTranscode(sessionID, transcodeNodeURL)
	return true
}

// remoteTranscodeControlClient sends stop requests to transcode nodes. Playback
// paths call the stop synchronously, so a stop aimed at a dead host must give
// up as quickly as the start that found it dead: it uses the same connect bound
// as playback's other node calls.
var remoteTranscodeControlClient = func() *http.Client {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	transport := base.Clone()
	transport.DialContext = nodepool.NodeDialContext()
	return &http.Client{Transport: transport}
}()

// deleteRemoteTranscode sends DELETE to the assigned transcode node with a
// bounded timeout. A missing process is already canceled and therefore
// succeeds; other transport and HTTP failures remain retryable by the caller.
func (m *TranscodeManager) deleteRemoteTranscode(ctx context.Context, sessionID, transcodeNodeURL string, fence bool) error {
	if transcodeNodeURL == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	deleteURL := transcodeNodeURL + "/transcode/" + sessionID
	if !fence {
		deleteURL += "?" + TranscodeStopFenceQueryParam + "=0"
	}
	req, err := http.NewRequestWithContext(deleteCtx, http.MethodDelete, deleteURL, nil)
	if err != nil {
		return fmt.Errorf("build remote transcode delete request: %w", err)
	}
	if err := nodeauth.Sign(req, transcodeNodeURL, m.jwtSecret()); err != nil {
		return fmt.Errorf("sign remote transcode delete: %w", err)
	}

	resp, err := telemetry.DoTrustedNode(remoteTranscodeControlClient, req, "transcode_stop")
	if err != nil {
		return fmt.Errorf("send remote transcode delete: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= http.StatusMultipleChoices && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("remote transcode delete returned status %d", resp.StatusCode)
	}
	return nil
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
