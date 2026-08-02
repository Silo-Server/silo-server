package playback

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type blockingGenerationTombstoneStore struct {
	mu                     sync.Mutex
	ended                  map[string]time.Time
	writeStarted           chan struct{}
	releaseWrite           chan struct{}
	reconstructLockAttempt chan struct{}
	startOnce              sync.Once
	reconstructOnce        sync.Once
}

func newBlockingGenerationTombstoneStore() *blockingGenerationTombstoneStore {
	return &blockingGenerationTombstoneStore{
		ended:                  make(map[string]time.Time),
		writeStarted:           make(chan struct{}),
		releaseWrite:           make(chan struct{}),
		reconstructLockAttempt: make(chan struct{}),
	}
}

func (s *blockingGenerationTombstoneStore) signalReconstructLockAttempt() {
	s.reconstructOnce.Do(func() { close(s.reconstructLockAttempt) })
}

func (s *blockingGenerationTombstoneStore) WasSessionGenerationEnded(_ context.Context, sessionID, generation string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	expiresAt, ok := s.ended[sessionID+"\x00"+generation]
	return ok && expiresAt.After(now), nil
}

func (s *blockingGenerationTombstoneStore) RecordEndedSessionGeneration(_ context.Context, sessionID, generation string, expiresAt time.Time) error {
	s.startOnce.Do(func() { close(s.writeStarted) })
	<-s.releaseWrite
	s.mu.Lock()
	s.ended[sessionID+"\x00"+generation] = expiresAt
	s.mu.Unlock()
	return nil
}

func runBlockedEndVersusReconstruct(
	t *testing.T,
	end func(*SessionManager) <-chan error,
) {
	t.Helper()
	store := newBlockingGenerationTombstoneStore()
	mgr := NewSessionManager(0, 0)
	mgr.SetSessionGenerationTombstoneStore(store)
	started, err := mgr.StartSession(1, "profile", 1, PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	endDone := end(mgr)
	<-store.writeStarted
	mgr.sessionLifecycleWaitHook = func(sessionID string) {
		if sessionID == started.ID {
			store.signalReconstructLockAttempt()
		}
	}
	reconstructDone := make(chan *Session, 1)
	reconstructErr := make(chan error, 1)
	go func() {
		got, gotErr := mgr.RegisterReconstructedChecked(t.Context(), &Session{
			ID: started.ID, Generation: started.Generation, StartedAt: started.StartedAt,
		})
		reconstructDone <- got
		reconstructErr <- gotErr
	}()
	<-store.reconstructLockAttempt
	select {
	case got := <-reconstructDone:
		t.Fatalf("reconstruction returned while end owned the lifecycle lock: %+v", got)
	default:
	}

	close(store.releaseWrite)
	if err := <-endDone; err != nil {
		t.Fatalf("end session: %v", err)
	}
	got := <-reconstructDone
	if err := <-reconstructErr; err != nil {
		t.Fatalf("RegisterReconstructedChecked: %v", err)
	}
	if got == nil || got.Generation == started.Generation {
		t.Fatalf("reconstructed generation = %v, want fresh identity", got)
	}
}

func TestConcurrentStopAndReconstructCannotReinsertEndedGeneration(t *testing.T) {
	runBlockedEndVersusReconstruct(t, func(mgr *SessionManager) <-chan error {
		done := make(chan error, 1)
		go func() { done <- mgr.StopSession(mgr.AllSessions()[0].ID) }()
		return done
	})
}

func TestConcurrentExpiryAndReconstructCannotReinsertEndedGeneration(t *testing.T) {
	runBlockedEndVersusReconstruct(t, func(mgr *SessionManager) <-chan error {
		done := make(chan error, 1)
		go func() {
			expired := mgr.CleanInactive(0, 0)
			if len(expired) != 1 {
				done <- &unexpectedExpiredCountError{got: len(expired)}
				return
			}
			done <- nil
		}()
		return done
	})
}

func TestTerminateSessionGenerationAbsentIsIdempotentAndRerunsFinalizer(t *testing.T) {
	mgr := NewSessionManager(0, 0)
	const sessionID = "restart-session"
	const generation = "58d591a3-c011-481d-b837-b2c9c8032b9a"
	finalizerCalls := 0
	finalizer := func() error {
		finalizerCalls++
		return nil
	}

	if err := mgr.TerminateSessionGeneration(t.Context(), sessionID, generation, finalizer); err != nil {
		t.Fatalf("first absent termination: %v", err)
	}
	if err := mgr.TerminateSessionGeneration(t.Context(), sessionID, generation, finalizer); err != nil {
		t.Fatalf("idempotent absent termination: %v", err)
	}
	if finalizerCalls != 2 {
		t.Fatalf("finalizer calls = %d, want 2", finalizerCalls)
	}
	if ended, err := mgr.wasSessionGenerationEnded(t.Context(), sessionID, generation, time.Now().UTC()); err != nil || !ended {
		t.Fatalf("ended generation = %v, err=%v; want recorded", ended, err)
	}
}

func TestTerminateSessionGenerationPreservesNewerLiveGeneration(t *testing.T) {
	mgr := NewSessionManager(0, 0)
	const sessionID = "reused-session"
	const oldGeneration = "13565a39-5cfa-41dc-9683-c5f59589b722"
	const newGeneration = "79e77106-bd71-483f-985c-b6e28a92c80f"
	if got, err := mgr.RegisterReconstructedChecked(t.Context(), &Session{
		ID: sessionID, Generation: newGeneration, StartedAt: time.Now().UTC(),
	}); err != nil || got == nil {
		t.Fatalf("register newer generation: got=%+v err=%v", got, err)
	}
	finalizerCalls := 0
	if err := mgr.TerminateSessionGeneration(t.Context(), sessionID, oldGeneration, func() error {
		finalizerCalls++
		return nil
	}); err != nil {
		t.Fatalf("terminate old generation: %v", err)
	}
	current, err := mgr.GetSession(sessionID)
	if err != nil || current.Generation != newGeneration {
		t.Fatalf("newer live session = %+v, err=%v; want generation %s", current, err, newGeneration)
	}
	if finalizerCalls != 1 {
		t.Fatalf("finalizer calls = %d, want 1", finalizerCalls)
	}
}

func TestTerminateSessionGenerationFinalizerFailureIsRetryableAndMutexFree(t *testing.T) {
	mgr := NewSessionManager(0, 0)
	const sessionID = "finalizer-retry-session"
	const generation = "ad39bd83-5f8a-4a8f-946a-4d44b01b32c7"
	wantErr := errors.New("finalizer unavailable")
	finalizerCalls := 0
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- mgr.TerminateSessionGeneration(t.Context(), sessionID, generation, func() error {
			finalizerCalls++
			_ = mgr.AllSessions()
			return wantErr
		})
	}()
	select {
	case err := <-firstDone:
		if !errors.Is(err, wantErr) {
			t.Fatalf("first finalizer error = %v, want %v", err, wantErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("finalizer blocked on SessionManager global mutex")
	}
	if err := mgr.TerminateSessionGeneration(t.Context(), sessionID, generation, func() error {
		finalizerCalls++
		return nil
	}); err != nil {
		t.Fatalf("retry termination: %v", err)
	}
	if finalizerCalls != 2 {
		t.Fatalf("finalizer calls = %d, want 2", finalizerCalls)
	}
}

func TestTerminateSessionGenerationBlocksReconstructionThroughFinalizer(t *testing.T) {
	mgr := NewSessionManager(0, 0)
	const sessionID = "guarded-finalizer-session"
	const oldGeneration = "34d30fba-b8fc-4220-8884-7d3587c29eae"
	const newGeneration = "744c0105-3a13-447d-89e5-dd9f44ad6367"
	if got, err := mgr.RegisterReconstructedChecked(t.Context(), &Session{
		ID: sessionID, Generation: oldGeneration, StartedAt: time.Now().UTC().Add(-time.Minute),
	}); err != nil || got == nil {
		t.Fatalf("register old generation: got=%+v err=%v", got, err)
	}

	finalizerStarted := make(chan struct{})
	releaseFinalizer := make(chan struct{})
	terminationDone := make(chan error, 1)
	go func() {
		terminationDone <- mgr.TerminateSessionGeneration(t.Context(), sessionID, oldGeneration, func() error {
			close(finalizerStarted)
			<-releaseFinalizer
			return nil
		})
	}()
	<-finalizerStarted

	reconstructAttempted := make(chan struct{})
	mgr.sessionLifecycleWaitHook = func(id string) {
		if id == sessionID {
			select {
			case <-reconstructAttempted:
			default:
				close(reconstructAttempted)
			}
		}
	}
	reconstructDone := make(chan *Session, 1)
	reconstructErr := make(chan error, 1)
	go func() {
		got, err := mgr.RegisterReconstructedChecked(t.Context(), &Session{
			ID: sessionID, Generation: newGeneration, StartedAt: time.Now().UTC(),
		})
		reconstructDone <- got
		reconstructErr <- err
	}()
	<-reconstructAttempted
	select {
	case got := <-reconstructDone:
		t.Fatalf("reconstruction installed during guarded finalizer: %+v", got)
	default:
	}

	close(releaseFinalizer)
	if err := <-terminationDone; err != nil {
		t.Fatalf("termination: %v", err)
	}
	got := <-reconstructDone
	if err := <-reconstructErr; err != nil {
		t.Fatalf("reconstruct newer generation: %v", err)
	}
	if got == nil || got.Generation != newGeneration {
		t.Fatalf("reconstructed session = %+v, want newer generation %s", got, newGeneration)
	}
}

func TestExactGenerationNativeMutationsPreserveReplacement(t *testing.T) {
	mgr := NewSessionManager(0, 0)
	if got, err := mgr.RegisterReconstructedChecked(t.Context(), &Session{
		ID: "shared", Generation: "g2", TranscodeNodeURL: "g2-node",
		TargetVideoCodec: "hevc", TargetAudioCodec: "copy", Position: 12, AudioTrackIndex: 3,
		activeTransportCount: 2,
	}); err != nil || got == nil {
		t.Fatalf("register G2: got=%+v err=%v", got, err)
	}
	if err := mgr.SetTranscodeNodeURLGeneration("shared", "g1", "g1-node"); !errors.Is(err, ErrSessionSuperseded) {
		t.Fatalf("stale node mutation = %v, want superseded", err)
	}
	if err := mgr.SetTranscodeStreamDetailsGeneration("shared", "g1", "h264", "aac", true); !errors.Is(err, ErrSessionSuperseded) {
		t.Fatalf("stale details mutation = %v, want superseded", err)
	}
	if err := mgr.UpdateProgressGeneration("shared", "g1", 99, true); !errors.Is(err, ErrSessionSuperseded) {
		t.Fatalf("stale progress mutation = %v, want superseded", err)
	}
	if err := mgr.UpdateAudioTrackGeneration("shared", "g1", 9, PlayTranscode); !errors.Is(err, ErrSessionSuperseded) {
		t.Fatalf("stale audio mutation = %v, want superseded", err)
	}
	if err := mgr.BeginTransportGeneration("shared", "g1"); !errors.Is(err, ErrSessionSuperseded) {
		t.Fatalf("stale begin transport = %v, want superseded", err)
	}
	if err := mgr.EndTransportGeneration("shared", "g1"); !errors.Is(err, ErrSessionSuperseded) {
		t.Fatalf("stale end transport = %v, want superseded", err)
	}
	live, err := mgr.GetSession("shared")
	if err != nil || live.Generation != "g2" || live.TranscodeNodeURL != "g2-node" || live.TargetVideoCodec != "hevc" || live.TargetAudioCodec != "copy" || live.TranscodeAudio || live.Position != 12 || live.IsPaused || live.AudioTrackIndex != 3 || live.activeTransportCount != 2 {
		t.Fatalf("stale exact mutation changed G2: live=%+v err=%v", live, err)
	}
}

func TestSessionLifecycleGenerationMatchesLegacyEmptyExactly(t *testing.T) {
	for _, method := range []PlayMethod{PlayDirect, PlayRemux, PlayTranscode} {
		t.Run(string(method), func(t *testing.T) {
			mgr := NewSessionManager(0, 0)
			if got, err := mgr.RegisterReconstructedChecked(t.Context(), &Session{
				ID: "legacy", Generation: "", PlayMethod: method, BasePlayMethod: method,
			}); err != nil || got == nil || got.Generation != "" {
				t.Fatalf("register legacy session: got=%+v err=%v", got, err)
			}
			called := false
			err := mgr.WithSessionGeneration(t.Context(), "legacy", "", func() error {
				called = true
				return mgr.BeginTransportGeneration("legacy", "")
			})
			if err != nil || !called {
				t.Fatalf("legacy empty lifecycle called=%v err=%v", called, err)
			}
		})
	}

	t.Run("empty caller rejects G2", func(t *testing.T) {
		mgr := NewSessionManager(0, 0)
		if got, err := mgr.RegisterReconstructedChecked(t.Context(), &Session{
			ID: "shared", Generation: "g2", TranscodeNodeURL: "g2-node",
		}); err != nil || got == nil {
			t.Fatalf("register G2: got=%+v err=%v", got, err)
		}
		called := false
		err := mgr.WithSessionGeneration(t.Context(), "shared", "", func() error {
			called = true
			return nil
		})
		if !errors.Is(err, ErrSessionSuperseded) || called {
			t.Fatalf("empty caller vs G2 called=%v err=%v, want superseded without callback", called, err)
		}
		live, getErr := mgr.GetSession("shared")
		if getErr != nil || live.Generation != "g2" || live.TranscodeNodeURL != "g2-node" {
			t.Fatalf("empty caller changed G2: live=%+v err=%v", live, getErr)
		}
	})

	t.Run("nonempty caller rejects legacy empty", func(t *testing.T) {
		mgr := NewSessionManager(0, 0)
		if got, err := mgr.RegisterReconstructedChecked(t.Context(), &Session{ID: "legacy", Generation: ""}); err != nil || got == nil {
			t.Fatalf("register legacy: got=%+v err=%v", got, err)
		}
		called := false
		err := mgr.WithSessionGeneration(t.Context(), "legacy", "g2", func() error {
			called = true
			return nil
		})
		if !errors.Is(err, ErrSessionSuperseded) || called {
			t.Fatalf("G2 caller vs empty called=%v err=%v, want superseded without callback", called, err)
		}
	})
}

type unexpectedExpiredCountError struct{ got int }

func (e *unexpectedExpiredCountError) Error() string { return "unexpected expired session count" }
