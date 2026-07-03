package playback

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSessionRegistry is a GetSession + RegisterReconstructed double.
type fakeSessionRegistry struct {
	sessions map[string]*Session
	// maxPerUser, when > 0, caps reconstructs per user via
	// RegisterReconstructedWithLimits so the admission path can be tested.
	maxPerUser int
	// limitsErr, when non-nil, is returned by RegisterReconstructedWithLimits to
	// simulate a limit-provider failure (e.g. a transient Postgres error). It
	// takes precedence over the over-cap check so the fail-open admission path
	// can be exercised. A real SessionManager surfaces such failures wrapped with
	// ErrLimitProviderUnavailable.
	limitsErr error
}

func (f *fakeSessionRegistry) GetSession(id string) (*Session, error) {
	if s, ok := f.sessions[id]; ok {
		return s, nil
	}
	return nil, ErrSessionNotFound
}

func (f *fakeSessionRegistry) RegisterReconstructed(s *Session) *Session {
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
// maxPerUser, when > 0, caps how many sessions a single user may reconstruct so
// the admission-rejection path can be exercised without a real SessionManager.
func (f *fakeSessionRegistry) RegisterReconstructedWithLimits(_ context.Context, s *Session) (*Session, error) {
	if f.sessions == nil {
		f.sessions = map[string]*Session{}
	}
	if existing, ok := f.sessions[s.ID]; ok {
		return existing, nil
	}
	if f.limitsErr != nil {
		return nil, f.limitsErr
	}
	if f.maxPerUser > 0 {
		live := 0
		for _, existing := range f.sessions {
			if existing.UserID == s.UserID {
				live++
			}
		}
		if live >= f.maxPerUser {
			return nil, ErrTooManyStreams
		}
	}
	f.sessions[s.ID] = s
	return s, nil
}

// CloseTranscodeSession stops the live session and drops it from the transcode
// map. Under token-carried reconstruction there is no durable card to delete; the
// segment dir is reaped later by liveness+age cleanup.
func TestCloseTranscodeSession_DropsLiveSession(t *testing.T) {
	m := NewTranscodeManager()
	m.RegisterTranscodeSession("s1", &TranscodeSession{})
	m.CloseTranscodeSession("s1", "")
	if got := m.GetTranscodeSession("s1"); got != nil {
		t.Fatal("session must be removed from the live map on close")
	}
}

func TestLoadOrReconstructSession(t *testing.T) {
	ctx := context.Background()

	newMgr := func(reg *fakeSessionRegistry) *TranscodeManager {
		m := NewTranscodeManager()
		m.Sessions = reg
		return m
	}
	cardPtr := func(c RecipeCard) *RecipeCard { return &c }

	t.Run("live session, matching owner -> loaded", func(t *testing.T) {
		reg := &fakeSessionRegistry{sessions: map[string]*Session{"s": {ID: "s", UserID: 5}}}
		m := newMgr(reg)
		got, status := m.LoadOrReconstructSession(ctx, reg.GetSession, "s", 5, nil)
		if status != SessionLoaded || got == nil || got.ID != "s" {
			t.Fatalf("got status=%v session=%+v", status, got)
		}
	})

	t.Run("live session, mismatched owner -> forbidden", func(t *testing.T) {
		reg := &fakeSessionRegistry{sessions: map[string]*Session{"s": {ID: "s", UserID: 5}}}
		m := newMgr(reg)
		if _, status := m.LoadOrReconstructSession(ctx, reg.GetSession, "s", 9, nil); status != SessionForbidden {
			t.Fatalf("status = %v, want forbidden", status)
		}
	})

	t.Run("live session, zero caller -> loaded (UUID as bearer)", func(t *testing.T) {
		reg := &fakeSessionRegistry{sessions: map[string]*Session{"s": {ID: "s", UserID: 5}}}
		m := newMgr(reg)
		if _, status := m.LoadOrReconstructSession(ctx, reg.GetSession, "s", 0, nil); status != SessionLoaded {
			t.Fatalf("status = %v, want loaded", status)
		}
	})

	t.Run("miss + remux token + matching owner -> reconstructed with method", func(t *testing.T) {
		reg := &fakeSessionRegistry{}
		m := newMgr(reg)
		card := NewRemuxRecipeCard("s", 5, "p", 77, true, 2)
		got, status := m.LoadOrReconstructSession(ctx, reg.GetSession, "s", 5, cardPtr(card))
		if status != SessionLoaded || got == nil {
			t.Fatalf("status=%v session=%+v", status, got)
		}
		if got.PlayMethod != PlayRemux || got.MediaFileID != 77 || !got.TranscodeAudio || got.AudioTrackIndex != 2 {
			t.Fatalf("reconstructed remux session wrong: %+v", got)
		}
		if _, err := reg.GetSession("s"); err != nil {
			t.Fatalf("reconstructed session not registered: %v", err)
		}
	})

	t.Run("miss + token + mismatched owner -> missing (reconstruct refuses)", func(t *testing.T) {
		reg := &fakeSessionRegistry{}
		m := newMgr(reg)
		card := NewDirectRecipeCard("s", 5, "p", 77)
		if _, status := m.LoadOrReconstructSession(ctx, reg.GetSession, "s", 9, cardPtr(card)); status != SessionMissing {
			t.Fatalf("status = %v, want missing", status)
		}
	})

	t.Run("miss + token for a different session id -> missing", func(t *testing.T) {
		reg := &fakeSessionRegistry{}
		m := newMgr(reg)
		card := NewDirectRecipeCard("other", 5, "p", 77)
		if _, status := m.LoadOrReconstructSession(ctx, reg.GetSession, "s", 5, cardPtr(card)); status != SessionMissing {
			t.Fatalf("status = %v, want missing (card session id mismatch)", status)
		}
	})

	t.Run("miss + no token -> missing", func(t *testing.T) {
		reg := &fakeSessionRegistry{}
		m := newMgr(reg)
		if _, status := m.LoadOrReconstructSession(ctx, reg.GetSession, "nope", 5, nil); status != SessionMissing {
			t.Fatalf("status = %v, want missing", status)
		}
	})
}

// CloseTranscodeSessionIf must leave a successor registered under the same id
// untouched: a reconstruct that replaced the crashed ffmpeg between exit and
// teardown must not have its live session (and shared output dir) torn down.
func TestCloseTranscodeSessionIf_LeavesSuccessor(t *testing.T) {
	m := NewTranscodeManager()
	dead := &TranscodeSession{}
	successor := &TranscodeSession{}

	// The map now holds the successor (the reconstruct won the race), not dead.
	m.RegisterTranscodeSession("s1", successor)

	if matched := m.CloseTranscodeSessionIf("s1", dead, ""); matched {
		t.Fatalf("CloseTranscodeSessionIf must report false when a successor holds the slot")
	}

	if got := m.GetTranscodeSession("s1"); got != successor {
		t.Fatalf("successor must survive a crash teardown for the dead session, got %v", got)
	}
}

// CloseTranscodeSessionIf must remove the entry when it is still the exact
// session that died (the ordinary crash case with no successor).
func TestCloseTranscodeSessionIf_RemovesMatching(t *testing.T) {
	m := NewTranscodeManager()
	dead := &TranscodeSession{}
	m.RegisterTranscodeSession("s1", dead)

	if matched := m.CloseTranscodeSessionIf("s1", dead, ""); !matched {
		t.Fatalf("CloseTranscodeSessionIf must report true when the dead session still holds the slot")
	}

	if got := m.GetTranscodeSession("s1"); got != nil {
		t.Fatalf("matching dead session must be removed, got %v", got)
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

// ReconstructSession must refuse to rebuild a session when the user is already at
// their per-user concurrency cap (token replay over-cap), while still allowing
// reconstructs up to the cap.
func TestReconstructSession_AdmissionCap(t *testing.T) {
	ctx := context.Background()
	reg := &fakeSessionRegistry{maxPerUser: 1}
	m := NewTranscodeManager()
	m.Sessions = reg

	// First reconstruct for the user admits.
	card1 := NewDirectRecipeCard("a", 7, "p", 100)
	if got := m.ReconstructSession(ctx, "a", 7, card1); got == nil {
		t.Fatal("first reconstruct within cap should succeed")
	}
	// Second distinct session for the same user is over cap -> refused.
	card2 := NewDirectRecipeCard("b", 7, "p", 101)
	if got := m.ReconstructSession(ctx, "b", 7, card2); got != nil {
		t.Fatal("over-cap reconstruct must be refused")
	}
}

// A transient limit-PROVIDER failure during reconstruct (e.g. a Postgres error
// in the post-restart wave) must NOT collapse into a permanent 404. The session
// must be admitted (fail open) so a user within their limits keeps playing.
func TestReconstructSession_ProviderErrorFailsOpen(t *testing.T) {
	ctx := context.Background()
	reg := &fakeSessionRegistry{
		limitsErr: fmt.Errorf("load session limits for user 7: %w",
			errors.Join(ErrLimitProviderUnavailable, errors.New("db timeout"))),
	}
	m := NewTranscodeManager()
	m.Sessions = reg

	card := NewDirectRecipeCard("a", 7, "p", 100)
	got := m.ReconstructSession(ctx, "a", 7, card)
	if got == nil {
		t.Fatal("limit-provider error must fail open and admit the reconstructed session, not refuse")
	}
	if got.ID != "a" || got.UserID != 7 {
		t.Fatalf("admitted session wrong: %+v", got)
	}
	// The fail-open path must register the session so LoadOrReconstructSession
	// yields SessionLoaded, not SessionMissing.
	if _, err := reg.GetSession("a"); err != nil {
		t.Fatalf("failed-open session not registered: %v", err)
	}
}

// LoadOrReconstructSession must surface the fail-open admission as SessionLoaded
// (not SessionMissing -> 404) when the limit provider is transiently unavailable.
func TestLoadOrReconstructSession_ProviderErrorFailsOpen(t *testing.T) {
	ctx := context.Background()
	reg := &fakeSessionRegistry{
		limitsErr: errors.Join(ErrLimitProviderUnavailable, errors.New("db timeout")),
	}
	m := NewTranscodeManager()
	m.Sessions = reg

	card := NewDirectRecipeCard("s", 5, "p", 77)
	got, status := m.LoadOrReconstructSession(ctx, reg.GetSession, "s", 5, &card)
	if status != SessionLoaded || got == nil {
		t.Fatalf("provider error must yield SessionLoaded, got status=%v session=%+v", status, got)
	}
}

// A genuine over-cap rejection must STILL refuse even after the fail-open change:
// the ErrTooManyStreams / ErrTooManyTranscodes sentinels are not provider errors.
func TestReconstructSession_OverCapStillRefused(t *testing.T) {
	ctx := context.Background()

	t.Run("too many streams", func(t *testing.T) {
		reg := &fakeSessionRegistry{limitsErr: ErrTooManyStreams}
		m := NewTranscodeManager()
		m.Sessions = reg
		card := NewDirectRecipeCard("a", 7, "p", 100)
		if got := m.ReconstructSession(ctx, "a", 7, card); got != nil {
			t.Fatal("ErrTooManyStreams over-cap must still be refused (nil)")
		}
		if _, err := reg.GetSession("a"); err == nil {
			t.Fatal("over-cap reconstruct must not register the session")
		}
	})

	t.Run("too many transcodes", func(t *testing.T) {
		reg := &fakeSessionRegistry{limitsErr: ErrTooManyTranscodes}
		m := NewTranscodeManager()
		m.Sessions = reg
		card := NewDirectRecipeCard("a", 7, "p", 100)
		if got := m.ReconstructSession(ctx, "a", 7, card); got != nil {
			t.Fatal("ErrTooManyTranscodes over-cap must still be refused (nil)")
		}
	})
}

// ReconstructSession ownership contract: the authless transcode delivery routes
// (HLS master.m3u8 / segment) present no userID, so a zero caller must be allowed
// and the rebuilt session bound to the card owner. A non-zero caller that does not
// match the card owner is still refused.
func TestReconstructSession_Ownership(t *testing.T) {
	ctx := context.Background()

	t.Run("zero caller -> reconstructed, bound to card owner", func(t *testing.T) {
		reg := &fakeSessionRegistry{}
		m := NewTranscodeManager()
		m.Sessions = reg

		card := NewDirectRecipeCard("s", 5, "p", 77)
		got := m.ReconstructSession(ctx, "s", 0, card)
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

	t.Run("non-zero mismatched caller -> refused", func(t *testing.T) {
		reg := &fakeSessionRegistry{}
		m := NewTranscodeManager()
		m.Sessions = reg

		card := NewDirectRecipeCard("s", 5, "p", 77)
		if got := m.ReconstructSession(ctx, "s", 9, card); got != nil {
			t.Fatal("non-zero caller mismatching the card owner must be refused")
		}
	})
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
