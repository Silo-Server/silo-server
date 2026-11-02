package playback

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMoveTranscodeExecutorOnlyReplacesTheExpectedRoute(t *testing.T) {
	manager := NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", 42, PlayTranscode, false)
	if err != nil {
		t.Fatal(err)
	}
	dead := TranscodeRoute{NodeURL: "http://dead:8080", TransportID: "transport-1"}
	if err := manager.SetTranscodeRoute(session.ID, dead); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetNodeRoutingAssignment(session.ID, NodeRoutingAssignment{
		Workload: "video_transcode", Execution: "transcode", ExecutionNodeID: 1,
		ExecutionNodeURL: dead.NodeURL, Egress: "api",
	}); err != nil {
		t.Fatal(err)
	}

	stale := TranscodeRoute{NodeURL: dead.NodeURL, TransportID: "older-transport"}
	moved, err := manager.MoveTranscodeExecutor(session.ID, stale, TranscodeExecutorMove{NodeURL: "http://other:8080"})
	if err != nil || moved {
		t.Fatalf("move against a superseded route = %v, %v; want no change", moved, err)
	}

	moved, err = manager.MoveTranscodeExecutor(session.ID, dead, TranscodeExecutorMove{
		NodeURL: "http://alive:8080", Execution: "transcode", ExecutionNodeID: 2, TranscodeHWAccel: "vaapi",
	})
	if err != nil || !moved {
		t.Fatalf("move = %v, %v", moved, err)
	}
	got, _ := manager.GetSession(session.ID)
	if got.TranscodeNodeURL != "http://alive:8080" || got.TranscodeTransportID != "transport-1" {
		t.Fatalf("route = %q/%q, want the new node and the same transport", got.TranscodeNodeURL, got.TranscodeTransportID)
	}
	if got.RoutingExecutionNodeID != 2 || got.RoutingExecutionNodeURL != "http://alive:8080" || got.RoutingEgress != "api" {
		t.Fatalf("routing = %+v", got)
	}
	if got.TranscodeHWAccel != "vaapi" {
		t.Fatalf("hw accel = %q", got.TranscodeHWAccel)
	}
}

func TestMoveTranscodeExecutorLeavesLegacyRoutingUnassigned(t *testing.T) {
	manager := NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", 42, PlayTranscode, false)
	if err != nil {
		t.Fatal(err)
	}
	dead := TranscodeRoute{NodeURL: "http://dead:8080"}
	if err := manager.SetTranscodeRoute(session.ID, dead); err != nil {
		t.Fatal(err)
	}
	moved, err := manager.MoveTranscodeExecutor(session.ID, dead, TranscodeExecutorMove{Execution: "api"})
	if err != nil || !moved {
		t.Fatalf("move = %v, %v", moved, err)
	}
	got, _ := manager.GetSession(session.ID)
	// Writing only the execution half would read as a partially committed
	// route, which the serve paths refuse.
	if got.TranscodeNodeURL != "" || got.RoutingExecution != "" {
		t.Fatalf("node %q execution %q, want a local legacy session with no routing", got.TranscodeNodeURL, got.RoutingExecution)
	}
}

// TestRehomeTranscodeJoinsAnInFlightMove pins that a caller arriving while a
// move for the same session runs waits on it rather than starting another, and
// that a caller who gives up stops waiting without disturbing it.
func TestRehomeTranscodeJoinsAnInFlightMove(t *testing.T) {
	m := NewTranscodeManager()
	var runs atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	move := func(context.Context) error {
		if runs.Add(1) == 1 {
			close(entered)
		}
		<-release
		return nil
	}

	var wg sync.WaitGroup
	leaderErr := make(chan error, 1)
	wg.Go(func() { leaderErr <- m.RehomeTranscode(context.Background(), "session-1", move) })
	<-entered

	// The leader is parked inside move, so this caller can only join it: a
	// second run would have to wait for release, which has not happened.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.RehomeTranscode(ctx, "session-1", move); err == nil {
		t.Fatal("a canceled caller did not stop waiting")
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("moves = %d while one was in flight, want 1", got)
	}

	close(release)
	wg.Wait()
	if err := <-leaderErr; err != nil {
		t.Fatalf("leader error: %v", err)
	}
}

func TestRehomeTranscodeMoveOutlivesTheLeadingRequest(t *testing.T) {
	m := NewTranscodeManager()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	check := make(chan struct{})
	moveCtxErr := make(chan error, 1)
	var deadline time.Time
	go func() {
		_ = m.RehomeTranscode(ctx, "session-1", func(moveCtx context.Context) error {
			deadline, _ = moveCtx.Deadline()
			close(started)
			<-check
			moveCtxErr <- moveCtx.Err()
			return nil
		})
	}()
	<-started
	cancel()
	close(check)
	if err := <-moveCtxErr; err != nil {
		t.Fatalf("the move was canceled with its leading request: %v", err)
	}
	if deadline.IsZero() {
		t.Fatal("the move has no time bound of its own")
	}
}
