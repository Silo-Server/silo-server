package playback

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newAsyncRestartTestSession builds a session whose ffmpeg stub appends the
// value of every -ss argument to logPath and then sleeps, so a test can observe
// which seek positions were actually applied without a real transcode pipeline.
func newAsyncRestartTestSession(t *testing.T, logPath string) *TranscodeSession {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-ffmpeg.sh")
	script := "#!/bin/sh\n" +
		"prev=\"\"\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$prev\" = \"-ss\" ]; then echo \"$arg\" >> \"" + logPath + "\"; fi\n" +
		"  prev=\"$arg\"\n" +
		"done\n" +
		"exec sleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return &TranscodeSession{
		outputDir: dir,
		opts: TranscodeOpts{
			SessionID:        "sess-async",
			InputPath:        "/media/movie.mkv",
			OutputDir:        dir,
			FFmpegPath:       bin,
			TargetCodecVideo: "h264",
			TargetCodecAudio: "aac",
			SegmentDuration:  2,
		},
	}
}

// waitAsyncRestartsDrained blocks until the manager has no coalesced far-seek
// worker left for sessionID, so the test observes a settled state rather than a
// fixed sleep.
func waitAsyncRestartsDrained(t *testing.T, m *TranscodeManager, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.asyncRestartMu.Lock()
		pending := m.asyncRestarts[sessionID]
		m.asyncRestartMu.Unlock()
		if pending == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("async seek restart worker did not drain")
		}
		time.Sleep(time.Millisecond)
	}
}

func readSeekLog(t *testing.T, logPath string) []string {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read seek log: %v", err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// waitSeekLogLast polls until the log's final applied seek is want. Spawning the
// ffmpeg stub returns before the shell has written its argument line, so the
// test must observe the write rather than the spawn.
func waitSeekLogLast(t *testing.T, logPath, want string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		seeks := readSeekLog(t, logPath)
		if len(seeks) > 0 && seeks[len(seeks)-1] == want {
			return seeks
		}
		if time.Now().After(deadline) {
			t.Fatalf("seek log = %v, want the last applied seek to be %q", seeks, want)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestRestartSegmentAsyncCoalescesToLatestTarget pins the far-seek coalescing:
// two different positions queued while the lifecycle lock is held must produce
// exactly one restart, at the newest target, rather than racing to be the last
// writer (which could leave ffmpeg at the older position).
func TestRestartSegmentAsyncCoalescesToLatestTarget(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "seeks.log")
	m := NewTranscodeManager()
	s := newAsyncRestartTestSession(t, logPath)
	const sid = "sess-async"
	m.RegisterTranscodeSession(sid, s)
	t.Cleanup(func() { _ = s.Close() })

	// Hold the lifecycle lock so both requests land before the worker runs; the
	// worker must read the coalesced latest target, not the first.
	release := m.LockSessionLifecycle(sid)
	m.RestartSegmentAsync(sid, s, 10)
	m.RestartSegmentAsync(sid, s, 20)
	release()

	waitAsyncRestartsDrained(t, m, sid)
	seeks := waitSeekLogLast(t, logPath, "40.000")
	if len(seeks) != 1 {
		t.Fatalf("applied seeks = %v, want exactly one restart at 40.000 (segment 20)", seeks)
	}
}

// TestRestartSegmentAsyncKeepsNewestSeekWhenOlderWaitExpiresLate pins the
// ordering that a seek can expose: two waits overlap, the viewer's newer seek
// reaches the coalescer first, and the older wait times out afterwards. The
// older target must not overwrite the newer one, so the worker applies exactly
// the newer position.
func TestRestartSegmentAsyncKeepsNewestSeekWhenOlderWaitExpiresLate(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "seeks.log")
	m := NewTranscodeManager()
	s := newAsyncRestartTestSession(t, logPath)
	const sid = "sess-async"
	m.RegisterTranscodeSession(sid, s)
	t.Cleanup(func() { _ = s.Close() })

	// Both requests arrived before either applied; the viewer asked for the
	// newer one second even though the older wait expires last.
	older := s.BeginSegmentRecovery()
	newer := s.BeginSegmentRecovery()
	release := m.LockSessionLifecycle(sid)
	m.RestartSegmentAsyncForRequest(sid, s, 20, newer)
	m.RestartSegmentAsyncForRequest(sid, s, 10, older)
	release()

	waitAsyncRestartsDrained(t, m, sid)
	seeks := waitSeekLogLast(t, logPath, "40.000")
	if len(seeks) != 1 {
		t.Fatalf("applied seeks = %v, want exactly one restart at 40.000 (the newer segment 20)", seeks)
	}
}

// TestRestartSegmentForRecoveryRefusesSupersededTarget pins the last check
// before ffmpeg is re-spawned: a request whose newer seek has already been
// recorded answers superseded instead of moving the generation to the position
// the viewer left.
func TestRestartSegmentForRecoveryRefusesSupersededTarget(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "seeks.log")
	s := newAsyncRestartTestSession(t, logPath)
	older := s.BeginSegmentRecovery()
	newer := s.BeginSegmentRecovery()
	s.MarkSegmentSeek(newer)

	target, ok, err := s.RestartSegmentForRecovery(context.Background(), 10, older)
	if !errors.Is(err, ErrSegmentRecoverySuperseded) {
		t.Fatalf("RestartSegmentForRecovery error = %v, want ErrSegmentRecoverySuperseded", err)
	}
	if ok {
		t.Fatal("RestartSegmentForRecovery reported a restart for a superseded target")
	}
	if target != (SegmentRecoveryTarget{}) {
		t.Fatalf("target = %+v, want the zero target", target)
	}
	if seeks := readSeekLog(t, logPath); len(seeks) != 0 {
		t.Fatalf("applied seeks = %v, want no restart for the superseded target", seeks)
	}
}

// TestRestartSegmentAsyncConvergesWhenTargetArrivesMidRestart pins the second
// half of the coalescing: a newer target that arrives while the first restart is
// still resolving makes the worker run once more, so the final applied position
// is the latest one the player asked for.
func TestRestartSegmentAsyncConvergesWhenTargetArrivesMidRestart(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "seeks.log")
	m := NewTranscodeManager()
	s := newAsyncRestartTestSession(t, logPath)
	const sid = "sess-async"
	m.RegisterTranscodeSession(sid, s)
	t.Cleanup(func() { _ = s.Close() })

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.opts.RefreshInput = func(context.Context) (string, func(), error) {
		once.Do(func() { close(entered) })
		<-release
		return s.opts.InputPath, nil, nil
	}

	m.RestartSegmentAsync(sid, s, 10)
	<-entered // the first restart is now inside restart(), holding the lifecycle lock
	m.RestartSegmentAsync(sid, s, 20)
	close(release)

	waitAsyncRestartsDrained(t, m, sid)
	seeks := waitSeekLogLast(t, logPath, "40.000")
	if len(seeks) > 2 {
		t.Fatalf("applied seeks = %v, want convergence in at most two restarts", seeks)
	}
}

// TestRestartSegmentAsyncAppliesTargetArrivingAtCompletion pins the completion
// window. The worker reads whether the coalesced target moved and then leaves the
// loop; a request landing between that read and the entry's removal used to find
// the entry still present, record its target, and return without starting a
// worker, after which the exiting worker deleted the entry — the newest target
// was never applied. The hook fires after the atomic completion check, when the
// entry is already gone, so the late request must start a fresh worker and its
// target must be the final applied seek.
func TestRestartSegmentAsyncAppliesTargetArrivingAtCompletion(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "seeks.log")
	m := NewTranscodeManager()
	s := newAsyncRestartTestSession(t, logPath)
	const sid = "sess-async"
	m.RegisterTranscodeSession(sid, s)
	t.Cleanup(func() { _ = s.Close() })

	// The first worker exits at segment 10 (seek 20.000); enqueue the late
	// target exactly in its completion window. Once guards the second worker's
	// own completion from enqueueing again.
	var once sync.Once
	m.asyncRestartFinishHook = func() {
		once.Do(func() { m.RestartSegmentAsync(sid, s, 20) })
	}

	m.RestartSegmentAsync(sid, s, 10)

	waitAsyncRestartsDrained(t, m, sid)
	// The final applied seek must be the late target. The first restart's stub
	// can be killed before its shell writes its argument, so only the last is
	// asserted; under the old check-then-delete the late target was dropped and
	// ffmpeg was left at segment 10 (seek 20.000).
	waitSeekLogLast(t, logPath, "40.000")
}

// TestRestartSegmentAsyncFollowsAReplanSuccessor pins the seek across a
// generation swap. A replan holds the session lifecycle lock across its
// spawn→register window, so a detached far-seek restart queued behind it wakes
// to a replaced generation. Dropping the restart with the predecessor abandons
// the player's newer seek and leaves the successor anchored where the replan
// started; the target must be applied to the live successor instead.
func TestRestartSegmentAsyncFollowsAReplanSuccessor(t *testing.T) {
	const sid = "sess-async"
	m := NewTranscodeManager()
	predecessorLog := filepath.Join(t.TempDir(), "predecessor.log")
	successorLog := filepath.Join(t.TempDir(), "successor.log")
	predecessor := newAsyncRestartTestSession(t, predecessorLog)
	successor := newAsyncRestartTestSession(t, successorLog)
	// The replan's successor starts at the session's last position (segment
	// 45 / 90s), behind the seek target at segment 345 (690s).
	successor.opts.StartSegmentNumber = 45
	successor.opts.SeekSeconds = 90
	m.RegisterTranscodeSession(sid, predecessor)
	t.Cleanup(func() {
		_ = predecessor.Close()
		_ = successor.Close()
	})

	// Hold the lock so the seek restart is queued on the predecessor, then
	// publish the successor exactly as a replan's swap does before releasing.
	release := m.LockSessionLifecycle(sid)
	m.RestartSegmentAsync(sid, predecessor, 345)
	m.SwapTranscodeSession(sid, successor)
	release()

	waitAsyncRestartsDrained(t, m, sid)
	seeks := waitSeekLogLast(t, successorLog, "690.000")
	if len(seeks) != 1 {
		t.Fatalf("successor applied seeks = %v, want exactly one restart at the seek target", seeks)
	}
	// The adopted restart is the seek's, not the replan's anchor: the successor
	// now names segment 345 and would seek FFmpeg to 690s.
	if opts := successor.Opts(); opts.StartSegmentNumber != 345 || math.Abs(opts.SeekSeconds-690) > 1e-9 {
		t.Fatalf("successor position = segment %d at %.3fs, want segment 345 at 690s",
			opts.StartSegmentNumber, opts.SeekSeconds)
	}
	if seeks := readSeekLog(t, predecessorLog); len(seeks) != 0 {
		t.Fatalf("superseded generation applied seeks = %v, want none", seeks)
	}
}

// TestRestartSegmentAsyncSkipsSuccessorAlreadyAtTheSeekTarget pins the other
// half of adoption: when the successor already starts at or past the requested
// segment, the seek is already its position and rebuilding it would only stall
// playback.
func TestRestartSegmentAsyncSkipsSuccessorAlreadyAtTheSeekTarget(t *testing.T) {
	const sid = "sess-async"
	m := NewTranscodeManager()
	predecessor := newAsyncRestartTestSession(t, filepath.Join(t.TempDir(), "predecessor.log"))
	successorLog := filepath.Join(t.TempDir(), "successor.log")
	successor := newAsyncRestartTestSession(t, successorLog)
	successor.opts.StartSegmentNumber = 345
	successor.opts.SeekSeconds = 690
	m.RegisterTranscodeSession(sid, predecessor)
	t.Cleanup(func() {
		_ = predecessor.Close()
		_ = successor.Close()
	})

	release := m.LockSessionLifecycle(sid)
	m.RestartSegmentAsync(sid, predecessor, 345)
	m.SwapTranscodeSession(sid, successor)
	release()
	waitAsyncRestartsDrained(t, m, sid)

	if seeks := readSeekLog(t, successorLog); len(seeks) != 0 {
		t.Fatalf("successor applied seeks = %v, want none: it already starts at the target", seeks)
	}
	// Not rebuilding also means the successor keeps its own position instead of
	// being moved back to the older request.
	if opts := successor.Opts(); opts.StartSegmentNumber != 345 || math.Abs(opts.SeekSeconds-690) > 1e-9 {
		t.Fatalf("successor position = segment %d at %.3fs, want its own segment 345 at 690s",
			opts.StartSegmentNumber, opts.SeekSeconds)
	}
}

// TestRestartSegmentAsyncStopsAtTheAdoptionBound pins the replan-storm bound: a
// queued far-seek restart follows successor generations, but only
// maxAsyncRestartSuccessors of them. Each successor below is replaced while the
// restart is still inside its spawn window, so the worker never completes a
// restart and the final generation is left to the client's next request. The
// worker must terminate at the bound, leave the live generation unpoisoned, and
// still let a later request apply the seek it dropped.
func TestRestartSegmentAsyncStopsAtTheAdoptionBound(t *testing.T) {
	const sid = "sess-async"
	m := NewTranscodeManager()
	newGeneration := func(name string) (*TranscodeSession, string) {
		logPath := filepath.Join(t.TempDir(), name+".log")
		s := newAsyncRestartTestSession(t, logPath)
		s.opts.StartSegmentNumber = 45
		s.opts.SeekSeconds = 90
		return s, logPath
	}
	predecessor, predecessorLog := newGeneration("predecessor")
	first, firstLog := newGeneration("successor-1")
	second, secondLog := newGeneration("successor-2")
	live, liveLog := newGeneration("successor-3")
	m.RegisterTranscodeSession(sid, predecessor)
	t.Cleanup(func() {
		for _, s := range []*TranscodeSession{predecessor, first, second, live} {
			_ = s.Close()
		}
	})

	// Block each successor's restart in the input refresh, which runs while the
	// session lifecycle lock is held. Swapping the live generation there is the
	// replan storm: the restart wakes to a generation that is no longer live.
	blockRestart := func(s *TranscodeSession) (entered <-chan struct{}, release chan<- struct{}) {
		in := make(chan struct{})
		out := make(chan struct{})
		var once sync.Once
		s.opts.RefreshInput = func(context.Context) (string, func(), error) {
			once.Do(func() { close(in) })
			<-out
			return "/media/movie.mkv", nil, nil
		}
		return in, out
	}
	firstEntered, releaseFirst := blockRestart(first)
	secondEntered, releaseSecond := blockRestart(second)
	waitEntered := func(entered <-chan struct{}, which string) {
		t.Helper()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s restart did not reach its spawn window", which)
		}
	}

	// Queue the seek on the predecessor, then let the first replan replace it.
	release := m.LockSessionLifecycle(sid)
	m.RestartSegmentAsync(sid, predecessor, 345)
	m.asyncRestartMu.Lock()
	pending := m.asyncRestarts[sid]
	m.asyncRestartMu.Unlock()
	if pending == nil {
		t.Fatal("no coalescing entry was created for the queued seek")
	}
	m.SwapTranscodeSession(sid, first)
	release()

	waitEntered(firstEntered, "first successor")
	m.SwapTranscodeSession(sid, second)
	close(releaseFirst)

	waitEntered(secondEntered, "second successor")
	m.SwapTranscodeSession(sid, live)
	close(releaseSecond)

	// Terminating is the bound: nothing may hold the session in a restart loop.
	waitAsyncRestartsDrained(t, m, sid)

	pending.mu.Lock()
	adoptions := pending.adoptions
	adopted := pending.ts
	pending.mu.Unlock()
	if adoptions != maxAsyncRestartSuccessors {
		t.Fatalf("worker adopted %d successors, want exactly %d", adoptions, maxAsyncRestartSuccessors)
	}
	if adopted != second {
		t.Fatalf("worker stopped on %p, want the last successor it was allowed to adopt (%p)", adopted, second)
	}
	if got := m.GetTranscodeSession(sid); got != live {
		t.Fatalf("live generation = %p, want the storm's final successor %p", got, live)
	}
	// The bound is a documented teardown, not a failure verdict: neither the
	// generation the worker stopped on nor the live successor may be poisoned.
	for name, s := range map[string]*TranscodeSession{
		"predecessor": predecessor, "successor-1": first, "successor-2": second, "successor-3": live,
	} {
		if got := s.AsyncRestartFailure(); got != nil {
			t.Fatalf("%s latched a failure after a superseded teardown: %v", name, got)
		}
	}
	// None of the storm's restarts completed: each was superseded before its
	// process spawned, so no generation moved.
	for name, logPath := range map[string]string{
		"predecessor": predecessorLog, "successor-1": firstLog, "successor-2": secondLog, "successor-3": liveLog,
	} {
		if seeks := readSeekLog(t, logPath); len(seeks) != 0 {
			t.Fatalf("%s applied seeks = %v after the bound, want none", name, seeks)
		}
	}
	// The dropped seek is not lost to the live generation: the next request for
	// the same segment still starts it at the seek target.
	if _, ok, err := m.RestartSegmentLocked(context.Background(), sid, live, 345); err != nil || !ok {
		t.Fatalf("live generation could not apply the queued seek after the bound: ok=%v err=%v", ok, err)
	}
	if seeks := waitSeekLogLast(t, liveLog, "690.000"); len(seeks) != 1 {
		t.Fatalf("live generation applied seeks = %v, want one restart at the seek target", seeks)
	}
}

// TestFollowAsyncRestartSuccessorIgnoresAReplacedEntry pins the entry-identity
// check: when a newer request has replaced this worker's coalescing entry, that
// request's worker owns the successor. Adopting here would restart the same
// generation twice and could move FFmpeg back to this worker's older target.
func TestFollowAsyncRestartSuccessorIgnoresAReplacedEntry(t *testing.T) {
	const sid = "sess-async"
	m := NewTranscodeManager()
	predecessor := newAsyncRestartTestSession(t, filepath.Join(t.TempDir(), "predecessor.log"))
	successor := newAsyncRestartTestSession(t, filepath.Join(t.TempDir(), "successor.log"))
	successor.opts.StartSegmentNumber = 45
	successor.opts.SeekSeconds = 90
	m.RegisterTranscodeSession(sid, successor)
	t.Cleanup(func() {
		_ = predecessor.Close()
		_ = successor.Close()
	})

	stale := &pendingAsyncRestart{ts: predecessor, segNum: 345}
	newer := &pendingAsyncRestart{ts: successor, segNum: 20}
	m.asyncRestarts = map[string]*pendingAsyncRestart{sid: newer}

	if m.followAsyncRestartSuccessor(sid, stale, predecessor) {
		t.Fatal("a worker whose coalescing entry was replaced adopted the successor")
	}
	if stale.adoptions != 0 || stale.ts != predecessor {
		t.Fatalf("stale worker adoptions = %d on %p, want 0 on the predecessor %p",
			stale.adoptions, stale.ts, predecessor)
	}
	if m.asyncRestarts[sid] != newer {
		t.Fatal("the stale worker changed the newer worker's coalescing entry")
	}
}

// TestRestartSegmentAsyncLatchesTerminalFailure pins the poison-latch: a
// detached restart refused by a dead source records its verdict so the next
// segment request can surface the real failure instead of a second
// stale_generation.
func TestRestartSegmentAsyncLatchesTerminalFailure(t *testing.T) {
	m := NewTranscodeManager()
	s := newAsyncRestartTestSession(t, filepath.Join(t.TempDir(), "seeks.log"))
	const sid = "sess-async"
	m.RegisterTranscodeSession(sid, s)
	t.Cleanup(func() { _ = s.Close() })
	s.demuxStamped = true

	m.RestartSegmentAsync(sid, s, 20)
	waitAsyncRestartsDrained(t, m, sid)

	if got := s.AsyncRestartFailure(); !errors.Is(got, ErrVirtualSourceDemuxFailed) {
		t.Fatalf("latched async restart failure = %v, want ErrVirtualSourceDemuxFailed", got)
	}
}

// TestRestartSegmentAsyncIgnoresSupersededTeardown pins that routine teardown
// does not poison the session: when the worker loses to a close, there is no
// failure verdict to surface on a session that no longer exists.
func TestRestartSegmentAsyncIgnoresSupersededTeardown(t *testing.T) {
	m := NewTranscodeManager()
	s := newAsyncRestartTestSession(t, filepath.Join(t.TempDir(), "seeks.log"))
	const sid = "sess-async"
	m.RegisterTranscodeSession(sid, s)
	t.Cleanup(func() { _ = s.Close() })
	s.demuxStamped = true

	// Unregister before the worker runs so restartSessionLocked reports
	// ErrSessionSuperseded rather than rebuilding a dead source.
	m.transcodeMu.Lock()
	delete(m.transcodes, sid)
	m.transcodeMu.Unlock()

	m.RestartSegmentAsync(sid, s, 20)
	waitAsyncRestartsDrained(t, m, sid)

	if got := s.AsyncRestartFailure(); got != nil {
		t.Fatalf("latched failure after a superseded restart = %v, want nil", got)
	}
}
