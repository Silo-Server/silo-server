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

	session, err := m.StartSession(1, "profile-1", 100, PlayTranscode, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := m.UpdateProgress(session.ID, 42, true); err != nil {
		t.Fatalf("UpdateProgress(paused): %v", err)
	}

	setLastActivity := func(age time.Duration) {
		m.mu.Lock()
		s := m.sessions[session.ID]
		s.LastActivityAt = time.Now().Add(-age)
		s.UpdatedAt = s.LastActivityAt
		m.mu.Unlock()
	}

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
	session, err := m.StartSession(1, "profile-1", 100, PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	setLastActivity := func(age time.Duration) {
		t.Helper()
		m.mu.Lock()
		s := m.sessions[session.ID]
		s.LastActivityAt = time.Now().Add(-age)
		s.UpdatedAt = s.LastActivityAt
		m.mu.Unlock()
	}
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
