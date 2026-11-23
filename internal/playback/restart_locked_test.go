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

// newFakeFFmpegSession builds a minimal TranscodeSession whose ffmpeg binary is
// a stub that sleeps, so Restart spawns a real (harmless) child process without
// a full transcode pipeline. Only the fields Restart touches are populated.
func newFakeFFmpegSession(t *testing.T) *TranscodeSession {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-ffmpeg.sh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return &TranscodeSession{
		outputDir: dir,
		opts: TranscodeOpts{
			SessionID:  "sess-x",
			FFmpegPath: bin,
			OutputDir:  dir,
		},
	}
}

func TestRestartSegmentLocked_UsesResolvedCopyAnchorAndManifestNumber(t *testing.T) {
	dir := t.TempDir()
	manifest := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-TARGETDURATION:3",
		"#EXT-X-MEDIA-SEQUENCE:9",
		"#EXT-X-MAP:URI=\"init.mp4\"",
		"#EXTINF:2.669000,",
		"seg_00009.m4s",
		"#EXTINF:1.669000,",
		"seg_00010.m4s",
		"#EXTINF:1.668000,",
		"seg_00011.m4s",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	argsPath := filepath.Join(dir, "restart-args")
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	ffmpeg := `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "framecrc" ]; then
    printf '%s\n' '#tb 0: 1/1000'
    printf '%s\n' '0, 18000, 18000, 41, 1024, 0x12345678'
    exit 0
  fi
done
printf '%s\n' "$@" > "` + argsPath + `"
exec sleep 30
`
	if err := os.WriteFile(ffmpegPath, []byte(ffmpeg), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}

	s := &TranscodeSession{
		outputDir: dir,
		opts: TranscodeOpts{
			SessionID:              "sess-copy",
			InputPath:              "/media/movie.mkv",
			OutputDir:              dir,
			FFmpegPath:             ffmpegPath,
			SeekSeconds:            18.261,
			StreamOriginSeconds:    18,
			CopySeekAnchorResolved: true,
			TargetCodecVideo:       "copy",
			TargetCodecAudio:       "copy",
			SegmentDuration:        2,
			StartSegmentNumber:     9,
		},
	}
	m := NewTranscodeManager()
	m.RegisterTranscodeSession("sess-copy", s)
	t.Cleanup(func() { _ = s.Close() })

	target, ok, err := m.RestartSegmentLocked(context.Background(), "sess-copy", s, 10)
	if err != nil {
		t.Fatalf("RestartSegmentLocked: %v", err)
	}
	if !ok {
		t.Fatal("RestartSegmentLocked returned ok=false")
	}
	if math.Abs(target.SeekSeconds-20.669) > 0.0001 || math.Abs(target.StreamOriginSeconds-18) > 0.0001 ||
		target.StartSegmentNumber != 9 || !target.CopySeekAnchorResolved {
		t.Fatalf("restart target = %+v, want seek=20.669 origin=18 start=9 resolved=true", target)
	}

	opts := s.Opts()
	if math.Abs(opts.SeekSeconds-20.669) > 0.0001 || math.Abs(opts.StreamOriginSeconds-18) > 0.0001 ||
		opts.StartSegmentNumber != 9 || !opts.CopySeekAnchorResolved {
		t.Fatalf("live restart opts = %+v, want resolved copy timeline", opts)
	}
	deadline := time.Now().Add(2 * time.Second)
	var args []byte
	for {
		args, err = os.ReadFile(argsPath)
		if err == nil && strings.Contains(string(args), "-ss\n20.669\n") && strings.Contains(string(args), "-start_number\n9\n") {
			break
		}
		if time.Now().After(deadline) {
			if err != nil {
				t.Fatalf("read restart args: %v", err)
			}
			t.Fatalf("restart args do not preserve resolved timeline:\n%s", args)
		}
		time.Sleep(time.Millisecond)
	}
}

// A restart must not spawn ffmpeg while another lifecycle holder (a fresh start,
// reconstruct, or another restart) owns the per-session lock — otherwise two
// ffmpeg processes write the same output directory. RestartSessionLocked must
// block on the lifecycle lock and only spawn once it is released.
func TestRestartSessionLocked_WaitsForLifecycleLock(t *testing.T) {
	m := NewTranscodeManager()
	s := newFakeFFmpegSession(t)
	const sid = "sess-x"
	m.RegisterTranscodeSession(sid, s)
	t.Cleanup(func() { _ = s.Close() })

	// Simulate a concurrent start/reconstruct holding the lifecycle lock.
	release := m.LockSessionLifecycle(sid)

	done := make(chan error, 1)
	go func() {
		done <- m.RestartSessionLocked(context.Background(), sid, s, 0, 0)
	}()

	// While the lock is held the restart must neither return nor spawn ffmpeg.
	select {
	case <-done:
		t.Fatal("RestartSessionLocked returned while the lifecycle lock was held")
	case <-time.After(150 * time.Millisecond):
	}
	if s.IsRunning() {
		t.Fatal("ffmpeg spawned while another holder owned the lifecycle lock")
	}

	release()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RestartSessionLocked after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RestartSessionLocked did not complete after lock release")
	}
	if !s.IsRunning() {
		t.Fatal("expected the session to be running after a successful restart")
	}
}

// Concurrent restarts on the same session (e.g. audio-switch racing
// segment-recovery) must serialize: the winner spawns exactly one live ffmpeg
// and neither call reports failure.
func TestRestartSessionLocked_ConcurrentRestartsSerialize(t *testing.T) {
	m := NewTranscodeManager()
	s := newFakeFFmpegSession(t)
	const sid = "sess-x"
	m.RegisterTranscodeSession(sid, s)
	t.Cleanup(func() { _ = s.Close() })

	const n = 8
	errs := make(chan error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			<-start
			errs <- m.RestartSessionLocked(context.Background(), sid, s, 0, 0)
		}()
	}
	close(start)

	for i := 0; i < n; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("concurrent RestartSessionLocked returned error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent RestartSessionLocked did not complete")
		}
	}
	if !s.IsRunning() {
		t.Fatal("expected exactly one live ffmpeg after concurrent restarts settle")
	}
}

// A segment request can wait on the lifecycle lock behind another restart and
// be abandoned there when the player seeks again. Once it gets the lock it must
// not restart FFmpeg for a position nobody is waiting on.
func TestRestartSegmentLocked_SkipsRequestThatEnded(t *testing.T) {
	m := NewTranscodeManager()
	s := newFakeFFmpegSession(t)
	const sid = "sess-x"
	m.RegisterTranscodeSession(sid, s)
	t.Cleanup(func() { _ = s.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := m.RestartSegmentLocked(ctx, sid, s, 40); !errors.Is(err, context.Canceled) {
		t.Fatalf("RestartSegmentLocked error = %v, want context.Canceled", err)
	}
	if s.IsRunning() {
		t.Fatal("restarted FFmpeg for a request that had ended")
	}
}

// A restart whose session was torn down (or replaced by a reconstruct winner)
// while it waited must not re-spawn ffmpeg for the stale handle.
func TestRestartSessionLocked_SupersededWhenUnregistered(t *testing.T) {
	m := NewTranscodeManager()
	s := newFakeFFmpegSession(t)

	err := m.RestartSessionLocked(context.Background(), "sess-x", s, 0, 0)
	if !errors.Is(err, ErrSessionSuperseded) {
		t.Fatalf("want ErrSessionSuperseded for an unmapped session, got %v", err)
	}
	if s.IsRunning() {
		t.Fatal("a superseded restart must not spawn ffmpeg")
	}
}

func TestRestartSessionLocked_SupersededWhenReplaced(t *testing.T) {
	m := NewTranscodeManager()
	stale := newFakeFFmpegSession(t)
	live := newFakeFFmpegSession(t)
	const sid = "sess-x"
	m.RegisterTranscodeSession(sid, live) // a different session now owns the id
	t.Cleanup(func() { _ = live.Close() })

	err := m.RestartSessionLocked(context.Background(), sid, stale, 0, 0)
	if !errors.Is(err, ErrSessionSuperseded) {
		t.Fatalf("want ErrSessionSuperseded for a replaced session, got %v", err)
	}
	if stale.IsRunning() {
		t.Fatal("a superseded restart must not spawn ffmpeg for the stale handle")
	}
	if live.IsRunning() {
		t.Fatal("the live session must be untouched by a stale restart")
	}
}

// A copy-mode segment request past the produced manifest window must restart
// FFmpeg in place at the bounded estimate for the requested segment instead of
// reporting an unresolved target. The handler turns "unresolved" into a 404,
// which forces the client through a full replan (provider re-resolve +
// transport startup). The fallback begins a fresh emit at the requested
// segment number using the probed keyframe origin.
func TestRestartSegmentLocked_CopyForwardJumpRestartsPastProducedWindow(t *testing.T) {
	dir := t.TempDir()
	manifest := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-TARGETDURATION:3",
		"#EXT-X-MEDIA-SEQUENCE:9",
		"#EXT-X-MAP:URI=\"init.mp4\"",
		"#EXTINF:2.669000,",
		"seg_00009.m4s",
		"#EXTINF:1.669000,",
		"seg_00010.m4s",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	argsPath := filepath.Join(dir, "restart-args")
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	// The anchor probe runs before the restart; report a real keyframe past the
	// manifest's last URI (79s) so segmentNumberAtSourceTime cannot align it.
	ffmpeg := `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "framecrc" ]; then
    printf '%s\n' '#tb 0: 1/1000'
    printf '%s\n' '0, 79000, 79000, 41, 1024, 0x12345678'
    exit 0
  fi
done
printf '%s\n' "$@" > "` + argsPath + `"
exec sleep 30
`
	if err := os.WriteFile(ffmpegPath, []byte(ffmpeg), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}

	s := &TranscodeSession{
		outputDir: dir,
		opts: TranscodeOpts{
			SessionID:              "sess-copy-jump",
			InputPath:              "/media/movie.mkv",
			OutputDir:              dir,
			FFmpegPath:             ffmpegPath,
			SeekSeconds:            18.261,
			StreamOriginSeconds:    18,
			CopySeekAnchorResolved: true,
			TargetCodecVideo:       "copy",
			TargetCodecAudio:       "copy",
			SegmentDuration:        2,
			StartSegmentNumber:     9,
			TotalDuration:          120,
		},
	}
	m := NewTranscodeManager()
	m.RegisterTranscodeSession("sess-copy-jump", s)
	t.Cleanup(func() { _ = s.Close() })

	const requested = 40
	target, ok, err := m.RestartSegmentLocked(context.Background(), "sess-copy-jump", s, requested)
	if err != nil {
		t.Fatalf("RestartSegmentLocked: %v", err)
	}
	if !ok {
		t.Fatal("RestartSegmentLocked returned ok=false for a segment inside the seek envelope")
	}
	// base 18 + (40-9)*2 = 80, inside the 120s envelope.
	if math.Abs(target.SeekSeconds-80) > 0.0001 || math.Abs(target.StreamOriginSeconds-79) > 0.0001 ||
		target.StartSegmentNumber != requested || !target.CopySeekAnchorResolved {
		t.Fatalf("restart target = %+v, want seek=80 origin=79 start=%d resolved=true", target, requested)
	}

	// The restart must spawn FFmpeg at the requested segment. Wait on the
	// observable argument file rather than a fixed sleep.
	deadline := time.Now().Add(2 * time.Second)
	for {
		args, readErr := os.ReadFile(argsPath)
		if readErr == nil && strings.Contains(string(args), "-ss\n80.000\n") && strings.Contains(string(args), "-start_number\n40\n") {
			break
		}
		if time.Now().After(deadline) {
			if readErr != nil {
				t.Fatalf("read restart args: %v", readErr)
			}
			t.Fatalf("restart args do not target the requested segment:\n%s", args)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestRestartKeepsInputWhenRefreshReturnsSamePath proves an in-place restart
// whose refresh hands back the pinned input does not release the cleanup that
// owns the live relay. Reuse semantics (#158) depend on this: that cleanup
// releases the very relay the replacement generation opens, so releasing it
// would delete the transport out from under the restart.
func TestRestartKeepsInputWhenRefreshReturnsSamePath(t *testing.T) {
	const pinnedInput = "/relay/source/pinned-token/movie.mp4"
	var releases int
	s := newFakeFFmpegSession(t)
	s.opts.InputPath = pinnedInput
	s.opts.InputCleanup = func() { releases++ }
	s.opts.RefreshInput = func(context.Context) (string, func(), error) {
		// Reuse: same path, no new cleanup.
		return pinnedInput, nil, nil
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Restart(context.Background(), 12, 3); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if releases != 0 {
		t.Fatalf("input cleanup ran %d times, want 0 (reuse must keep the pinned relay)", releases)
	}
	if got := s.Opts().InputPath; got != pinnedInput {
		t.Fatalf("InputPath = %q, want the unchanged pinned input %q", got, pinnedInput)
	}
}

// TestRestartReleasesRedundantCleanupOnSamePath proves a same-path refresh that
// still supplies a cleanup does not leak it: the retained cleanup owns the live
// relay, so the refresh's redundant registration is released immediately and is
// never installed as the session's input cleanup. Before the fix the same-path
// branch discarded the refresh cleanup outright (#158).
func TestRestartReleasesRedundantCleanupOnSamePath(t *testing.T) {
	const pinnedInput = "/relay/source/pinned-token/movie.mp4"
	var retainedReleases, redundantReleases int
	s := newFakeFFmpegSession(t)
	s.opts.InputPath = pinnedInput
	s.opts.InputCleanup = func() { retainedReleases++ }
	s.opts.RefreshInput = func(context.Context) (string, func(), error) {
		// Same path but a fresh cleanup: a redundant re-registration of the
		// same relay, which the restart must release rather than leak.
		return pinnedInput, func() { redundantReleases++ }, nil
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Restart(context.Background(), 12, 3); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if redundantReleases != 1 {
		t.Fatalf("refresh cleanup ran %d times, want 1 (same-path refresh must not leak it)", redundantReleases)
	}
	if retainedReleases != 0 {
		t.Fatalf("retained cleanup ran %d times, want 0 (reuse must keep the pinned relay)", retainedReleases)
	}
	if got := s.Opts().InputPath; got != pinnedInput {
		t.Fatalf("InputPath = %q, want the unchanged pinned input %q", got, pinnedInput)
	}

	// The retained cleanup still owns the relay and runs exactly once at close;
	// the redundant cleanup is not installed, so close does not run it again.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if retainedReleases != 1 {
		t.Fatalf("retained cleanup ran %d times total, want 1 at close", retainedReleases)
	}
	if redundantReleases != 1 {
		t.Fatalf("refresh cleanup ran %d times total, want 1 (never the installed cleanup)", redundantReleases)
	}
}

// TestRestartReleasesPreviousInputOnNewPath proves a refresh that supplies a
// genuinely new input releases the previous cleanup exactly once and never runs
// the replacement's cleanup during the swap, so a rotated relay does not leak
// and the replacement relay is not torn down.
func TestRestartReleasesPreviousInputOnNewPath(t *testing.T) {
	const newInput = "/relay/source/new-token/movie.mp4"
	var releases int
	s := newFakeFFmpegSession(t)
	s.opts.InputPath = "/relay/source/old-token/movie.mp4"
	s.opts.InputCleanup = func() { releases++ }
	var newReleases int
	s.opts.RefreshInput = func(context.Context) (string, func(), error) {
		return newInput, func() { newReleases++ }, nil
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Restart(context.Background(), 12, 3); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if releases != 1 {
		t.Fatalf("old input cleanup ran %d times, want 1", releases)
	}
	if got := s.Opts().InputPath; got != newInput {
		t.Fatalf("InputPath = %q, want the refreshed input %q", got, newInput)
	}
	if newReleases != 0 {
		t.Fatalf("new input cleanup ran %d times before session close, want 0", newReleases)
	}

	// The replacement is installed as the session's cleanup: close releases it
	// exactly once, while the old cleanup is never run a second time.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if newReleases != 1 {
		t.Fatalf("new input cleanup ran %d times after close, want 1", newReleases)
	}
	if releases != 1 {
		t.Fatalf("old input cleanup ran %d times total, want exactly 1", releases)
	}
}

// TestRestartRotationDoesNotLeakAcrossRestarts proves repeated path-changing
// restarts release each superseded input exactly once and retain only the final
// replacement, so a chain of relay rotations cannot leak a registration or tear
// down the live one.
func TestRestartRotationDoesNotLeakAcrossRestarts(t *testing.T) {
	var firstReleases, secondReleases, finalReleases int
	s := newFakeFFmpegSession(t)
	s.opts.InputPath = "/relay/source/token-0/movie.mp4"
	s.opts.InputCleanup = func() { firstReleases++ }
	started := 0
	s.opts.RefreshInput = func(context.Context) (string, func(), error) {
		started++
		if started == 1 {
			return "/relay/source/token-1/movie.mp4", func() { secondReleases++ }, nil
		}
		return "/relay/source/token-2/movie.mp4", func() { finalReleases++ }, nil
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Restart(context.Background(), 12, 3); err != nil {
		t.Fatalf("first restart: %v", err)
	}
	if err := s.Restart(context.Background(), 14, 4); err != nil {
		t.Fatalf("second restart: %v", err)
	}
	if got, want := s.Opts().InputPath, "/relay/source/token-2/movie.mp4"; got != want {
		t.Fatalf("InputPath = %q, want the final rotated input %q", got, want)
	}
	if firstReleases != 1 {
		t.Fatalf("token-0 cleanup ran %d times, want 1", firstReleases)
	}
	if secondReleases != 1 {
		t.Fatalf("token-1 cleanup ran %d times, want 1", secondReleases)
	}
	if finalReleases != 0 {
		t.Fatalf("final cleanup ran %d times before session close, want 0", finalReleases)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if finalReleases != 1 {
		t.Fatalf("final cleanup ran %d times after close, want 1", finalReleases)
	}
	if firstReleases != 1 || secondReleases != 1 {
		t.Fatalf("superseded cleanups ran again after close: token-0=%d token-1=%d, want 1 each", firstReleases, secondReleases)
	}
}

// TestRestartSessionLockedSupersededWhenClosedFirst proves a close that
// unregisters the session before the restart starts makes the restart refuse
// with ErrSessionSuperseded and never re-spawn ffmpeg.
func TestRestartSessionLockedSupersededWhenClosedFirst(t *testing.T) {
	m := NewTranscodeManager()
	s := newFakeFFmpegSession(t)
	const sid = "sess-x"
	m.RegisterTranscodeSession(sid, s)
	t.Cleanup(func() { _ = s.Close() })

	m.CloseTranscodeSession(sid, "")

	err := m.RestartSessionLocked(context.Background(), sid, s, 0, 0)
	if !errors.Is(err, ErrSessionSuperseded) {
		t.Fatalf("restart after close = %v, want ErrSessionSuperseded", err)
	}
	if s.IsRunning() {
		t.Fatal("a restart after a close must not spawn ffmpeg")
	}
}

// TestRestartSessionLockedSupersededWhenClosedMidRestart proves the restart's
// liveness re-check stops a replacement that a stop unregistered after the
// manager's guard but before the spawn: the restart returns ErrSessionSuperseded
// and spawns no ffmpeg.
func TestRestartSessionLockedSupersededWhenClosedMidRestart(t *testing.T) {
	m := NewTranscodeManager()
	s := newFakeFFmpegSession(t)
	const sid = "sess-x"
	s.opts.InputPath = "/media/movie.mkv"
	m.RegisterTranscodeSession(sid, s)
	t.Cleanup(func() { _ = s.Close() })

	// Pause the restart after the guard and before the spawn so the close lands
	// in the exact window the liveness re-check exists to cover.
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.opts.RefreshInput = func(context.Context) (string, func(), error) {
		once.Do(func() { close(entered) })
		<-release
		return "/media/movie.mkv", nil, nil
	}

	restartDone := make(chan error, 1)
	go func() { restartDone <- m.RestartSessionLocked(context.Background(), sid, s, 0, 0) }()
	<-entered

	m.CloseTranscodeSession(sid, "")
	close(release)

	err := <-restartDone
	if !errors.Is(err, ErrSessionSuperseded) {
		t.Fatalf("restart closed mid-flight = %v, want ErrSessionSuperseded", err)
	}
	if s.IsRunning() {
		t.Fatal("a restart superseded mid-flight must not leave ffmpeg running")
	}
}

// TestCloseTranscodeSessionDuringRestartKillsReplacement proves a close landing
// after a restart spawned cannot leave the replacement ffmpeg running: the close
// stops the process the restart produced.
func TestCloseTranscodeSessionDuringRestartKillsReplacement(t *testing.T) {
	m := NewTranscodeManager()
	s := newFakeFFmpegSession(t)
	const sid = "sess-x"
	m.RegisterTranscodeSession(sid, s)
	t.Cleanup(func() { _ = s.Close() })

	hookEntered := make(chan struct{})
	hookRelease := make(chan struct{})
	s.SetRestartHook(func(context.Context) {
		close(hookEntered)
		<-hookRelease
	})

	restartDone := make(chan error, 1)
	go func() { restartDone <- m.RestartSessionLocked(context.Background(), sid, s, 0, 0) }()
	<-hookEntered // the restart has spawned ffmpeg and is past its liveness checks

	// The close unregisters and stops the session while the restart is still in
	// its critical section.
	m.CloseTranscodeSession(sid, "")
	close(hookRelease)
	if err := <-restartDone; err != nil {
		t.Fatalf("restart: %v", err)
	}

	if m.GetTranscodeSession(sid) != nil {
		t.Fatal("session still registered after close")
	}
	if s.IsRunning() {
		t.Fatal("the restart's replacement ffmpeg survived the close")
	}
}

// The replacement FFmpeg can write stderr as soon as it starts, before restart
// publishes the new options. Logging that line must not race the restart.
// Only `go test -race` tells fixed from unfixed code here.
func TestRestartLogsReplacementStderrWithoutRacingOptions(t *testing.T) {
	s := newFakeFFmpegSession(t)
	bin := filepath.Join(s.outputDir, "chatty-ffmpeg.sh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'replacement started' >&2\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	sink := &lineSignalingFFmpegLogSink{lines: make(chan string, 1)}
	s.opts.FFmpegPath = bin
	s.opts.FFmpegLogSink = sink
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Restart(context.Background(), 0, 0); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	select {
	case line := <-sink.lines:
		if line != "replacement started" {
			t.Fatalf("logged line = %q, want the replacement's stderr line", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the replacement's stderr line never reached the log sink")
	}
}

type lineSignalingFFmpegLogSink struct{ lines chan string }

func (s *lineSignalingFFmpegLogSink) WriteLine(_ context.Context, _ string, _ FFmpegLogAttrs, line string) {
	select {
	case s.lines <- line:
	default:
	}
}

func (s *lineSignalingFFmpegLogSink) WriteEvent(context.Context, string, FFmpegLogAttrs, string) {}
