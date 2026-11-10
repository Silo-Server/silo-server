package playback

import (
	"fmt"
	"testing"
	"time"
)

// TestPausedSessionSurvivesIntentionalPause covers issue #243 symptom (a):
// a paused session whose client stops reporting progress (backgrounded tab,
// slept device, tvOS pause) must not be reaped after a few minutes — reaping
// kills the ffmpeg transcode and there is no revival path, so pressing Play
// after a >5 minute pause freezes the client. An intentional pause must
// survive well beyond the old 2-minute grace; truly abandoned sessions are
// still reaped once the (now longer) paused grace elapses.
func TestPausedSessionSurvivesIntentionalPause(t *testing.T) {
	m := NewSessionManager(0, 0)

	session := startPlaybackTestSession(t, m, 1, "profile-1", 100, PlayTranscode)
	if err := m.UpdateProgress(session.ID, 42, true); err != nil {
		t.Fatalf("UpdateProgress(paused): %v", err)
	}

	setLastActivity := func(age time.Duration) { backdatePlaybackTestSession(m, session.ID, age) }

	// Paused for 10 minutes: must survive.
	setLastActivity(10 * time.Minute)
	m.CleanStale()
	if _, err := m.GetSession(session.ID); err != nil {
		t.Fatalf("session reaped after 10-minute pause; paused grace must allow intentional pauses (err: %v)", err)
	}

	// Abandoned well past the paused grace: must still be reaped.
	setLastActivity(DefaultPausedSessionGrace + time.Minute)
	m.CleanStale()
	if _, err := m.GetSession(session.ID); err == nil {
		t.Fatal("session survived past the paused grace; abandoned sessions must still be reaped")
	}
}

func TestSequentialRangedTransportsSurviveIdleAndPausedGrace(t *testing.T) {
	m := NewSessionManager(0, 0)
	session := startPlaybackTestSession(t, m, 1, "profile-1", 100, PlayDirect)

	setLastActivity := func(age time.Duration) { backdatePlaybackTestSession(m, session.ID, age) }
	assertPresent := func(stage string) {
		t.Helper()
		if _, err := m.GetSession(session.ID); err != nil {
			t.Fatalf("%s: session was cleaned: %v", stage, err)
		}
	}

	const activeGrace = 2 * time.Minute
	for cycle := 1; cycle <= 3; cycle++ {
		setLastActivity(activeGrace / 2)
		m.CleanInactive(activeGrace, DefaultPausedSessionGrace)
		assertPresent(fmt.Sprintf("idle gap before transport %d", cycle))

		if err := m.BeginTransport(session.ID); err != nil {
			t.Fatalf("BeginTransport(%d): %v", cycle, err)
		}
		setLastActivity(activeGrace + time.Minute)
		m.CleanInactive(activeGrace, DefaultPausedSessionGrace)
		assertPresent(fmt.Sprintf("active transport %d", cycle))
		if err := m.EndTransport(session.ID); err != nil {
			t.Fatalf("EndTransport(%d): %v", cycle, err)
		}
	}

	if err := m.UpdateProgress(session.ID, 42, true); err != nil {
		t.Fatalf("UpdateProgress(paused): %v", err)
	}
	setLastActivity(DefaultPausedSessionGrace - time.Minute)
	m.CleanInactive(activeGrace, DefaultPausedSessionGrace)
	assertPresent("late paused idle gap")

	if err := m.BeginTransport(session.ID); err != nil {
		t.Fatalf("BeginTransport(late range): %v", err)
	}
	setLastActivity(DefaultPausedSessionGrace + time.Minute)
	m.CleanInactive(activeGrace, DefaultPausedSessionGrace)
	assertPresent("late ranged transport active")
	if err := m.EndTransport(session.ID); err != nil {
		t.Fatalf("EndTransport(late range): %v", err)
	}
	assertPresent("completed ranged transport sequence")
}

// TestStopReportedPausedSessionKeepsPausedGrace is the #1454 review
// regression: a stale ID-less stop marks a paused session, and with no later
// heartbeat that session must still get the paused grace, not the active one,
// so idle cleanup can't tear down a really-paused play.
func TestStopReportedPausedSessionKeepsPausedGrace(t *testing.T) {
	m := NewSessionManager(0, 0)
	session := startPlaybackTestSession(t, m, 1, "profile-1", 100, PlayDirect)
	if err := m.UpdateProgress(session.ID, 42, true); err != nil {
		t.Fatalf("UpdateProgress(paused): %v", err)
	}
	if err := m.MarkStopReported(session.ID); err != nil {
		t.Fatalf("MarkStopReported: %v", err)
	}

	backdatePlaybackTestSession(m, session.ID, 46*time.Second)
	m.CleanStale()

	got, err := m.GetSession(session.ID)
	if err != nil {
		t.Fatalf("stop-reported paused session reaped after 46s idle; it must keep the paused grace (err: %v)", err)
	}
	if !got.IsPaused || !got.StopReported {
		t.Fatalf("IsPaused=%v StopReported=%v, want both true", got.IsPaused, got.StopReported)
	}
	if err := m.UpdateProgress(session.ID, 43, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.GetSession(session.ID); got.StopReported {
		t.Fatal("a progress report must clear the stop mark")
	}
}

// A replacement stream (and its rollback) is an active play, so it clears a
// stop mark rather than leaving the play hidden from the admin view.
func TestReplacementClearsStopReported(t *testing.T) {
	m := NewSessionManager(0, 0)
	session := startPlaybackTestSession(t, m, 1, "profile-1", 100, PlayDirect)
	if err := m.MarkStopReported(session.ID); err != nil {
		t.Fatal(err)
	}
	rollback, err := m.ApplyReplacement(session.ID, SessionReplacement{EffectiveMediaFileID: 100})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := m.GetSession(session.ID); got.StopReported {
		t.Fatal("ApplyReplacement kept the stop mark")
	}

	if err := m.MarkStopReported(session.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.RollbackReplacement(session.ID, rollback); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.GetSession(session.ID); got.StopReported {
		t.Fatal("RollbackReplacement kept the stop mark")
	}
}

// agePlaybackTestPlan makes the session's current plan, and its last activity,
// age old: the client received the plan then and has been quiet since.
func agePlaybackTestPlan(manager *SessionManager, sessionID string, age time.Duration) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	session := manager.sessions[sessionID]
	session.PlanCommittedAt = time.Now().Add(-age)
	session.LastActivityAt = session.PlanCommittedAt
	session.UpdatedAt = session.PlanCommittedAt
}

// A client that cannot start a plan sends no progress, because its player has
// no position yet, and asks for the next fallback only when its startup
// timeout (60s on the web) runs out. The session must outlive that wait
// (issue #3202), the wait restarts with each replacement plan, and a startup
// the client abandoned is still reaped.
func TestSessionSurvivesClientStartupWindow(t *testing.T) {
	m := NewSessionManager(0, 0)
	session := startPlaybackTestSession(t, m, 1, "profile-1", 100, PlayDirect)
	assertPresent := func(stage string) {
		t.Helper()
		if _, err := m.GetSession(session.ID); err != nil {
			t.Fatalf("%s: session was reaped: %v", stage, err)
		}
	}
	// idleFor ages only the activity, leaving the plan's own timestamp.
	idleFor := func(age time.Duration) {
		m.mu.Lock()
		defer m.mu.Unlock()
		s := m.sessions[session.ID]
		s.LastActivityAt = time.Now().Add(-age)
		s.UpdatedAt = s.LastActivityAt
	}

	agePlaybackTestPlan(m, session.ID, 61*time.Second)
	m.CleanStale()
	assertPresent("first plan past the web startup timeout")

	// The fallback replaces a plan that is already past its own window.
	agePlaybackTestPlan(m, session.ID, StartupSessionGrace+time.Second)
	if _, err := m.ApplyReplacement(session.ID, SessionReplacement{EffectiveMediaFileID: 100}); err != nil {
		t.Fatal(err)
	}
	idleFor(61 * time.Second)
	m.CleanStale()
	assertPresent("fallback plan past the web startup timeout")

	agePlaybackTestPlan(m, session.ID, StartupSessionGrace+time.Second)
	m.CleanStale()
	if _, err := m.GetSession(session.ID); err == nil {
		t.Fatal("session survived past the startup grace; an abandoned startup must still be reaped")
	}

	// A replica that lost the session rebuilds it mid-startup; the client is
	// still waiting out its timeout.
	m.RegisterReconstructed(&Session{ID: session.ID, UserID: 1, ProfileID: "profile-1", MediaFileID: 100, PlayMethod: PlayDirect})
	idleFor(61 * time.Second)
	m.CleanStale()
	assertPresent("reconstructed plan past the web startup timeout")

	// A replan that is rolled back handed the client no plan, so it must not
	// restart the window.
	agePlaybackTestPlan(m, session.ID, StartupSessionGrace+time.Second)
	rollback, err := m.ApplyReplacement(session.ID, SessionReplacement{EffectiveMediaFileID: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RollbackReplacement(session.ID, rollback); err != nil {
		t.Fatal(err)
	}
	idleFor(61 * time.Second)
	m.CleanStale()
	if _, err := m.GetSession(session.ID); err == nil {
		t.Fatal("a rolled-back replan restarted the startup window")
	}
}
