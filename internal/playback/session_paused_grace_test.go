package playback

import (
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
