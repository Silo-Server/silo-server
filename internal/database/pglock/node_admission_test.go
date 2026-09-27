package pglock

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNodeAdmissionUpgradeAndJoin(t *testing.T) {
	pool := testPool(t)
	key := time.Now().UnixNano()
	owner, err := AdmitNode(t.Context(), pool, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	peer, err := AdmitNode(t.Context(), pool, key)
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := owner.TryExclusive(t.Context())
	if err != nil || acquired {
		t.Fatalf("upgrade with a second shared holder = (%t, %v), want unavailable", acquired, err)
	}
	if err := peer.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Closing a client socket can return just before PostgreSQL has retired
	// that backend's session lock. Observe the upgrade rather than assuming
	// the server processed the close in the same scheduling turn.
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		acquired, err = owner.TryExclusive(t.Context())
		if err != nil || acquired {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("peer session lock remained after close")
		}
	}
	if err != nil || !acquired {
		t.Fatalf("upgrade after peer exit = (%t, %v), want exclusive", acquired, err)
	}

	joinCtx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	joined, err := AdmitNode(joinCtx, pool, key)
	if joined != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("join during exclusive ownership = (%v, %v), want deadline", joined, err)
	}
	if err := owner.ReleaseExclusive(t.Context()); err != nil {
		t.Fatal(err)
	}
	joined, err = AdmitNode(t.Context(), pool, key)
	if err != nil {
		t.Fatalf("join after exclusive release: %v", err)
	}
	if err := joined.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// terminateSession ends the admission's backend the way a database restart or
// failover does.
func terminateSession(t *testing.T, pool *pgxpool.Pool, admission *NodeAdmission) {
	t.Helper()
	var pid int
	if err := admission.conn.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	var terminated bool
	if err := pool.QueryRow(t.Context(), `SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate admission backend = (%t, %v)", terminated, err)
	}
}

// recordingGate stands in for the blob store fences.
type recordingGate struct {
	paused  chan struct{}
	resumed chan struct{}
}

func newRecordingGate() *recordingGate {
	return &recordingGate{paused: make(chan struct{}, 4), resumed: make(chan struct{}, 4)}
}

func (g *recordingGate) pause(context.Context) (func(), error) {
	g.paused <- struct{}{}
	return func() { g.resumed <- struct{}{} }, nil
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not happen", what)
	}
}

// A database restart or failover must not stop a node that is not part of a
// transition: it rejoins on a new session and keeps serving.
func TestNodeAdmissionRejoinsAfterSharedSessionLoss(t *testing.T) {
	pool := testPool(t)
	key := time.Now().UnixNano()
	owner, err := AdmitNode(t.Context(), pool, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	gate := newRecordingGate()
	owner.SetWriteGate(gate.pause)
	terminateSession(t, pool, owner)
	if err := owner.Probe(t.Context()); err != nil {
		t.Fatalf("probe after session loss = %v, want rejoined", err)
	}
	select {
	case <-owner.Lost():
		t.Fatal("rejoinable session loss closed the lost signal")
	case <-gate.paused:
		t.Fatal("an immediate rejoin paused writes")
	default:
	}
	// The new session holds the shared lock: a joining node cannot take
	// exclusive ownership while this node is admitted.
	peer, err := AdmitNode(t.Context(), pool, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close(context.Background()) })
	if acquired, err := peer.TryExclusive(t.Context()); err != nil || acquired {
		t.Fatalf("peer upgrade beside a rejoined node = (%t, %v), want unavailable", acquired, err)
	}
}

// Until the database answers again, the node stays up with writes paused.
func TestNodeAdmissionPausesWritesUntilRejoin(t *testing.T) {
	pool := testPool(t)
	owner, err := AdmitNode(t.Context(), pool, time.Now().UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	gate := newRecordingGate()
	owner.SetWriteGate(gate.pause)
	unreachable, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if err := owner.Probe(unreachable); !errors.Is(err, ErrAdmissionRejoining) {
		t.Fatalf("probe without a reachable database = %v, want rejoining", err)
	}
	waitSignal(t, gate.paused, "pausing writes")
	if acquired, err := owner.TryExclusive(t.Context()); acquired || !errors.Is(err, ErrAdmissionRejoining) {
		t.Fatalf("upgrade while rejoining = (%t, %v), want ErrAdmissionRejoining", acquired, err)
	}
	if err := owner.Probe(t.Context()); err != nil {
		t.Fatalf("probe once the database answers = %v, want rejoined", err)
	}
	waitSignal(t, gate.resumed, "resuming writes")
	select {
	case <-owner.Lost():
		t.Fatal("rejoin closed the lost signal")
	default:
	}
}

// Another node may have started a transition while this one was out. It keeps
// the exclusive lock until it exits, so this node must stop instead of writing
// beside it.
func TestNodeAdmissionStopsWhenAnotherNodeOwnsTransition(t *testing.T) {
	pool := testPool(t)
	key := time.Now().UnixNano()
	owner, err := AdmitNode(t.Context(), pool, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	peer, err := AdmitNode(t.Context(), pool, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close(context.Background()) })
	gate := newRecordingGate()
	owner.SetWriteGate(gate.pause)
	terminateSession(t, pool, owner)
	deadline := time.Now().Add(3 * time.Second)
	for {
		acquired, err := peer.TryExclusive(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if acquired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminated session kept its shared lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := owner.Probe(t.Context()); !errors.Is(err, ErrAdmissionLost) {
		t.Fatalf("rejoin beside a transition owner = %v, want ErrAdmissionLost", err)
	}
	select {
	case <-owner.Lost():
	default:
		t.Fatal("refused rejoin left the lost signal open")
	}
	waitSignal(t, gate.paused, "pausing writes")
}

// A node that owns a transition cannot resume after losing its session: the
// exclusive lock went with it, so the copy has to stop.
func TestNodeAdmissionLossWhileOwningTransitionIsFinal(t *testing.T) {
	pool := testPool(t)
	owner, err := AdmitNode(t.Context(), pool, time.Now().UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	if acquired, err := owner.TryExclusive(t.Context()); err != nil || !acquired {
		t.Fatalf("upgrade = (%t, %v)", acquired, err)
	}
	terminateSession(t, pool, owner)
	if err := owner.Probe(t.Context()); !errors.Is(err, ErrAdmissionLost) {
		t.Fatalf("probe after losing an exclusive session = %v, want ErrAdmissionLost", err)
	}
	select {
	case <-owner.Lost():
	default:
		t.Fatal("lost admission signal was not closed")
	}
	if acquired, err := owner.TryExclusive(t.Context()); acquired || !errors.Is(err, ErrAdmissionLost) {
		t.Fatalf("upgrade after loss = (%t, %v), want ErrAdmissionLost", acquired, err)
	}
}

// Monitor is what main runs: a restart of the database under it must leave the
// node admitted, not stopped.
func TestNodeAdmissionMonitorRejoinsAfterDatabaseRestart(t *testing.T) {
	pool := testPool(t)
	key := time.Now().UnixNano()
	owner, err := AdmitNode(t.Context(), pool, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	original := owner.conn
	terminateSession(t, pool, owner)
	go owner.Monitor(ctx, 20*time.Millisecond)
	deadline := time.Now().Add(3 * time.Second)
	for {
		owner.mu.Lock()
		rejoined := owner.conn != nil && owner.conn != original
		owner.mu.Unlock()
		if rejoined {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("monitor did not rejoin after the session was terminated")
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-owner.Lost():
		t.Fatal("monitor stopped the node after a database restart")
	default:
	}
	if acquired, err := owner.TryExclusive(t.Context()); err != nil || !acquired {
		t.Fatalf("upgrade after rejoin = (%t, %v), want exclusive", acquired, err)
	}
}

// A node cut off for a whole transition, including its commit and restart,
// finds no exclusive holder when it returns. The location check stops it
// instead of letting it write to the stores it opened before the commit.
func TestNodeAdmissionRejoinStopsWhenStorageMoved(t *testing.T) {
	pool := testPool(t)
	key := time.Now().UnixNano()
	owner, err := AdmitNode(t.Context(), pool, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	gate := newRecordingGate()
	owner.SetWriteGate(gate.pause)
	owner.SetRejoinCheck(func(context.Context) error {
		return fmt.Errorf("%w: artwork storage is recorded as %q", ErrStorageMoved, "s3|https://s3|public|")
	})
	terminateSession(t, pool, owner)
	if err := owner.Probe(t.Context()); !errors.Is(err, ErrAdmissionLost) || !errors.Is(err, ErrStorageMoved) {
		t.Fatalf("rejoin after storage moved = %v, want final loss", err)
	}
	select {
	case <-owner.Lost():
	default:
		t.Fatal("moved storage left the lost signal open")
	}
	waitSignal(t, gate.paused, "pausing writes")
	// The rejected session must not keep a shared lock behind.
	peer, err := AdmitNode(t.Context(), pool, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close(context.Background()) })
	deadline := time.Now().Add(3 * time.Second)
	for {
		acquired, err := peer.TryExclusive(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if acquired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rejected rejoin kept its shared lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A check that cannot read the recorded location keeps the node rejoining with
// writes paused, and the next probe tries again.
func TestNodeAdmissionRejoinRetriesUnreadableCheck(t *testing.T) {
	pool := testPool(t)
	owner, err := AdmitNode(t.Context(), pool, time.Now().UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	gate := newRecordingGate()
	owner.SetWriteGate(gate.pause)
	readable := false
	owner.SetRejoinCheck(func(context.Context) error {
		if !readable {
			return errors.New("settings unavailable")
		}
		return nil
	})
	terminateSession(t, pool, owner)
	if err := owner.Probe(t.Context()); !errors.Is(err, ErrAdmissionRejoining) {
		t.Fatalf("rejoin with an unreadable check = %v, want rejoining", err)
	}
	waitSignal(t, gate.paused, "pausing writes")
	readable = true
	if err := owner.Probe(t.Context()); err != nil {
		t.Fatalf("rejoin once the check passes = %v", err)
	}
	waitSignal(t, gate.resumed, "resuming writes")
}

func TestNodeAdmissionVerifyExclusive(t *testing.T) {
	pool := testPool(t)
	owner, err := AdmitNode(t.Context(), pool, time.Now().UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	if err := owner.VerifyExclusive(t.Context()); !errors.Is(err, ErrAdmissionLost) {
		t.Fatalf("verify without exclusive ownership = %v, want ErrAdmissionLost", err)
	}
	if acquired, err := owner.TryExclusive(t.Context()); err != nil || !acquired {
		t.Fatalf("upgrade = (%t, %v)", acquired, err)
	}
	if err := owner.VerifyExclusive(t.Context()); err != nil {
		t.Fatalf("verify while owning = %v", err)
	}
	terminateSession(t, pool, owner)
	if err := owner.VerifyExclusive(t.Context()); !errors.Is(err, ErrAdmissionLost) {
		t.Fatalf("verify after the session ended = %v, want ErrAdmissionLost", err)
	}
	select {
	case <-owner.Lost():
	default:
		t.Fatal("lost exclusive session was not signaled")
	}
}
