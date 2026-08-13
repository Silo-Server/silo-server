package playback

import (
	"testing"
	"time"
)

// A proxy-served session has no in-flight transport request on this server, so
// the idle sweep is all that stands between a healthy multi-hour stream and
// being reaped mid-playback.
func TestRemoteTransportGraceWidensIdleWindows(t *testing.T) {
	local := &Session{}
	remote := &Session{remoteTransport: true}

	if active, paused := remoteTransportGrace(local, 45*time.Second, 2*time.Minute); active != 45*time.Second || paused != 2*time.Minute {
		t.Fatalf("local grace = (%v, %v), want the configured windows unchanged", active, paused)
	}

	active, paused := remoteTransportGrace(remote, 45*time.Second, 2*time.Minute)
	if active != remoteTransportIdleGrace || paused != remoteTransportIdleGrace {
		t.Fatalf("remote grace = (%v, %v), want both widened to %v", active, paused, remoteTransportIdleGrace)
	}
}

// The widened window must not become immunity: this manager has no absolute
// session lifetime, so a client that disappears without stopping would leak the
// session forever.
func TestRemoteTransportGraceStillExpires(t *testing.T) {
	manager := NewSessionManager(0, 0)
	session, err := manager.StartSession(7, "profile-1", 42, PlayDirect, false)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	if err := manager.SetRemoteTransport(session.ID, true); err != nil {
		t.Fatalf("mark remote transport: %v", err)
	}

	// Backdate the session past the widened window, as an abandoned client would.
	manager.mu.Lock()
	stale := time.Now().Add(-2 * remoteTransportIdleGrace)
	manager.sessions[session.ID].LastActivityAt = stale
	manager.sessions[session.ID].UpdatedAt = stale
	manager.mu.Unlock()

	if expired := manager.CleanInactive(45*time.Second, 2*time.Minute); len(expired) != 1 {
		t.Fatalf("expired %d abandoned proxy sessions, want 1", len(expired))
	}
}

// A session inside the widened window survives a heartbeat gap that would have
// reaped it before, which is the reported failure this guards against.
func TestRemoteTransportSurvivesHeartbeatGap(t *testing.T) {
	manager := NewSessionManager(0, 0)
	session, err := manager.StartSession(7, "profile-1", 42, PlayDirect, false)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	if err := manager.SetRemoteTransport(session.ID, true); err != nil {
		t.Fatalf("mark remote transport: %v", err)
	}

	manager.mu.Lock()
	gap := time.Now().Add(-90 * time.Second) // twice the default active grace
	manager.sessions[session.ID].LastActivityAt = gap
	manager.sessions[session.ID].UpdatedAt = gap
	manager.mu.Unlock()

	if expired := manager.CleanInactive(DefaultActiveSessionGrace, DefaultPausedSessionGrace); len(expired) != 0 {
		t.Fatalf("reaped %d healthy proxy-served sessions during a heartbeat gap", len(expired))
	}
}

// Clearing the mark restores normal reaping, so a re-plan that moves a session
// back onto this server does not leave it with a stale widened grace.
func TestRemoteTransportCanBeCleared(t *testing.T) {
	manager := NewSessionManager(0, 0)
	session, err := manager.StartSession(7, "profile-1", 42, PlayDirect, false)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	if err := manager.SetRemoteTransport(session.ID, true); err != nil {
		t.Fatalf("mark remote transport: %v", err)
	}
	if err := manager.SetRemoteTransport(session.ID, false); err != nil {
		t.Fatalf("clear remote transport: %v", err)
	}

	manager.mu.Lock()
	gap := time.Now().Add(-90 * time.Second)
	manager.sessions[session.ID].LastActivityAt = gap
	manager.sessions[session.ID].UpdatedAt = gap
	manager.mu.Unlock()

	if expired := manager.CleanInactive(DefaultActiveSessionGrace, DefaultPausedSessionGrace); len(expired) != 1 {
		t.Fatalf("expired %d sessions after clearing the mark, want 1", len(expired))
	}
}
