package playback

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// TestSegmentRecoveryDecisionWaitsWhileRestarting covers half of issue #243's
// seek-freeze: while a restart is already in flight, a concurrent segment
// request must WAIT for the restart's output rather than trigger another
// restart. Without this, pipelined HLS segment requests spawn dueling ffmpeg
// restarts that keep preempting the segment the player is blocked on.
func TestSegmentRecoveryDecisionWaitsWhileRestarting(t *testing.T) {
	session := &TranscodeSession{
		outputDir:  t.TempDir(),
		restarting: &restartFlight{done: make(chan struct{})},
		opts: TranscodeOpts{
			TargetCodecVideo:   "h264",
			SegmentDuration:    2,
			StartSegmentNumber: 0,
		},
	}

	decision := session.SegmentRecoveryDecision(10, time.Now())
	if decision.Reason != "transcode_restarting" {
		t.Fatalf("Reason = %q, want transcode_restarting", decision.Reason)
	}
	if !decision.Wait {
		t.Error("Wait = false, want true (concurrent requests must wait out an in-flight restart)")
	}
	if decision.RestartOnTimeout {
		t.Error("RestartOnTimeout = true, want false (a timed-out wait must re-decide, not blindly restart)")
	}
}

// TestRestartInvokesRestartHook verifies that a successful Restart fires the
// session's restart hook. The API handler uses the hook to re-arm the
// throttler and the exit monitor; firing it from Restart itself keeps every
// restart caller of a hook-wired session (web segment recovery, audio
// switch) consistent instead of each call site remembering to re-arm by
// hand.
func TestRestartInvokesRestartHook(t *testing.T) {
	// `true` starts and exits cleanly, standing in for ffmpeg. Resolve it
	// via PATH — it lives in /bin on Linux but /usr/bin on macOS.
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("`true` not found in PATH: %v", err)
	}

	session := &TranscodeSession{
		outputDir: t.TempDir(),
		opts: TranscodeOpts{
			TargetCodecVideo:   "h264",
			SegmentDuration:    2,
			StartSegmentNumber: 0,
			FFmpegPath:         truePath,
		},
	}

	hookFired := make(chan struct{}, 1)
	session.SetRestartHook(func(context.Context) {
		hookFired <- struct{}{}
	})

	if err := session.Restart(context.Background(), 20, 10); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	select {
	case <-hookFired:
	case <-time.After(2 * time.Second):
		t.Fatal("restart hook was not invoked after successful restart")
	}
}

func TestRestartCopySeekOriginIsReplacedOrCleared(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("`true` not found in PATH: %v", err)
	}

	newSession := func() *TranscodeSession {
		return &TranscodeSession{
			outputDir: t.TempDir(),
			opts: TranscodeOpts{
				TargetCodecVideo:       "copy",
				SegmentDuration:        2,
				SeekSeconds:            18,
				StreamOriginSeconds:    10,
				CopySeekAnchorResolved: true,
				StartSegmentNumber:     5,
				FFmpegPath:             truePath,
			},
		}
	}

	resolved := newSession()
	if err := resolved.RestartWithCopySeekAnchor(context.Background(), 100, 48, 96); err != nil {
		t.Fatalf("RestartWithCopySeekAnchor: %v", err)
	}
	resolvedOpts := resolved.Opts()
	if resolvedOpts.SeekSeconds != 100 || resolvedOpts.StreamOriginSeconds != 96 ||
		!resolvedOpts.CopySeekAnchorResolved || resolvedOpts.StartSegmentNumber != 48 {
		t.Fatalf("resolved restart opts = %+v", resolvedOpts)
	}

	unresolved := newSession()
	if err := unresolved.Restart(context.Background(), 100, 50); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	unresolvedOpts := unresolved.Opts()
	if unresolvedOpts.StreamOriginSeconds != 0 || unresolvedOpts.CopySeekAnchorResolved {
		t.Fatalf("generic restart retained stale copy origin: %+v", unresolvedOpts)
	}
}

// TestRestartWaiterReceivesInFlightOutcome covers the single-flight outcome:
// a caller arriving while a restart is in progress must not perform its own
// restart, and it must receive the in-flight restart's result instead of
// assuming success. The first caller here fails validation; the waiter must
// surface that failure rather than returning nil.
func TestRestartWaiterReceivesInFlightOutcome(t *testing.T) {
	session := &TranscodeSession{
		outputDir:  t.TempDir(),
		restarting: &restartFlight{done: make(chan struct{})},
		opts: TranscodeOpts{
			TargetCodecVideo:   "h264",
			SegmentDuration:    2,
			StartSegmentNumber: 0,
			// Nonexistent binary: if the waiter were to start its own restart
			// instead of joining the flight, exec fails and the call returns an
			// error, failing the assertions below.
			FFmpegPath: "/nonexistent/ffmpeg-single-flight-test",
		},
	}
	flight := session.restarting

	// The in-flight leader completes its restart with a validation failure.
	go func() {
		session.mu.Lock()
		flight.err = tonemap.ErrSourceRevisionChanged
		session.mu.Unlock()
		close(flight.done)
	}()

	err := session.Restart(context.Background(), 20, 10)
	if !errors.Is(err, tonemap.ErrSourceRevisionChanged) {
		t.Fatalf("Restart during in-flight restart = %v, want the in-flight validation outcome", err)
	}

	session.mu.Lock()
	restartCount := session.restartCount
	session.mu.Unlock()
	if restartCount != 0 {
		t.Errorf("restartCount = %d, want 0 (waiter must not perform a restart)", restartCount)
	}
}
