package playback

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// TestSegmentRecoveryDecisionWaitsWhileRestarting covers half of issue #243's
// seek-freeze: while a restart is already in flight, a concurrent segment
// request must WAIT for the restart's output rather than trigger another
// restart. Without this, pipelined HLS segment requests spawn dueling ffmpeg
// restarts that keep preempting the segment the player is blocked on.
func TestSegmentRecoveryDecisionWaitsWhileRestarting(t *testing.T) {
	session := &TranscodeSession{
		outputDir:  t.TempDir(),
		restarting: true,
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

// TestRestartIsSingleFlight covers the other half: Restart must be
// single-flight per session. A second caller arriving while a restart is in
// progress must return immediately without killing the process the first
// restart just started.
func TestRestartIsSingleFlight(t *testing.T) {
	session := &TranscodeSession{
		outputDir:  t.TempDir(),
		restarting: true,
		opts: TranscodeOpts{
			TargetCodecVideo:   "h264",
			SegmentDuration:    2,
			StartSegmentNumber: 0,
			// Nonexistent binary: if the guard is missing and Restart
			// proceeds, exec fails and the call returns an error, failing
			// the assertions below.
			FFmpegPath: "/nonexistent/ffmpeg-single-flight-test",
		},
	}

	err := session.Restart(context.Background(), 20, 10)
	if err != nil {
		t.Fatalf("Restart during in-flight restart = %v, want nil (single-flight no-op)", err)
	}

	session.mu.Lock()
	restartCount := session.restartCount
	stillRestarting := session.restarting
	session.mu.Unlock()
	if restartCount != 0 {
		t.Errorf("restartCount = %d, want 0 (second caller must not perform a restart)", restartCount)
	}
	if !stillRestarting {
		t.Error("restarting flag cleared by no-op caller; must be left for the in-flight restart to clear")
	}
}
