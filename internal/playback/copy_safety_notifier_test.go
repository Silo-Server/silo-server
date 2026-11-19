package playback

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

type fakeCopySafetyControl struct {
	mu         sync.Mutex
	remembered []playbackCommandNote
	forgotten  []string
	stopped    []string
}

type playbackCommandNote struct {
	commandID string
	sessionID string
	name      CommandName
}

func (c *fakeCopySafetyControl) RememberRealtimeCommand(commandID, sessionID string, name CommandName) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remembered = append(c.remembered, playbackCommandNote{commandID: commandID, sessionID: sessionID, name: name})
}

func (c *fakeCopySafetyControl) ForgetRealtimeCommand(commandID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forgotten = append(c.forgotten, commandID)
}

func (c *fakeCopySafetyControl) StopSession(_ context.Context, sessionID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = append(c.stopped, sessionID)
	return nil
}

func (c *fakeCopySafetyControl) stoppedSessions() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.stopped...)
}

func (c *fakeCopySafetyControl) trackedCommands() []playbackCommandNote {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]playbackCommandNote(nil), c.remembered...)
}

// fakeAttemptLookup is read by the notifier's deferred goroutines while a test
// is still publishing attempts to it, so it is mutex-guarded and hands out
// copies rather than the stored record.
type fakeAttemptLookup struct {
	mu      sync.Mutex
	records map[string]*AttemptRecordV3
}

func (l *fakeAttemptLookup) GetAttempt(_ context.Context, sessionID string) (*AttemptRecordV3, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	record, ok := l.records[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}
	copied := *record
	return &copied, nil
}

// set publishes an attempt, replacing any previous one. Callers must supply a
// fresh record rather than mutate one they already handed over.
func (l *fakeAttemptLookup) set(sessionID string, record *AttemptRecordV3) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records[sessionID] = record
}

func remuxAttempt(sessionID, planID string, features ...string) *AttemptRecordV3 {
	return &AttemptRecordV3{
		SessionID:     sessionID,
		CurrentPlanID: planID,
		CurrentPlan: PlanV3{
			PlanID:   planID,
			Delivery: DeliveryRemuxHLSV3,
		},
		NormalizedRequest: StartRequestV3{ClientFeatures: features},
	}
}

func newCopySafetyFixture(t *testing.T) (*SessionManager, *RealtimeHub, *CommandTracker, *fakeCopySafetyControl) {
	t.Helper()
	sessions := NewSessionManager(0, 0)
	hub := NewRealtimeHub()
	tracker := NewCommandTracker()
	t.Cleanup(tracker.Close)
	return sessions, hub, tracker, &fakeCopySafetyControl{}
}

// A negotiated, connected client is told to replan; the session keeps playing
// until it reports back.
func TestCopySafetyNotifierPushesPlanInvalidated(t *testing.T) {
	sessions, hub, tracker, control := newCopySafetyFixture(t)
	session := startPlaybackTestSession(t, sessions, 1, "profile-1", 100, PlayRemux)
	conn := connectRealtimeTestConn(t, sessions, hub, session.ID)

	attempts := &fakeAttemptLookup{records: map[string]*AttemptRecordV3{
		session.ID: remuxAttempt(session.ID, "plan-abc", FeaturePlanInvalidatedV3),
	}}
	notifier := NewCopySafetyNotifier(sessions, attempts, NewCommandDispatcher(sessions, hub, tracker), control)

	notifier.VideoCopyUnsafe(context.Background(), 100)

	if len(conn.messages) != 1 {
		t.Fatalf("messages = %d, want 1 plan_invalidated command", len(conn.messages))
	}
	command, ok := conn.messages[0].(CommandEnvelope)
	if !ok {
		t.Fatalf("message type = %T, want CommandEnvelope", conn.messages[0])
	}
	if command.Type != RealtimeMessageTypeCommand || command.Name != CommandPlanInvalidated {
		t.Fatalf("command = %#v, want a plan_invalidated command", command)
	}
	if command.DeadlineMS != int(CopySafetyInvalidationDeadline/time.Millisecond) {
		t.Fatalf("deadline_ms = %d, want %d", command.DeadlineMS, int(CopySafetyInvalidationDeadline/time.Millisecond))
	}
	var payload PlanInvalidatedPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		t.Fatalf("json.Unmarshal(payload): %v", err)
	}
	if payload.Reason != PlanInvalidatedVideoCopyUnsafe || payload.PlanID != "plan-abc" {
		t.Fatalf("payload = %#v, want the invalidated plan and the copy-unsafe reason", payload)
	}
	tracked := control.trackedCommands()
	if len(tracked) != 1 || tracked[0].sessionID != session.ID || tracked[0].name != CommandPlanInvalidated {
		t.Fatalf("tracked commands = %#v, want one plan_invalidated for the session", tracked)
	}
	if tracked[0].commandID != command.CommandID {
		t.Fatalf("tracked command id = %q, want the dispatched %q", tracked[0].commandID, command.CommandID)
	}
	if stopped := control.stoppedSessions(); len(stopped) != 0 {
		t.Fatalf("stopped %v, want the session left running until the client reports back", stopped)
	}
}

// Everything that cannot be told to replan is stopped instead: that fallback is
// the whole backwards-compatibility story for clients shipped before the token.
func TestCopySafetyNotifierStopsSessionsItCannotTell(t *testing.T) {
	tests := []struct {
		name      string
		features  []string
		connected bool
		attempt   bool
	}{
		{name: "feature not negotiated", features: []string{FeatureSeekReanchorV3}, connected: true, attempt: true},
		{name: "no realtime connection", features: []string{FeaturePlanInvalidatedV3}, connected: false, attempt: true},
		{name: "no durable attempt", connected: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sessions, hub, tracker, control := newCopySafetyFixture(t)
			session := startPlaybackTestSession(t, sessions, 1, "profile-1", 100, PlayRemux)
			conn := listenRealtimeTestConn(t, hub, session.ID)
			if tc.connected {
				if err := sessions.SetRealtimeConnection(session.ID, true); err != nil {
					t.Fatalf("SetRealtimeConnection: %v", err)
				}
			}

			attempts := &fakeAttemptLookup{records: map[string]*AttemptRecordV3{}}
			if tc.attempt {
				attempts.records[session.ID] = remuxAttempt(session.ID, "plan-abc", tc.features...)
			}
			notifier := NewCopySafetyNotifier(sessions, attempts, NewCommandDispatcher(sessions, hub, tracker), control)
			notifier.settle = 0

			notifier.VideoCopyUnsafe(context.Background(), 100)

			if len(conn.messages) != 0 {
				t.Fatalf("messages = %d, want no command pushed", len(conn.messages))
			}
			if stopped := control.stoppedSessions(); len(stopped) != 1 || stopped[0] != session.ID {
				t.Fatalf("stopped = %v, want the session terminated", stopped)
			}
		})
	}
}

// A session with no v3 attempt at all (a reconstructed session, or one whose
// attempt expired) is classified by its live play method, and stopped because
// it can be told nothing.
func TestCopySafetyNotifierUsesSessionPlayMethodWithoutAnAttempt(t *testing.T) {
	sessions, hub, tracker, control := newCopySafetyFixture(t)
	remuxing := startPlaybackTestSession(t, sessions, 1, "profile-1", 100, PlayRemux)
	transcoding := startPlaybackTestSession(t, sessions, 2, "profile-2", 100, PlayTranscode)

	notifier := NewCopySafetyNotifier(sessions, nil, NewCommandDispatcher(sessions, hub, tracker), control)
	notifier.settle = 0
	notifier.VideoCopyUnsafe(context.Background(), 100)

	stopped := control.stoppedSessions()
	if len(stopped) != 1 || stopped[0] != remuxing.ID {
		t.Fatalf("stopped = %v, want only the remuxing session %q (not %q)", stopped, remuxing.ID, transcoding.ID)
	}
}

// A session is registered with the manager before its start handler has written
// the attempt record and long before the client has opened a realtime channel.
// A verdict landing in that window must not stop a session that is still being
// built: the notifier waits out the settle window and looks again.
func TestCopySafetyNotifierWaitsOutTheSettleWindowBeforeStopping(t *testing.T) {
	sessions, hub, tracker, control := newCopySafetyFixture(t)
	session := startPlaybackTestSession(t, sessions, 1, "profile-1", 100, PlayRemux)
	conn := listenRealtimeTestConn(t, hub, session.ID)

	attempts := &fakeAttemptLookup{records: map[string]*AttemptRecordV3{}}
	notifier := NewCopySafetyNotifier(sessions, attempts, NewCommandDispatcher(sessions, hub, tracker), control)
	notifier.settle = 60 * time.Millisecond

	notifier.VideoCopyUnsafe(context.Background(), 100)

	if stopped := control.stoppedSessions(); len(stopped) != 0 {
		t.Fatalf("stopped = %v, want the still-establishing session left alone", stopped)
	}

	// The start finishes inside the window: the attempt lands and the client
	// connects, so the second look finds a session it can tell instead of kill.
	attempts.set(session.ID, remuxAttempt(session.ID, "plan-abc", FeaturePlanInvalidatedV3))
	if err := sessions.SetRealtimeConnection(session.ID, true); err != nil {
		t.Fatalf("SetRealtimeConnection: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for len(conn.sent()) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no command pushed after the settle window; stopped = %v", control.stoppedSessions())
		}
		time.Sleep(time.Millisecond)
	}
	sent := conn.sent()
	if command, ok := sent[0].(CommandEnvelope); !ok || command.Name != CommandPlanInvalidated {
		t.Fatalf("message = %#v, want a plan_invalidated command", sent[0])
	}
	if stopped := control.stoppedSessions(); len(stopped) != 0 {
		t.Fatalf("stopped = %v, want the settled session told rather than terminated", stopped)
	}
}

// A session that never becomes reachable is still stopped, just one settle
// window later than an already-settled one.
func TestCopySafetyNotifierStopsAfterTheSettleWindow(t *testing.T) {
	sessions, hub, tracker, control := newCopySafetyFixture(t)
	session := startPlaybackTestSession(t, sessions, 1, "profile-1", 100, PlayRemux)
	notifier := NewCopySafetyNotifier(sessions, nil, NewCommandDispatcher(sessions, hub, tracker), control)
	notifier.settle = 20 * time.Millisecond

	notifier.VideoCopyUnsafe(context.Background(), 100)

	waitForStop(t, control, session.ID)
}

// The command carries a deadline: a client that acks and then goes quiet loses
// its session, so it cannot keep decoding a stream the server withdrew.
func TestCopySafetyNotifierDeadlineStopsUnansweredSession(t *testing.T) {
	sessions, hub, tracker, control := newCopySafetyFixture(t)
	session := startPlaybackTestSession(t, sessions, 1, "profile-1", 100, PlayRemux)
	conn := connectRealtimeTestConn(t, sessions, hub, session.ID)

	attempts := &fakeAttemptLookup{records: map[string]*AttemptRecordV3{
		session.ID: remuxAttempt(session.ID, "plan-abc", FeaturePlanInvalidatedV3),
	}}
	notifier := NewCopySafetyNotifier(sessions, attempts, NewCommandDispatcher(sessions, hub, tracker), control)
	notifier.deadline = 10 * time.Millisecond

	notifier.VideoCopyUnsafe(context.Background(), 100)

	if len(conn.messages) != 1 {
		t.Fatalf("messages = %d, want the command delivered first", len(conn.messages))
	}
	waitForStop(t, control, session.ID)
	// An acked-then-completed command cancels the deadline; here nothing
	// answered, so the tracker must have released the command as well.
	if _, tracked := tracker.Status("unused"); tracked {
		t.Fatal("tracker reported an unknown command as tracked")
	}
}

// Regression for the verdict landing inside a version-changing replan. Between
// applySession and CompleteReplan the live session already names the
// replacement file while the durable attempt still names the previous one, and
// the durable record wins the identity test — so the immediate pass reads the
// session as "serving another file" and does nothing. Marking it seen there
// would strand it: the sweep would skip it, and the now-persisted verdict stops
// any later scan from re-notifying, leaving it remuxing a condemned route for
// the rest of the title.
func TestCopySafetyNotifierSweepsSessionSkippedDuringAReplanCommit(t *testing.T) {
	sessions, hub, tracker, control := newCopySafetyFixture(t)
	// The live session has already been moved onto file 11 by applySession.
	session, err := sessions.StartSessionWithFiles(1, "profile-1", 11, 10, PlayRemux, false)
	if err != nil {
		t.Fatalf("StartSessionWithFiles: %v", err)
	}

	// The durable attempt has not been committed yet, so it still names the
	// file the replan moved off.
	stale := remuxAttempt(session.ID, "plan-old")
	stale.EffectiveMediaFileID = 10
	attempts := &fakeAttemptLookup{records: map[string]*AttemptRecordV3{session.ID: stale}}
	notifier := NewCopySafetyNotifier(sessions, attempts, NewCommandDispatcher(sessions, hub, tracker), control)
	notifier.settle = 20 * time.Millisecond

	// The verdict for the replacement file lands mid-commit.
	notifier.VideoCopyUnsafe(context.Background(), 11)

	if stopped := control.stoppedSessions(); len(stopped) != 0 {
		t.Fatalf("stopped = %v, want nothing while the identities disagree", stopped)
	}

	// CompleteReplan lands: live and durable state now agree on file 11.
	committed := remuxAttempt(session.ID, "plan-new")
	committed.EffectiveMediaFileID = 11
	attempts.set(session.ID, committed)

	waitForStop(t, control, session.ID)
}

// The same hole on the route test rather than the file test: mid-commit the
// durable plan still describes the transcode the replan moved off, so the
// session reads as "not on a copy route" even though it is now remuxing.
func TestCopySafetyNotifierSweepsSessionWhoseDurablePlanLagsTheRoute(t *testing.T) {
	sessions, hub, tracker, control := newCopySafetyFixture(t)
	session := startPlaybackTestSession(t, sessions, 1, "profile-1", 100, PlayRemux)

	stale := remuxAttempt(session.ID, "plan-old")
	stale.CurrentPlan.Delivery = DeliveryTranscodeHLSV3
	attempts := &fakeAttemptLookup{records: map[string]*AttemptRecordV3{session.ID: stale}}
	notifier := NewCopySafetyNotifier(sessions, attempts, NewCommandDispatcher(sessions, hub, tracker), control)
	notifier.settle = 20 * time.Millisecond

	notifier.VideoCopyUnsafe(context.Background(), 100)

	if stopped := control.stoppedSessions(); len(stopped) != 0 {
		t.Fatalf("stopped = %v, want nothing while the durable plan still says transcode", stopped)
	}

	committed := remuxAttempt(session.ID, "plan-new")
	committed.CurrentPlan.Delivery = DeliveryRemuxProgressiveV3
	attempts.set(session.ID, committed)

	waitForStop(t, control, session.ID)
}
