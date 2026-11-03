package playback

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// fakeSessionRegistry is a GetSession + RegisterReconstructed double.
type fakeSessionRegistry struct {
	mu       sync.Mutex
	sessions map[string]*Session
	// limitsErr, when non-nil, is returned by RegisterReconstructedWithLimits to
	// simulate a limit-provider failure (e.g. a transient Postgres error), so the
	// fail-open admission path can be exercised. A real SessionManager surfaces
	// such failures wrapped with ErrLimitProviderUnavailable.
	limitsErr error
}

func (f *fakeSessionRegistry) GetSession(id string) (*Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[id]; ok {
		return s, nil
	}
	return nil, ErrSessionNotFound
}

func (f *fakeSessionRegistry) RegisterReconstructed(s *Session) *Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sessions == nil {
		f.sessions = map[string]*Session{}
	}
	if existing, ok := f.sessions[s.ID]; ok {
		return existing
	}
	f.sessions[s.ID] = s
	return s
}

// RegisterReconstructedWithLimits mirrors RegisterReconstructed for the tests.
func (f *fakeSessionRegistry) RegisterReconstructedWithLimits(_ context.Context, s *Session) (*Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sessions == nil {
		f.sessions = map[string]*Session{}
	}
	if existing, ok := f.sessions[s.ID]; ok {
		return existing, nil
	}
	if f.limitsErr != nil {
		return nil, f.limitsErr
	}
	f.sessions[s.ID] = s
	return s, nil
}

func (f *fakeSessionRegistry) RollbackReconstructedToneMap(expected *Session) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if expected == nil || f.sessions[expected.ID] != expected {
		return false
	}
	delete(f.sessions, expected.ID)
	return true
}

func (f *fakeSessionRegistry) CaptureReconstructedExecution(sessionID string) (*Session, uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	current := f.sessions[sessionID]
	if current == nil {
		return nil, 0
	}
	return current, current.streamRevision
}

func (f *fakeSessionRegistry) ConfirmReconstructedExecution(expected *Session, revision uint64, mode tonemap.Mode, encoderHWAccel string) *Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	if expected == nil {
		return nil
	}
	current := f.sessions[expected.ID]
	if current == expected && current.streamRevision == revision {
		current.ToneMapMode = mode
		current.TranscodeHWAccel = encoderHWAccel
	}
	return current
}

func TestStartShutdownCleanup_ClosesEveryLocalTranscode(t *testing.T) {
	m := NewTranscodeManager()
	dirs := map[string]string{"s1": t.TempDir(), "s2": t.TempDir()}
	for id, dir := range dirs {
		m.RegisterTranscodeSession(id, &TranscodeSession{outputDir: dir})
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := m.StartShutdownCleanup(ctx)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown cleanup did not finish")
	}
	for id, dir := range dirs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("shutdown left output for %q: %v", id, err)
		}
		if got := m.GetTranscodeSession(id); got != nil {
			t.Fatalf("transcode %q survived shutdown cleanup", id)
		}
	}
}

func TestStartShutdownCleanup_RejectsLaterRegistrationAndSwap(t *testing.T) {
	m := NewTranscodeManager()
	ctx, cancel := context.WithCancel(context.Background())
	done := m.StartShutdownCleanup(ctx)
	cancel()
	<-done

	for _, test := range []struct {
		name    string
		publish func(*TranscodeSession) bool
	}{
		{name: "register", publish: func(session *TranscodeSession) bool {
			return m.RegisterTranscodeSession("late-register", session)
		}},
		{name: "swap", publish: func(session *TranscodeSession) bool {
			_, accepted := m.SwapTranscodeSession("late-swap", session)
			return accepted
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "session")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			session := NewTranscodeSessionForTest(dir)
			if test.publish(session) {
				t.Fatal("session was published after shutdown")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("rejected session cache still exists: %v", err)
			}
		})
	}
}

func TestLoadOrReconstructSession(t *testing.T) {
	ctx := context.Background()
	cardPtr := func(c RecipeCard) *RecipeCard { return &c }
	registered := func(t *testing.T, reg *fakeSessionRegistry) {
		t.Helper()
		if _, err := reg.GetSession("s"); err != nil {
			t.Fatalf("reconstructed session not registered: %v", err)
		}
	}

	tests := []struct {
		name       string
		live       *Session // registered under its ID before the lookup
		limitsErr  error
		id         string // defaults to "s"
		caller     int
		card       *RecipeCard
		wantStatus SessionLoadStatus
		check      func(*testing.T, *Session, *fakeSessionRegistry)
	}{
		{
			name: "live session, matching owner -> loaded", live: &Session{ID: "s", UserID: 5}, caller: 5, wantStatus: SessionLoaded,
			check: func(t *testing.T, got *Session, _ *fakeSessionRegistry) {
				if got == nil || got.ID != "s" {
					t.Fatalf("session = %+v", got)
				}
			},
		},
		{name: "live session, mismatched owner -> forbidden", live: &Session{ID: "s", UserID: 5}, caller: 9, wantStatus: SessionForbidden},
		{name: "live session, zero caller -> loaded (UUID as bearer)", live: &Session{ID: "s", UserID: 5}, caller: 0, wantStatus: SessionLoaded},
		// A v3 transport that negotiated header-authenticated media never treats its
		// session UUID as a credential, so the front door refuses an anonymous
		// caller instead of leaving that rule to each serve handler.
		{
			name: "live media-authorized session, zero caller -> unauthorized", live: &Session{ID: "s", UserID: 5, RequireMediaAuthorization: true},
			caller: 0, wantStatus: SessionUnauthorized,
			check: func(t *testing.T, got *Session, _ *fakeSessionRegistry) {
				if got != nil {
					t.Fatalf("session = %+v, want unauthorized", got)
				}
			},
		},
		{name: "live media-authorized session, owner -> loaded", live: &Session{ID: "s", UserID: 5, RequireMediaAuthorization: true}, caller: 5, wantStatus: SessionLoaded},
		{
			name: "miss + remux token + matching owner -> reconstructed with method", caller: 5,
			card: cardPtr(NewRemuxRecipeCard("s", 5, "p", 77, true, 2)), wantStatus: SessionLoaded,
			check: func(t *testing.T, got *Session, reg *fakeSessionRegistry) {
				if got == nil || got.PlayMethod != PlayRemux || got.MediaFileID != 77 || !got.TranscodeAudio || got.AudioTrackIndex != 2 {
					t.Fatalf("reconstructed remux session wrong: %+v", got)
				}
				registered(t, reg)
			},
		},
		{name: "miss + token + mismatched owner -> missing (reconstruct refuses)", caller: 9, card: cardPtr(NewDirectRecipeCard("s", 5, "p", 77)), wantStatus: SessionMissing},
		{name: "miss + token for a different session id -> missing", caller: 5, card: cardPtr(NewDirectRecipeCard("other", 5, "p", 77)), wantStatus: SessionMissing},
		{name: "miss + no token -> missing", id: "nope", caller: 5, wantStatus: SessionMissing},
		// The fail-open admission surfaces as SessionLoaded (not SessionMissing
		// -> 404) when the limit provider is transiently unavailable.
		{
			name:      "miss + token + provider error -> loaded (fails open)",
			limitsErr: fmt.Errorf("load session limits: %w", errors.Join(ErrLimitProviderUnavailable, errors.New("db timeout"))),
			caller:    5, card: cardPtr(NewDirectRecipeCard("s", 5, "p", 77)), wantStatus: SessionLoaded,
			check: func(t *testing.T, got *Session, reg *fakeSessionRegistry) {
				if got == nil || got.ID != "s" || got.UserID != 5 {
					t.Fatalf("admitted session wrong: %+v", got)
				}
				registered(t, reg)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &fakeSessionRegistry{limitsErr: tt.limitsErr}
			if tt.live != nil {
				reg.sessions = map[string]*Session{tt.live.ID: tt.live}
			}
			m := NewTranscodeManager()
			m.Sessions = reg
			id := tt.id
			if id == "" {
				id = "s"
			}
			got, status := m.LoadOrReconstructSession(ctx, reg.GetSession, id, tt.caller, tt.card)
			if status != tt.wantStatus {
				t.Fatalf("status = %v session = %+v, want %v", status, got, tt.wantStatus)
			}
			if tt.check != nil {
				tt.check(t, got, reg)
			}
		})
	}
}

func TestLoadOrReconstructTranscode_TransientExecutorFailureRetriesWithoutZombie(t *testing.T) {
	ctx := context.Background()
	sessions := NewSessionManager(0, 0)
	m := NewTranscodeManager()
	m.Sessions = sessions
	m.Config = func() TranscodeRuntimeConfig { return TranscodeRuntimeConfig{} }

	card := NewRecipeCard(5, "p", 77, "", TranscodeOpts{
		SessionID:            "s",
		ToneMapPolicy:        tonemap.PolicySoftwareOnly,
		ToneMapMode:          tonemap.ModeSoftware,
		ToneMapSourceKind:    tonemap.SourcePQ,
		ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
		TargetCodecVideo:     "h264",
		FastStart:            true,
	})

	var resolveCalls int
	m.resolveToneMapExecutor = func(_ context.Context, opts TranscodeOpts) (TranscodeOpts, error) {
		resolveCalls++
		provisional, err := sessions.GetSession("s")
		if err != nil {
			t.Fatalf("provisional session missing during executor resolution: %v", err)
		}
		if provisional.ToneMapMode != "" {
			t.Fatalf("provisional ToneMapMode = %q, want empty until executor confirmation", provisional.ToneMapMode)
		}
		if resolveCalls == 1 {
			return opts, errors.New("temporary capability probe failure")
		}
		opts.ToneMapMode = tonemap.ModeSoftware
		opts.ToneMapFilter = tonemap.SoftwareFilterHable
		opts.HWAccel = HWAccelNone
		return opts, nil
	}
	m.startTranscode = func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		return &TranscodeSession{opts: opts}, nil
	}

	got, runtime, status := m.LoadOrReconstructTranscode(ctx, sessions.GetSession, "s", 5, -1, &card)
	if status != SessionUnavailable || got != nil || runtime != nil {
		t.Fatalf("first attempt = status %v session %v runtime %v, want retryable unavailable", status, got, runtime)
	}
	if _, err := sessions.GetSession("s"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("failed attempt left a zombie session: %v", err)
	}

	got, runtime, status = m.LoadOrReconstructTranscode(ctx, sessions.GetSession, "s", 5, -1, &card)
	if status != SessionLoaded || got == nil || runtime == nil {
		t.Fatalf("retry = status %v session %v runtime %v, want loaded", status, got, runtime)
	}
	if runtime.Opts().FastStart {
		t.Fatal("reconstructed runtime retained fresh-start manifest tuning")
	}
	if got.ToneMapMode != tonemap.ModeSoftware {
		t.Fatalf("confirmed ToneMapMode = %q, want software", got.ToneMapMode)
	}
	stored, err := sessions.GetSession("s")
	if err != nil || stored.ToneMapMode != tonemap.ModeSoftware {
		t.Fatalf("stored confirmed session = %+v, err %v", stored, err)
	}
	if resolveCalls != 2 {
		t.Fatalf("executor resolution calls = %d, want one per attempt", resolveCalls)
	}
}

func TestLoadOrReconstructTranscode_FailedAttemptPreservesConcurrentSession(t *testing.T) {
	ctx := context.Background()
	sessions := NewSessionManager(0, 0)
	m := NewTranscodeManager()
	m.Sessions = sessions

	card := NewRecipeCard(5, "p", 77, "", TranscodeOpts{
		SessionID:            "s",
		ToneMapPolicy:        tonemap.PolicySoftwareOnly,
		ToneMapMode:          tonemap.ModeSoftware,
		ToneMapSourceKind:    tonemap.SourcePQ,
		ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
		TargetCodecVideo:     "h264",
	})

	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	m.resolveToneMapExecutor = func(_ context.Context, opts TranscodeOpts) (TranscodeOpts, error) {
		close(probeStarted)
		<-releaseProbe
		return opts, errors.New("temporary capability probe failure")
	}

	result := make(chan SessionLoadStatus, 1)
	go func() {
		_, _, status := m.LoadOrReconstructTranscode(ctx, sessions.GetSession, "s", 5, -1, &card)
		result <- status
	}()
	<-probeStarted

	legitimate := &Session{ID: "s", UserID: 5, PlayMethod: PlayTranscode, ToneMapMode: tonemap.ModeHardware}
	if err := sessions.StopSession("s"); err != nil {
		t.Fatalf("replace provisional session: %v", err)
	}
	if got := sessions.RegisterReconstructed(legitimate); got != legitimate {
		t.Fatalf("register concurrent legitimate session = %p, want %p", got, legitimate)
	}
	close(releaseProbe)

	if status := <-result; status != SessionUnavailable {
		t.Fatalf("failed reconstruction status = %v, want unavailable", status)
	}
	got, err := sessions.GetSession("s")
	if err != nil || got.ToneMapMode != tonemap.ModeHardware {
		t.Fatalf("rollback removed or changed concurrent legitimate session: got %+v err %v", got, err)
	}
}

// The crash closures use the matched return of CloseTranscodeSessionIf as the
// authoritative gate for tearing down the upstream playback session. This test
// proves that contract end-to-end for the successor case: when a successor is
// present, the call returns false (so the closure returns early and never stops
// the successor's session), and a second call with the successor as expected
// returns true. The closures (handlers.OnFFmpegCrash / jellycompat
// OnFFmpegCrash) wire `matched` directly to the StopSession/stopPlaybackSessionByID
// decision, so a false gate guarantees the live session is left intact.
func TestCloseTranscodeSessionIf_GateContract(t *testing.T) {
	m := NewTranscodeManager()
	dead := &TranscodeSession{}
	successor := &TranscodeSession{}

	// Reconstruct won the race: successor sits in the slot under the same id.
	m.RegisterTranscodeSession("s1", successor)

	// Crash teardown for the dead session must not match -> closure must NOT
	// proceed to stop the (successor's) playback session.
	if m.CloseTranscodeSessionIf("s1", dead, "") {
		t.Fatalf("gate must be false while a successor owns the id")
	}
	if got := m.GetTranscodeSession("s1"); got != successor {
		t.Fatalf("successor transcode must survive, got %v", got)
	}

	// Tearing down the successor itself (the ordinary later stop) must match.
	if !m.CloseTranscodeSessionIf("s1", successor, "") {
		t.Fatalf("gate must be true when expected matches the live entry")
	}
	if got := m.GetTranscodeSession("s1"); got != nil {
		t.Fatalf("successor must be removed once it is the expected session, got %v", got)
	}
}

// fastResumeSeek must apply the seg×dur fast resume only for encoded transcodes;
// copy-mode cards have variable-duration segments so the fast seek is unsafe.
func TestFastResumeSeek(t *testing.T) {
	encoded := RecipeCard{TargetCodecVideo: "h264", SegmentDuration: 4, StartSegmentNumber: 0}
	if seg, seek, ok := fastResumeSeek(encoded, 10); !ok || seg != 10 || seek != 40 {
		t.Fatalf("encoded fast resume = (%d, %v, %v), want (10, 40, true)", seg, seek, ok)
	}

	// Copy-mode: never apply the seg×dur seek regardless of how far the client advanced.
	copyCard := RecipeCard{TargetCodecVideo: "copy", SegmentDuration: 4, StartSegmentNumber: 0}
	if _, _, ok := fastResumeSeek(copyCard, 10); ok {
		t.Fatal("copy-mode must not apply the fast seg×dur resume seek")
	}
	// Case-insensitive guard.
	if _, _, ok := fastResumeSeek(RecipeCard{TargetCodecVideo: "COPY", SegmentDuration: 4}, 10); ok {
		t.Fatal("copy-mode detection must be case-insensitive")
	}

	// Manifest path (negative segment) and a non-advanced client take no fast seek.
	if _, _, ok := fastResumeSeek(encoded, -1); ok {
		t.Fatal("manifest path (negative segment) must not fast-seek")
	}
	if _, _, ok := fastResumeSeek(encoded, 0); ok {
		t.Fatal("non-advanced client must not fast-seek")
	}
}

// ReconstructSession admission contract. A genuine over-cap rejection must
// STILL refuse even after the fail-open change: the ErrTooManyStreams /
// ErrTooManyTranscodes sentinels are not provider errors. The authless
// transcode delivery routes (HLS master.m3u8 / segment) present no userID, so
// a zero caller must be allowed and the rebuilt session bound to the card
// owner. A non-zero caller that does not match the card owner is still refused.
func TestReconstructSessionAdmission(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		limitsErr error
		card      RecipeCard
		caller    int
		wantNil   bool
	}{
		{name: "too many streams", limitsErr: ErrTooManyStreams, card: NewDirectRecipeCard("a", 7, "p", 100), caller: 7, wantNil: true},
		{name: "too many transcodes", limitsErr: ErrTooManyTranscodes, card: NewDirectRecipeCard("a", 7, "p", 100), caller: 7, wantNil: true},
		{name: "zero caller -> reconstructed, bound to card owner", card: NewDirectRecipeCard("s", 5, "p", 77), caller: 0},
		{name: "non-zero mismatched caller -> refused", card: NewDirectRecipeCard("s", 5, "p", 77), caller: 9, wantNil: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &fakeSessionRegistry{limitsErr: tt.limitsErr}
			m := NewTranscodeManager()
			m.Sessions = reg
			got := m.ReconstructSession(ctx, tt.card.SessionID, tt.caller, tt.card)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("ReconstructSession = %+v, want refused (nil)", got)
				}
				if _, err := reg.GetSession("a"); errors.Is(tt.limitsErr, ErrTooManyStreams) && err == nil {
					t.Fatal("over-cap reconstruct must not register the session")
				}
				return
			}
			if got == nil {
				t.Fatal("zero caller (UUID-as-bearer route) must reconstruct")
			}
			if got.UserID != 5 {
				t.Fatalf("reconstructed session UserID = %d, want 5 (card owner)", got.UserID)
			}
			if _, err := reg.GetSession("s"); err != nil {
				t.Fatalf("reconstructed session not registered: %v", err)
			}
		})
	}
}

// acquireReconstructSlot must bound concurrent reconstructs and let a caller
// whose request is cancelled give up its place instead of queueing dead work.
func TestAcquireReconstructSlot(t *testing.T) {
	m := &TranscodeManager{reconstructSem: make(chan struct{}, 1)}

	release, ok := m.acquireReconstructSlot(context.Background())
	if !ok {
		t.Fatal("first acquire should succeed")
	}

	// Cap is full: a cancelled request must back off rather than block forever.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := m.acquireReconstructSlot(cancelled); ok {
		t.Fatal("acquire on a full semaphore with a cancelled context must fail")
	}

	// Releasing frees the slot for the next reconstruct.
	release()
	release2, ok := m.acquireReconstructSlot(context.Background())
	if !ok {
		t.Fatal("acquire should succeed after the slot is released")
	}
	release2()
}

func TestReconstructLifecycleWaitDoesNotConsumeGlobalSlot(t *testing.T) {
	m := NewTranscodeManager()
	m.reconstructSem = make(chan struct{}, 1)
	m.Config = func() TranscodeRuntimeConfig { return TranscodeRuntimeConfig{TranscodeDir: t.TempDir()} }
	sessionID := "blocked-reconstruct"
	unlock := m.LockSessionLifecycle(sessionID)
	card := NewRecipeCard(1, "profile", 1, "", TranscodeOpts{
		SessionID: sessionID, InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", SegmentDuration: 2,
	})
	done := make(chan struct{})
	go func() {
		_, _ = m.ReconstructTranscodeWithError(context.Background(), sessionID, -1, card)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		m.lifecycleMu.Lock()
		lock := m.lifecycleLocks[sessionID]
		refs := 0
		if lock != nil {
			refs = lock.refs
		}
		m.lifecycleMu.Unlock()
		if refs == 2 {
			break
		}
		if time.Now().After(deadline) {
			unlock()
			t.Fatal("reconstruct did not begin waiting for the session lifecycle lock")
		}
		time.Sleep(time.Millisecond)
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	slotRelease, available := m.acquireReconstructSlot(probeCtx)
	cancel()
	if available {
		slotRelease()
	}
	// Let the waiter finish before reporting the assertion so no goroutine is
	// left behind on the failure path.
	m.RegisterTranscodeSession(sessionID, &TranscodeSession{})
	unlock()
	<-done
	if !available {
		t.Fatal("session-lock waiter consumed the only global reconstruct slot")
	}
}

func TestLockSessionLifecycle_MutualExclusionAndCleanup(t *testing.T) {
	m := NewTranscodeManager()

	// Two holders of the same key must be mutually exclusive.
	var counter, maxConcurrent int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := m.LockSessionLifecycle("sess-a")
			defer unlock()
			c := atomic.AddInt32(&counter, 1)
			if c > atomic.LoadInt32(&maxConcurrent) {
				atomic.StoreInt32(&maxConcurrent, c)
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt32(&counter, -1)
		}()
	}
	wg.Wait()
	if maxConcurrent != 1 {
		t.Fatalf("lifecycle lock allowed %d concurrent holders, want 1", maxConcurrent)
	}

	// Different keys do not block each other and the map drops entries once
	// released.
	u1 := m.LockSessionLifecycle("k1")
	u2 := m.LockSessionLifecycle("k2")
	m.lifecycleMu.Lock()
	n := len(m.lifecycleLocks)
	m.lifecycleMu.Unlock()
	if n != 2 {
		t.Fatalf("expected 2 live lifecycle locks, got %d", n)
	}
	u1()
	u2()
	m.lifecycleMu.Lock()
	n = len(m.lifecycleLocks)
	m.lifecycleMu.Unlock()
	if n != 0 {
		t.Fatalf("expected lifecycle lock map to drain to 0, got %d", n)
	}
}

func TestReconstructTranscodeWalksAutoPipelineInSameOutputDir(t *testing.T) {
	transcodeDir := t.TempDir()
	m := NewTranscodeManager()
	m.Config = func() TranscodeRuntimeConfig {
		return TranscodeRuntimeConfig{TranscodeDir: transcodeDir, HWAccel: hwAccelAuto}
	}
	m.autoTranscodePipeline = func(_ context.Context, opts TranscodeOpts) *AutoTranscodePipeline {
		if opts.HWAccel != hwAccelAuto {
			t.Errorf("pipeline built from HWAccel %q, want live auto config", opts.HWAccel)
		}
		opts.HWAccel = transcodeHWNVENC
		return newResolvedAutoTranscodePipeline(opts, newAutoTranscodePipelineCache())
	}
	var attempts []TranscodeOpts
	m.startTranscode = func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		attempts = append(attempts, opts)
		if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// The full-hardware attempt exits before its manifest; the mixed one
		// writes into the same directory, which must still exist.
		return fakeStartupSession(t, opts, opts.OutputDir, len(attempts) == 2, false), nil
	}
	card := NewRecipeCard(5, "p", 77, "", TranscodeOpts{
		SessionID: "s", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetResolution: "720p", SegmentDuration: 2,
	})

	got, err := m.ReconstructTranscodeWithError(context.Background(), "s", -1, card)
	if err != nil || got == nil {
		t.Fatalf("reconstruct = %v, %v; want the mixed-stage session", got, err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want full hardware then mixed", len(attempts))
	}
	if attempts[0].SoftwareVideoDecode || !attempts[1].SoftwareVideoDecode || attempts[1].HWAccel != transcodeHWNVENC {
		t.Fatalf("attempt paths = %+v / %+v, want GPU decode then CPU decode with NVENC", attempts[0], attempts[1])
	}
	if live := m.GetTranscodeSession("s"); live != got {
		t.Fatalf("registered session = %p, want %p", live, got)
	}
	if _, statErr := os.Stat(attempts[1].OutputDir); statErr != nil {
		t.Fatalf("reconstruct output dir removed: %v", statErr)
	}
}

func TestReconstructTranscodeWithoutAutoStartsOnceWithoutWaiting(t *testing.T) {
	m := NewTranscodeManager()
	m.Config = func() TranscodeRuntimeConfig {
		return TranscodeRuntimeConfig{TranscodeDir: t.TempDir(), HWAccel: transcodeHWQSV}
	}
	starts := 0
	m.startTranscode = func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		starts++
		// Never ready and already exited: a waiting caller would fail.
		return &TranscodeSession{opts: opts, stderr: newBoundedTailBuffer(stderrTailMaxBytes)}, nil
	}
	card := NewRecipeCard(5, "p", 77, "", TranscodeOpts{
		SessionID: "s", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
	})
	got, err := m.ReconstructTranscodeWithError(context.Background(), "s", -1, card)
	if err != nil || got == nil {
		t.Fatalf("reconstruct = %v, %v; want the single unwaited session", got, err)
	}
	if starts != 1 {
		t.Fatalf("starts = %d, want 1", starts)
	}
}
