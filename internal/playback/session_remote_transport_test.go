package playback

import (
	"context"
	"errors"
	"math"
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

// A session's own IdleGrace replaces both default windows: a player that
// only reports progress and sends no stop ends its play after that long, even
// when its last report was paused.
func TestSessionIdleGraceReplacesDefaults(t *testing.T) {
	for _, tc := range []struct {
		name        string
		idle        time.Duration
		paused      bool
		wantExpired int
	}{
		// Past the 45s active default, inside the session's own window.
		{name: "survives report gap", idle: 60 * time.Second},
		{name: "expires after its grace", idle: 100 * time.Second, wantExpired: 1},
		// Well inside the 30-minute paused default, past the session's own.
		{name: "paused expires after its grace", idle: 100 * time.Second, paused: true, wantExpired: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewSessionManager(0, 0)
			manager.RegisterReconstructed(&Session{ID: "reported-1", UserID: 7, IsPaused: tc.paused, IdleGrace: 90 * time.Second})
			backdatePlaybackTestSession(manager, "reported-1", tc.idle)

			if expired := manager.CleanInactive(DefaultActiveSessionGrace, DefaultPausedSessionGrace); len(expired) != tc.wantExpired {
				t.Fatalf("expired %d sessions, want %d", len(expired), tc.wantExpired)
			}
		})
	}
}

// A report-only play may be a downloaded copy, so it never takes a stream
// slot from the account.
func TestReportOnlySessionDoesNotCountTowardLimits(t *testing.T) {
	manager := NewSessionManager(1, 0)
	manager.RegisterReconstructed(&Session{ID: "reported-1", UserID: 7, ReportOnly: true})
	if _, err := manager.StartSessionWithFiles(7, "profile-1", 42, 42, PlayDirect, false); err != nil {
		t.Fatalf("start a stream beside a report-only play: %v", err)
	}
}

// Report-only sessions are outside stream limits, so a per-account cap bounds
// them; a session already live under the ID is not refused.
func TestRegisterReportOnlyCapsPerAccount(t *testing.T) {
	manager := NewSessionManager(0, 0)
	for _, tc := range []struct {
		id     string
		userID int
		want   bool
	}{
		{"a", 7, true},
		{"b", 7, true},
		{"c", 7, false},
		{"a", 7, true},
		{"d", 8, true},
	} {
		if got := manager.RegisterReportOnly(&Session{ID: tc.id, UserID: tc.userID}, 2); got != tc.want {
			t.Errorf("register %s for user %d = %v, want %v", tc.id, tc.userID, got, tc.want)
		}
	}
}

// A client that reports a running listening total is credited how far it
// rose past the highest total seen, so a late, lower report credits nothing.
// A client that never reports a total is measured from its position.
func TestUpdateProgressReported(t *testing.T) {
	for _, tc := range []struct {
		name         string
		reports      [][2]float64 // position, total
		wantPlayed   float64
		wantReported float64
	}{
		{name: "late lower total", reports: [][2]float64{{100, 10}, {100, 5}}, wantPlayed: 10, wantReported: 10},
		{name: "no total", reports: [][2]float64{{110, 0}}, wantPlayed: 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewSessionManager(0, 0)
			started := time.Now().Add(-10 * time.Second)
			var watch WatchTime
			watch.Observe(100, false, started)
			manager.RegisterReconstructed(&Session{ID: "reported-1", UserID: 7, StartedAt: started, Watch: watch})
			for _, r := range tc.reports {
				if err := manager.UpdateProgressReported("reported-1", r[0], r[1]); err != nil {
					t.Fatalf("report: %v", err)
				}
			}
			s, err := manager.GetSession("reported-1")
			if err != nil {
				t.Fatalf("get session: %v", err)
			}
			if math.Abs(s.Watch.PlayedSeconds-tc.wantPlayed) > 0.01 || s.ReportedListening != tc.wantReported {
				t.Fatalf("played = %v, reported = %v, want %v and %v", s.Watch.PlayedSeconds, s.ReportedListening, tc.wantPlayed, tc.wantReported)
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
