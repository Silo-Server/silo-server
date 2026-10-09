package playback

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesessions"
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

// startAbandonedRemoteSession returns a remote-transport session whose client
// stopped reporting progress long enough ago to be reaped.
func startAbandonedRemoteSession(t *testing.T, manager *SessionManager) *Session {
	t.Helper()
	session, err := manager.StartSession(7, "profile-1", 42, PlayTranscode, false)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	if err := manager.SetRemoteTransport(session.ID, true); err != nil {
		t.Fatalf("mark remote transport: %v", err)
	}
	manager.mu.Lock()
	stale := time.Now().Add(-2 * remoteTransportIdleGrace)
	manager.sessions[session.ID].LastActivityAt = stale
	manager.sessions[session.ID].UpdatedAt = stale
	manager.mu.Unlock()
	return session
}

// A Cast receiver keeps pulling media from the proxy after its sender phone
// sleeps and stops reporting progress. The node's delivery record must keep the
// session alive.
func TestRemoteTransportSurvivesWhileNodeDeliversMedia(t *testing.T) {
	manager := NewSessionManager(0, 0)
	session := startAbandonedRemoteSession(t, manager)
	delivered := time.Now().Add(-10 * time.Second)
	manager.SetDeliveryActivityReader(func(_ context.Context, sessions []Session) (map[string]time.Time, error) {
		return map[string]time.Time{sessions[0].ID: delivered}, nil
	})

	if expired := manager.CleanInactive(DefaultActiveSessionGrace, DefaultPausedSessionGrace); len(expired) != 0 {
		t.Fatalf("reaped %d sessions whose node is still delivering media", len(expired))
	}
	got, err := manager.GetSession(session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if !got.LastActivityAt.Equal(delivered) {
		t.Fatalf("last activity = %v, want the delivery time %v", got.LastActivityAt, delivered)
	}
}

// Delivery activity is not immunity: once the node stops serving media, the
// session expires like any abandoned one.
func TestRemoteTransportExpiresAfterNodeDeliveryStops(t *testing.T) {
	manager := NewSessionManager(0, 0)
	startAbandonedRemoteSession(t, manager)
	manager.SetDeliveryActivityReader(func(context.Context, []Session) (map[string]time.Time, error) {
		return nil, nil
	})

	if expired := manager.CleanInactive(DefaultActiveSessionGrace, DefaultPausedSessionGrace); len(expired) != 1 {
		t.Fatalf("expired %d sessions with no delivery or progress, want 1", len(expired))
	}
}

// A failed lookup falls back to progress reports alone rather than holding
// every remote session open while Redis is unavailable.
func TestRemoteTransportDeliveryLookupFailureDoesNotProtect(t *testing.T) {
	manager := NewSessionManager(0, 0)
	startAbandonedRemoteSession(t, manager)
	manager.SetDeliveryActivityReader(func(context.Context, []Session) (map[string]time.Time, error) {
		return nil, errors.New("redis unavailable")
	})

	if expired := manager.CleanInactive(DefaultActiveSessionGrace, DefaultPausedSessionGrace); len(expired) != 1 {
		t.Fatalf("expired %d sessions after a failed delivery lookup, want 1", len(expired))
	}
}

// Every remote session is looked up on each sweep, including ones that still
// look active, so none can go idle between the lookup and the expiry check. A
// locally served session has its own transport accounting and is never asked.
func TestDeliveryLookupAsksAboutEveryRemoteSession(t *testing.T) {
	manager := NewSessionManager(0, 0)
	abandoned := startAbandonedRemoteSession(t, manager)
	local, err := manager.StartSession(8, "profile-2", 43, PlayDirect, false)
	if err != nil {
		t.Fatalf("start local session: %v", err)
	}
	active, err := manager.StartSession(9, "profile-3", 44, PlayTranscode, false)
	if err != nil {
		t.Fatalf("start active session: %v", err)
	}
	if err := manager.SetRemoteTransport(active.ID, true); err != nil {
		t.Fatalf("mark remote transport: %v", err)
	}

	asked := make(map[string]bool)
	manager.SetDeliveryActivityReader(func(_ context.Context, sessions []Session) (map[string]time.Time, error) {
		for _, s := range sessions {
			asked[s.ID] = true
		}
		return nil, nil
	})
	manager.CleanInactive(DefaultActiveSessionGrace, DefaultPausedSessionGrace)

	if !asked[abandoned.ID] || !asked[active.ID] || asked[local.ID] || len(asked) != 2 {
		t.Fatalf("delivery lookup asked about %v, want both remote sessions (%s, %s) and not the local one", asked, abandoned.ID, active.ID)
	}
}

// Delivery is merged before a session looks idle, so a stream that only its
// node vouches for keeps fresh activity between sweeps. Otherwise it would drop
// out of the stream-limit count until the next sweep revived it, and a paused
// session's longer grace could outlast the record that should extend it.
func TestDeliveryRefreshesRemoteSessionsBeforeTheyLookIdle(t *testing.T) {
	manager := NewSessionManager(0, 0)
	session, err := manager.StartSession(7, "profile-1", 42, PlayTranscode, false)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	if err := manager.SetRemoteTransport(session.ID, true); err != nil {
		t.Fatalf("mark remote transport: %v", err)
	}
	quiet := time.Now().Add(-remoteTransportIdleGrace / 2)
	manager.mu.Lock()
	manager.sessions[session.ID].LastActivityAt = quiet
	manager.sessions[session.ID].UpdatedAt = quiet
	manager.mu.Unlock()
	delivered := time.Now().Add(-5 * time.Second)
	manager.SetDeliveryActivityReader(func(_ context.Context, sessions []Session) (map[string]time.Time, error) {
		return map[string]time.Time{session.ID: delivered}, nil
	})

	if expired := manager.CleanInactive(DefaultActiveSessionGrace, DefaultPausedSessionGrace); len(expired) != 0 {
		t.Fatalf("expired %d sessions, want none", len(expired))
	}
	got, err := manager.GetSession(session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if !got.LastActivityAt.Equal(delivered) {
		t.Fatalf("last activity = %v, want the delivery time %v", got.LastActivityAt, delivered)
	}
}

// A delivery record has to be readable at whichever sweep first runs after the
// last delivery, even a late one or one that follows failed lookups; otherwise
// the session ends before one grace has passed since its media stopped.
// Records outliving the grace covers every such sweep.
func TestNodeDeliveryRecordsOutliveRemoteTransportGrace(t *testing.T) {
	if nodesessions.DeliveryWindow <= remoteTransportIdleGrace {
		t.Fatalf("delivery records live %v, want longer than the remote-transport grace %v", nodesessions.DeliveryWindow, remoteTransportIdleGrace)
	}
}
