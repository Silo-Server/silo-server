package playback

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesessions"
)

// The remote-transport mark widens the idle window a session gets before it is
// reaped.
func TestRemoteTransportIdleGrace(t *testing.T) {
	for _, tc := range []struct {
		name        string
		clear       bool
		idle        time.Duration
		activeGrace time.Duration
		pausedGrace time.Duration
		wantExpired int
	}{
		// The widened window must not become immunity: this manager has no
		// absolute session lifetime, so a client that disappears without stopping
		// would leak the session forever.
		{name: "grace still expires", idle: 2 * remoteTransportIdleGrace, activeGrace: 45 * time.Second, pausedGrace: 2 * time.Minute, wantExpired: 1},
		// A session inside the widened window survives a heartbeat gap (twice the
		// default active grace) that would have reaped it before, which is the
		// reported failure this guards against.
		{name: "survives heartbeat gap", idle: 90 * time.Second, activeGrace: DefaultActiveSessionGrace, pausedGrace: DefaultPausedSessionGrace},
		// Clearing the mark restores normal reaping, so a re-plan that moves a
		// session back onto this server does not leave it with a stale widened
		// grace.
		{name: "can be cleared", clear: true, idle: 90 * time.Second, activeGrace: DefaultActiveSessionGrace, pausedGrace: DefaultPausedSessionGrace, wantExpired: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewSessionManager(0, 0)
			session := startPlaybackTestSession(t, manager, 7, "profile-1", 42, PlayDirect)
			if err := manager.SetRemoteTransport(session.ID, true); err != nil {
				t.Fatalf("mark remote transport: %v", err)
			}
			if tc.clear {
				if err := manager.SetRemoteTransport(session.ID, false); err != nil {
					t.Fatalf("clear remote transport: %v", err)
				}
			}
			backdatePlaybackTestSession(manager, session.ID, tc.idle)

			if expired := manager.CleanInactive(tc.activeGrace, tc.pausedGrace); len(expired) != tc.wantExpired {
				t.Fatalf("expired %d sessions, want %d", len(expired), tc.wantExpired)
			}
		})
	}
}

// backdatePlaybackTestSession makes a session look as if its client last
// reported age ago.
func backdatePlaybackTestSession(manager *SessionManager, sessionID string, age time.Duration) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	session := manager.sessions[sessionID]
	session.LastActivityAt = time.Now().Add(-age)
	session.UpdatedAt = session.LastActivityAt
	session.PlanCommittedAt = session.LastActivityAt
}

// startAbandonedRemoteSession returns a remote-transport session whose client
// stopped reporting progress long enough ago to be reaped.
func startAbandonedRemoteSession(t *testing.T, manager *SessionManager) *Session {
	t.Helper()
	session := startPlaybackTestSession(t, manager, 7, "profile-1", 42, PlayTranscode)
	if err := manager.SetRemoteTransport(session.ID, true); err != nil {
		t.Fatalf("mark remote transport: %v", err)
	}
	backdatePlaybackTestSession(manager, session.ID, 2*remoteTransportIdleGrace)
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
	local := startPlaybackTestSession(t, manager, 8, "profile-2", 43, PlayDirect)
	active := startPlaybackTestSession(t, manager, 9, "profile-3", 44, PlayTranscode)
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
	session := startPlaybackTestSession(t, manager, 7, "profile-1", 42, PlayTranscode)
	if err := manager.SetRemoteTransport(session.ID, true); err != nil {
		t.Fatalf("mark remote transport: %v", err)
	}
	backdatePlaybackTestSession(manager, session.ID, remoteTransportIdleGrace/2)
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
