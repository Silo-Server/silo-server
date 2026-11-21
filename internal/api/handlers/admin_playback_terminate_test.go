package handlers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// TestAdminTerminateStopsThenNotifies drives the v2 terminate against a live
// session: the session is stopped first, the client is notified only when a
// lane is open, and a repeat on the now-unknown session (one without an
// attempt row, as on the bridge) is 404.
func TestAdminTerminateStopsThenNotifies(t *testing.T) {
	for _, lane := range []string{"offline", "online"} {
		t.Run(lane, func(t *testing.T) {
			control, sessionMgr, hub, session := newAdminPlaybackControlTestHandler(t)

			var conn *adminPlaybackControlTestConn
			if lane == "online" {
				conn = &adminPlaybackControlTestConn{}
				registration := hub.Register(session.ID, conn)
				if registration == nil {
					t.Fatal("register lane")
				}
				t.Cleanup(func() { hub.Unregister(registration) })
			}

			view, err := control.Terminate(t.Context(), AdminTerminateInput{SessionID: session.ID, ActorID: 2, Reason: "policy"})
			if err != nil {
				t.Fatal(err)
			}
			if !view.AuthorityRevoked || view.AlreadyRevoked || view.DurableState != AdminTerminateDurableStopped {
				t.Fatalf("view = %+v", view)
			}
			if lane == "online" {
				if !view.ClientNotified || view.Delivery != AdminTerminateDeliveryDispatched || len(conn.messages) != 1 {
					t.Fatalf("online view = %+v messages=%d", view, len(conn.messages))
				}
				env, ok := conn.messages[0].(playback.CommandEnvelope)
				if !ok || env.Name != playback.CommandTerminate || env.CommandID != view.CommandID || env.Reason != "policy" || env.IssuedBy == nil || env.IssuedBy.Kind != "admin" {
					t.Fatalf("envelope = %#v", conn.messages[0])
				}
			} else if view.ClientNotified || view.Delivery != AdminTerminateDeliveryUnavailable {
				t.Fatalf("offline view = %+v", view)
			}
			if _, err := sessionMgr.GetSession(session.ID); !errors.Is(err, playback.ErrSessionNotFound) {
				t.Fatalf("session survived terminate: %v", err)
			}
			// Nothing per session is retained once the terminate returns.
			if got := control.terminateLockCount(); got != 0 {
				t.Fatalf("terminate locks retained = %d", got)
			}

			// The session is gone: a repeat is unknown, exactly as the bridge
			// answers, and nothing is re-dispatched.
			if _, err := control.Terminate(t.Context(), AdminTerminateInput{SessionID: session.ID, ActorID: 2}); !errors.Is(err, playback.ErrSessionNotFound) {
				t.Fatalf("repeat err = %v", err)
			}
			// The durable attempt is stopped too, so no replica accepts
			// progress for it afterwards.
			if store, ok := control.playback.PlanStoreV3.(playback.ProgressStoreV3); ok {
				if _, err := store.ApplyProgress(t.Context(), session.ID, playback.ProgressSampleV3{Sequence: 1, Position: 5}); !errors.Is(err, playback.ErrAttemptStoppedV3) && !errors.Is(err, playback.ErrSessionNotFound) {
					t.Fatalf("progress after terminate = %v, want stopped attempt", err)
				}
			}
			if conn != nil && len(conn.messages) != 1 {
				t.Fatalf("repeat dispatched: %d messages", len(conn.messages))
			}
			waitForPlaybackSessionMissing(t, sessionMgr, session.ID)
		})
	}
}

// TestAdminTerminateErrors covers the unknown-session 404, the invalid actor,
// and the unwired handler.
// TestAdminTerminateDropsCommandLedger: the sequenced command ledger of a
// terminated session goes with it rather than waiting for a later command.
func TestAdminTerminateDropsCommandLedger(t *testing.T) {
	control, _, _, session := newAdminPlaybackControlTestHandler(t)
	in := sequencedInput(session.ID, sequencedCommandA, 1, playback.CommandStop)
	in.DeadlineMS = int(maxPlaybackControlDeadline / time.Millisecond)
	if _, err := control.Command(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := control.commandLedgerState(session.ID); !ok {
		t.Fatal("expected a ledger before terminate")
	}
	if _, err := control.Terminate(t.Context(), AdminTerminateInput{SessionID: session.ID, ActorID: 2}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := control.commandLedgerState(session.ID); ok {
		t.Fatal("ledger survived terminate")
	}
}

// TestAdminTerminateLockIsReleasedUnderContention: concurrent terminates for
// one session still serialize, and the lock entry is gone once the last
// waiter returns.
func TestAdminTerminateLockIsReleasedUnderContention(t *testing.T) {
	control, _, _, session := newAdminPlaybackControlTestHandler(t)
	const waiters = 8
	var wg sync.WaitGroup
	results := make(chan error, waiters)
	for range waiters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := control.Terminate(context.Background(), AdminTerminateInput{SessionID: session.ID, ActorID: 2})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, playback.ErrSessionNotFound):
		default:
			t.Fatalf("terminate err = %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("terminates that revoked = %d, want exactly 1", succeeded)
	}
	if got := control.terminateLockCount(); got != 0 {
		t.Fatalf("terminate locks retained = %d", got)
	}
}

func TestAdminTerminateErrors(t *testing.T) {
	control, _, _, session := newAdminPlaybackControlTestHandler(t)
	if _, err := control.Terminate(context.Background(), AdminTerminateInput{SessionID: "missing", ActorID: 2}); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("missing err = %v", err)
	}
	if _, err := control.Terminate(context.Background(), AdminTerminateInput{SessionID: session.ID, ActorID: 0}); !errors.Is(err, ErrAdminPlaybackCommandInvalid) {
		t.Fatalf("no actor err = %v", err)
	}
	var unwired *AdminPlaybackControlHandler
	if _, err := unwired.Terminate(context.Background(), AdminTerminateInput{SessionID: session.ID, ActorID: 2}); !errors.Is(err, ErrAdminTerminateUnavailable) {
		t.Fatalf("unwired err = %v", err)
	}
}

// TestAdminTerminateRevokesAnAttemptHeldElsewhere: the load balancer can route
// an administrator's terminate to a replica that does not hold the live
// session. The durable attempt is still stopped and denied, so the owning
// replica's copy is reaped by the marker instead of the request failing.
func TestAdminTerminateRevokesAnAttemptHeldElsewhere(t *testing.T) {
	control, _, _, _ := newAdminPlaybackControlTestHandler(t)
	remote := uuid.NewString()
	if err := control.playback.PlanStoreV3.SaveAttempt(t.Context(), playback.AttemptRecordV3{
		PlaybackAttemptID: uuid.NewString(), SessionID: remote, UserID: 1, ProfileID: "profile-1",
		RequestedMediaFileID: 100, EffectiveMediaFileID: 100, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	view, err := control.Terminate(t.Context(), AdminTerminateInput{SessionID: remote, ActorID: 2})
	if err != nil || !view.AuthorityRevoked || view.DurableState != AdminTerminateDurableStopped {
		t.Fatalf("view = %+v, %v", view, err)
	}
	record, err := control.playback.PlanStoreV3.GetAttempt(t.Context(), remote)
	if err != nil || !attemptEndedByAdmin(record) {
		t.Fatalf("remote attempt not stopped by an administrator: %+v %v", record, err)
	}
}

// TestAdminTerminateEndsAJellyfinPlayHeldElsewhere: a Jellyfin play has no
// attempt row to stop, so a terminate that lands on a replica without the
// session reaches the play only through the Jellyfin surface. When that
// surface ends the play, the terminate reports the revocation instead of an
// unknown session.
func TestAdminTerminateEndsAJellyfinPlayHeldElsewhere(t *testing.T) {
	control, sessionMgr, _, _ := newAdminPlaybackControlTestHandler(t)
	remote := uuid.NewString()
	sessionMgr.SetCompatAdminEnd(func(_ context.Context, sessionID string) bool {
		return sessionID == remote
	})

	view, err := control.Terminate(t.Context(), AdminTerminateInput{SessionID: remote, ActorID: 2})
	if err != nil || !view.AuthorityRevoked || view.DurableState != AdminTerminateDurableStopped {
		t.Fatalf("view = %+v, %v", view, err)
	}
	// A session neither surface knows stays unknown.
	if _, err := control.Terminate(t.Context(), AdminTerminateInput{SessionID: uuid.NewString(), ActorID: 2}); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("unknown session err = %v", err)
	}
}

// TestAdminTerminateRepeatConverges: a terminate of a session an
// administrator already ended answers the converged receipt and dispatches
// nothing, while a session its player stopped first stays unknown. Both rest
// on the attempt row, which every replica reads.
func TestAdminTerminateRepeatConverges(t *testing.T) {
	for _, earlier := range []string{"terminate", "player stop"} {
		t.Run(earlier, func(t *testing.T) {
			control, sessionMgr, hub, session := newAdminPlaybackControlTestHandler(t)
			if err := control.playback.PlanStoreV3.SaveAttempt(t.Context(), playback.AttemptRecordV3{
				PlaybackAttemptID: uuid.NewString(), SessionID: session.ID, UserID: 1, ProfileID: "profile-1",
				RequestedMediaFileID: 100, EffectiveMediaFileID: 100, ExpiresAt: time.Now().Add(time.Hour),
			}); err != nil {
				t.Fatal(err)
			}
			conn := &adminPlaybackControlTestConn{}
			registration := hub.Register(session.ID, conn)
			if registration == nil {
				t.Fatal("register lane")
			}
			t.Cleanup(func() { hub.Unregister(registration) })

			if earlier == "terminate" {
				if _, err := control.Terminate(t.Context(), AdminTerminateInput{SessionID: session.ID, ActorID: 2}); err != nil {
					t.Fatal(err)
				}
			} else {
				// The player's stop won the row and ended the live session.
				stopAttemptAs(control.playback, session.ID, uuid.NewString())
				if err := sessionMgr.StopSession(session.ID); err != nil {
					t.Fatal(err)
				}
			}
			sent := len(conn.messages)

			view, err := control.Terminate(t.Context(), AdminTerminateInput{SessionID: session.ID, ActorID: 2, Reason: "again"})
			if earlier == "player stop" {
				if !errors.Is(err, playback.ErrSessionNotFound) || errors.Is(err, ErrSessionEndedByAdmin) {
					t.Fatalf("terminate after a player stop: %+v, %v; want not found", view, err)
				}
			} else {
				want := AdminTerminateView{SessionID: session.ID, AuthorityRevoked: true, AlreadyRevoked: true, DurableState: AdminTerminateDurableStopped, Delivery: AdminTerminateDeliveryNone}
				if err != nil || view != want {
					t.Fatalf("repeat = %+v, %v; want %+v", view, err, want)
				}
			}
			if len(conn.messages) != sent {
				t.Fatalf("repeat dispatched %d more messages", len(conn.messages)-sent)
			}
		})
	}
}
