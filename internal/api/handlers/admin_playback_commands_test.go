package handlers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

const (
	sequencedCommandA = "11111111-1111-4111-8111-111111111111"
	sequencedCommandB = "22222222-2222-4222-8222-222222222222"
	sequencedCommandC = "33333333-3333-4333-8333-333333333333"
)

func sequencedInput(sessionID, commandID string, sequence int64, name playback.CommandName) AdminPlaybackCommandInput {
	return AdminPlaybackCommandInput{SessionID: sessionID, CommandID: commandID, Sequence: sequence, Name: name, ActorID: 2, DeadlineMS: 10}
}

// A live control lane so pause and resume are admitted.
func sequencedLane(t *testing.T, control *AdminPlaybackControlHandler, hub *playback.RealtimeHub, session *playback.Session) *adminPlaybackControlTestConn {
	t.Helper()
	conn := &adminPlaybackControlTestConn{}
	registration := hub.Register(session.ID, conn)
	if registration == nil {
		t.Fatal("expected realtime registration")
	}
	t.Cleanup(func() { hub.Unregister(registration) })
	if !control.playback.setRealtimeConnectionState(session.ID, true) {
		t.Fatal("expected the session to accept a realtime connection state")
	}
	return conn
}

func TestSequencedCommandAppliesOnceAndReplays(t *testing.T) {
	control, _, hub, session := newAdminPlaybackControlTestHandler(t)
	conn := sequencedLane(t, control, hub, session)
	ctx := context.Background()

	first, err := control.Command(ctx, sequencedInput(session.ID, sequencedCommandA, 1, playback.CommandPause))
	if err != nil || first.Outcome != AdminPlaybackCommandApplied || first.Delivery != AdminPlaybackDeliveryDispatched || first.Sequence != 1 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	if len(conn.messages) != 1 {
		t.Fatalf("dispatched %d commands, want 1", len(conn.messages))
	}
	env, ok := conn.messages[0].(playback.CommandEnvelope)
	if !ok || env.CommandID != sequencedCommandA || env.Name != playback.CommandPause || env.IssuedBy == nil || env.IssuedBy.Kind != "admin" {
		t.Fatalf("envelope = %#v", conn.messages[0])
	}

	// The identical identity and content is a replay: nothing is sent again.
	replay, err := control.Command(ctx, sequencedInput(session.ID, sequencedCommandA, 1, playback.CommandPause))
	if err != nil || replay.Outcome != AdminPlaybackCommandReplayed || replay.Delivery != AdminPlaybackDeliveryDispatched {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if len(conn.messages) != 1 {
		t.Fatalf("replay dispatched: %d commands", len(conn.messages))
	}

	// The same command_id with different content is an idempotency conflict.
	for _, changed := range []AdminPlaybackCommandInput{
		sequencedInput(session.ID, sequencedCommandA, 2, playback.CommandPause),
		sequencedInput(session.ID, sequencedCommandA, 1, playback.CommandUnpause),
		func() AdminPlaybackCommandInput {
			in := sequencedInput(session.ID, sequencedCommandA, 1, playback.CommandPause)
			in.ActorID = 3
			return in
		}(),
		func() AdminPlaybackCommandInput {
			in := sequencedInput(session.ID, sequencedCommandA, 1, playback.CommandPause)
			in.Reason = "different"
			return in
		}(),
	} {
		if _, err := control.Command(ctx, changed); !errors.Is(err, ErrAdminPlaybackCommandConflict) {
			t.Fatalf("changed %+v err = %v, want conflict", changed, err)
		}
	}
	if len(conn.messages) != 1 {
		t.Fatalf("conflict dispatched: %d commands", len(conn.messages))
	}
}

func TestSequencedCommandRefusesStaleOrder(t *testing.T) {
	control, _, hub, session := newAdminPlaybackControlTestHandler(t)
	conn := sequencedLane(t, control, hub, session)
	ctx := context.Background()

	// pause(5) then resume(9): the delayed pause retry with a new identity at
	// sequence 5 (or anything at or below 9) must not revert the resume.
	if _, err := control.Command(ctx, sequencedInput(session.ID, sequencedCommandA, 5, playback.CommandPause)); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Command(ctx, sequencedInput(session.ID, sequencedCommandB, 9, playback.CommandUnpause)); err != nil {
		t.Fatal(err)
	}
	for _, stale := range []int64{5, 9, 1} {
		_, err := control.Command(ctx, sequencedInput(session.ID, sequencedCommandC, stale, playback.CommandPause))
		if !errors.Is(err, ErrAdminPlaybackCommandStale) {
			t.Fatalf("sequence %d err = %v, want stale", stale, err)
		}
		// The refusal names the sequence that won, so an administrator whose
		// allocation runs behind another's can reissue above it.
		var refusal *AdminPlaybackCommandStaleError
		if !errors.As(err, &refusal) || refusal.Latest != 9 {
			t.Fatalf("sequence %d refusal = %#v, want latest 9", stale, err)
		}
	}
	// Reissued above the reported latest, the same intent is applied.
	if _, err := control.Command(ctx, sequencedInput(session.ID, sequencedCommandC, 10, playback.CommandPause)); err != nil {
		t.Fatalf("reissue above latest: %v", err)
	}
	// The original pause identity still replays its own receipt without dispatch.
	replay, err := control.Command(ctx, sequencedInput(session.ID, sequencedCommandA, 5, playback.CommandPause))
	if err != nil || replay.Outcome != AdminPlaybackCommandReplayed {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if len(conn.messages) != 3 {
		t.Fatalf("dispatched %d commands, want exactly pause, resume and the reissued pause", len(conn.messages))
	}
	latest, receipts, ok := control.commandLedgerState(session.ID)
	if !ok || latest != 10 || receipts != 3 {
		t.Fatalf("ledger = latest %d receipts %d ok %v", latest, receipts, ok)
	}
}

func TestSequencedCommandConcurrentDuplicatesDispatchOnce(t *testing.T) {
	control, _, hub, session := newAdminPlaybackControlTestHandler(t)
	conn := sequencedLane(t, control, hub, session)
	ctx := context.Background()

	const callers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	outcomes := map[string]int{}
	for range callers {
		wg.Go(func() {
			<-start
			view, err := control.Command(ctx, sequencedInput(session.ID, sequencedCommandA, 3, playback.CommandPause))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				outcomes["error:"+err.Error()]++
				return
			}
			// Every receipt, replayed or applied, carries the completed delivery.
			if view.Delivery != AdminPlaybackDeliveryDispatched || view.CommandID != sequencedCommandA || view.Sequence != 3 {
				outcomes["incomplete:"+view.Outcome+"/"+view.Delivery]++
				return
			}
			outcomes[view.Outcome]++
		})
	}
	close(start)
	wg.Wait()
	if outcomes[AdminPlaybackCommandApplied] != 1 || outcomes[AdminPlaybackCommandReplayed] != callers-1 || len(outcomes) != 2 {
		t.Fatalf("outcomes = %v", outcomes)
	}
	if len(conn.messages) != 1 {
		t.Fatalf("dispatched %d commands, want 1", len(conn.messages))
	}
}

func TestSequencedCommandValidationAndSessionState(t *testing.T) {
	control, sessionMgr, hub, session := newAdminPlaybackControlTestHandler(t)
	ctx := context.Background()

	// Pause needs a hello-ready control lane; refusal records nothing.
	if _, err := control.Command(ctx, sequencedInput(session.ID, sequencedCommandA, 1, playback.CommandPause)); !errors.Is(err, ErrAdminPlaybackRealtimeRequired) {
		t.Fatalf("pause without lane err = %v", err)
	}
	if _, _, ok := control.commandLedgerState(session.ID); ok {
		if latest, receipts, _ := control.commandLedgerState(session.ID); latest != 0 || receipts != 0 {
			t.Fatalf("refused pause recorded latest %d receipts %d", latest, receipts)
		}
	}
	for name, in := range map[string]AdminPlaybackCommandInput{
		"non-canonical id":                sequencedInput(session.ID, "not-a-uuid", 1, playback.CommandPause),
		"uppercase id":                    sequencedInput(session.ID, "11111111-1111-4111-8111-11111111111A", 1, playback.CommandPause),
		"zero sequence":                   sequencedInput(session.ID, sequencedCommandA, 0, playback.CommandPause),
		"sequence above the shared bound": sequencedInput(session.ID, sequencedCommandA, AdminPlaybackCommandMaxSequence+1, playback.CommandPause),
		"no actor": func() AdminPlaybackCommandInput {
			in := sequencedInput(session.ID, sequencedCommandA, 1, playback.CommandPause)
			in.ActorID = 0
			return in
		}(),
		"terminate is not a sequenced action": sequencedInput(session.ID, sequencedCommandA, 1, playback.CommandTerminate),
		"empty message":                       sequencedInput(session.ID, sequencedCommandA, 1, playback.CommandDisplayMessage),
	} {
		if _, err := control.Command(ctx, in); !errors.Is(err, ErrAdminPlaybackCommandInvalid) {
			t.Fatalf("%s err = %v, want invalid", name, err)
		}
	}
	if _, err := control.Command(ctx, sequencedInput("missing", sequencedCommandA, 1, playback.CommandStop)); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("missing session err = %v", err)
	}

	// Message without a lane is refused (the v1 409); with one it is dispatched
	// once with the payload and never falls back to stopping the session.
	message := sequencedInput(session.ID, sequencedCommandB, 2, playback.CommandDisplayMessage)
	message.Title, message.Message = "Heads up", "Movie night ends at nine"
	if _, err := control.Command(ctx, message); !errors.Is(err, ErrAdminPlaybackRealtimeRequired) {
		t.Fatalf("message without lane err = %v", err)
	}
	conn := sequencedLane(t, control, hub, session)
	view, err := control.Command(ctx, message)
	if err != nil || view.Outcome != AdminPlaybackCommandApplied || view.Delivery != AdminPlaybackDeliveryDispatched {
		t.Fatalf("message = %+v, %v", view, err)
	}
	env, ok := conn.messages[len(conn.messages)-1].(playback.CommandEnvelope)
	if !ok || env.Name != playback.CommandDisplayMessage || string(env.Payload) != `{"message":"Movie night ends at nine","title":"Heads up"}` {
		t.Fatalf("message envelope = %#v", conn.messages[len(conn.messages)-1])
	}
	if _, err := sessionMgr.GetSession(session.ID); err != nil {
		t.Fatalf("message must not end the session: %v", err)
	}
}

func TestSequencedStopFallsBackWithoutLaneAndDropsLedger(t *testing.T) {
	control, sessionMgr, _, session := newAdminPlaybackControlTestHandler(t)
	ctx := context.Background()

	view, err := control.Command(ctx, sequencedInput(session.ID, sequencedCommandA, 4, playback.CommandStop))
	if err != nil || view.Outcome != AdminPlaybackCommandApplied || view.Delivery != AdminPlaybackDeliveryFallbackScheduled {
		t.Fatalf("stop = %+v, %v", view, err)
	}
	// The receipt survives until the session is gone, then is dropped with it.
	replay, err := control.Command(ctx, sequencedInput(session.ID, sequencedCommandA, 4, playback.CommandStop))
	if err != nil || replay.Outcome != AdminPlaybackCommandReplayed || replay.Delivery != AdminPlaybackDeliveryFallbackScheduled {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	waitForPlaybackSessionMissing(t, sessionMgr, session.ID)
	// The fallback that ended the session drops its ledger itself; no later
	// command is needed to reclaim it.
	waitForCommandLedgerGone(t, control, session.ID)
	if _, err := control.Command(ctx, sequencedInput(session.ID, sequencedCommandA, 4, playback.CommandStop)); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("after fallback err = %v", err)
	}
	if _, _, ok := control.commandLedgerState(session.ID); ok {
		t.Fatal("ledger survived the ended session")
	}
}

// A session that ends behind the command path's back between lookup and
// admission cannot be given a fresh ledger: the lookup happens under the
// ledger lock, so a missing session drops the ledger and answers not found.
func TestSequencedCommandOnEndedSessionLeavesNoLedger(t *testing.T) {
	control, sessionMgr, _, session := newAdminPlaybackControlTestHandler(t)
	ctx := context.Background()
	in := sequencedInput(session.ID, sequencedCommandA, 1, playback.CommandStop)
	in.DeadlineMS = int(maxPlaybackControlDeadline / time.Millisecond)
	if _, err := control.Command(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := sessionMgr.StopSession(session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Command(ctx, sequencedInput(session.ID, sequencedCommandB, 2, playback.CommandStop)); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
	if got := control.commandLedgerCount(); got != 0 {
		t.Fatalf("ledgers = %d, want 0", got)
	}
}

func waitForCommandLedgerGone(t *testing.T, control *AdminPlaybackControlHandler, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, _, ok := control.commandLedgerState(sessionID); !ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("ledger for %q was not dropped with the session", sessionID)
}

// A session that ends by any route the command path does not see (a client
// stop, a liveness reap) must not leave its ledger behind for the process
// lifetime: the next command on any session sweeps ended ledgers once per
// interval.
func TestSequencedLedgerSweepsEndedSessions(t *testing.T) {
	control, sessionMgr, _, session := newAdminPlaybackControlTestHandler(t)
	ctx := context.Background()
	in := sequencedInput(session.ID, sequencedCommandA, 1, playback.CommandStop)
	in.DeadlineMS = int(maxPlaybackControlDeadline / time.Millisecond)
	if _, err := control.Command(ctx, in); err != nil {
		t.Fatal(err)
	}
	other, err := sessionMgr.StartSession(1, "profile-1", 101, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	in = sequencedInput(other.ID, sequencedCommandB, 1, playback.CommandStop)
	in.DeadlineMS = int(maxPlaybackControlDeadline / time.Millisecond)
	if _, err := control.Command(ctx, in); err != nil {
		t.Fatal(err)
	}
	if got := control.commandLedgerCount(); got != 2 {
		t.Fatalf("ledgers = %d, want 2", got)
	}

	// The first session ends behind the command path's back.
	if err := sessionMgr.StopSession(session.ID); err != nil {
		t.Fatal(err)
	}
	// Within the interval nothing is swept; the commanded session is untouched.
	in = sequencedInput(other.ID, sequencedCommandC, 2, playback.CommandStop)
	in.DeadlineMS = int(maxPlaybackControlDeadline / time.Millisecond)
	if _, err := control.Command(ctx, in); err != nil {
		t.Fatal(err)
	}
	if got := control.commandLedgerCount(); got != 2 {
		t.Fatalf("ledgers after throttled command = %d, want 2", got)
	}
	// Once the interval has elapsed the next command prunes the ended session.
	control.commandMu.Lock()
	control.commandSweptAt = time.Now().Add(-2 * adminPlaybackLedgerSweepInterval)
	control.commandMu.Unlock()
	if _, err := control.Command(ctx, sequencedInput(other.ID, sequencedCommandC, 2, playback.CommandStop)); err != nil {
		t.Fatal(err)
	}
	if got := control.commandLedgerCount(); got != 1 {
		t.Fatalf("ledgers after sweep = %d, want 1", got)
	}
	if _, _, ok := control.commandLedgerState(other.ID); !ok {
		t.Fatal("the live session's ledger was swept")
	}
}

func TestSequencedLedgerIsBounded(t *testing.T) {
	control, _, _, session := newAdminPlaybackControlTestHandler(t)
	ctx := context.Background()
	for i := range adminPlaybackCommandLedgerLimit + 8 {
		id := "44444444-4444-4444-8444-" + padCommandSuffix(i)
		in := sequencedInput(session.ID, id, int64(i+1), playback.CommandStop)
		in.DeadlineMS = int(maxPlaybackControlDeadline / time.Millisecond)
		if _, err := control.Command(ctx, in); err != nil {
			t.Fatalf("command %d: %v", i, err)
		}
	}
	latest, receipts, ok := control.commandLedgerState(session.ID)
	if !ok || receipts != adminPlaybackCommandLedgerLimit || latest != adminPlaybackCommandLedgerLimit+8 {
		t.Fatalf("ledger = latest %d receipts %d ok %v", latest, receipts, ok)
	}
}

func padCommandSuffix(i int) string {
	const digits = "0123456789ab"
	out := make([]byte, 12)
	for pos := 11; pos >= 0; pos-- {
		out[pos] = digits[i%10]
		i /= 10
	}
	return string(out)
}
