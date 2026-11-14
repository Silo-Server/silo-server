package playback

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
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

// A copy-mode restart at a missing segment resumes on the manifest's own
// timeline: seek to the segment's listed start, keep the stream origin, and
// number from the playlist's first segment. The MPEG-TS row covers the
// playlist with no init map and .ts names.
func TestRestartSegmentLocked_UsesResolvedCopyAnchorAndManifestNumber(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mpegts bool
		header []string
		ext    string
	}{
		{name: "fMP4", header: []string{"#EXT-X-VERSION:7", "#EXT-X-TARGETDURATION:3", "#EXT-X-MEDIA-SEQUENCE:9", "#EXT-X-MAP:URI=\"init.mp4\""}, ext: ".m4s"},
		{name: "MPEG-TS", mpegts: true, header: []string{"#EXT-X-VERSION:3", "#EXT-X-TARGETDURATION:3", "#EXT-X-MEDIA-SEQUENCE:9"}, ext: ".ts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			lines := append([]string{"#EXTM3U"}, tc.header...)
			lines = append(lines,
				"#EXTINF:2.669000,", "seg_00009"+tc.ext,
				"#EXTINF:1.669000,", "seg_00010"+tc.ext,
				"#EXTINF:1.668000,", "seg_00011"+tc.ext,
				"")
			if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
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
					CopyVideoMPEGTS:        tc.mpegts,
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
				opts.StartSegmentNumber != 9 || !opts.CopySeekAnchorResolved || opts.CopyVideoMPEGTS != tc.mpegts {
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
				time.Sleep(time.Millisecond) //nolint:forbidigo // Polls observable state: the fake FFmpeg writes its arguments from its own process.
			}
		})
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

// The API server tears a crashed local transcode down so the client can
// reconstruct it. A reconstruct starts on GPU decode again and dies at the same
// frame, so the exit monitor restarts this failure on CPU decode itself, and
// tears the session down as before when that restart cannot start.
func TestLocalExitMonitorRestartsFilterReinitFailureOnCPUDecode(t *testing.T) {
	for _, tc := range []struct {
		name           string
		ffmpegMissing  bool
		wantTornDown   bool
		wantRegistered bool
	}{
		{name: "restart starts", wantRegistered: true},
		{name: "restart cannot start", ffmpegMissing: true, wantTornDown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetDeviceLoad(t)
			fakeDeviceStat(t, qsvPairDevA)
			synctest.Test(t, func(t *testing.T) {
				const sessionID = "reinit-local"
				dir := t.TempDir()
				argsPath := filepath.Join(dir, "restart-args")
				ffmpegPath := filepath.Join(dir, "ffmpeg")
				if !tc.ffmpegMissing {
					// The replacement exits at once: a process left running
					// would hold the bubble's clock.
					script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsPath + "\"\n"
					if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				exited := make(chan struct{})
				close(exited)
				stderr := newBoundedTailBuffer(0)
				_, _ = stderr.Write([]byte("[vf#0:0 @ 0x5ad071e0c0c0] Error reinitializing filters!\n"))
				s := &TranscodeSession{
					outputDir: dir, done: exited, waitErr: errors.New("exit status 187"), stderr: stderr,
					lastRequestedSegment: 312,
					opts: TranscodeOpts{
						InputPath: "/nonexistent/input.mkv", OutputDir: dir, SessionID: sessionID,
						TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, StartSegmentNumber: 300,
						FFmpegPath: ffmpegPath, HWAccel: transcodeHWQSV, HWDevice: qsvPairDevA,
					},
				}
				m := NewTranscodeManager()
				m.RegisterTranscodeSession(sessionID, s)
				var tornDown atomic.Bool
				m.OnFFmpegCrash = func(_ context.Context, id string, dead *TranscodeSession) {
					tornDown.Store(true)
					m.CloseTranscodeSessionIf(id, dead, "")
				}

				m.MonitorLocalTranscodeExit(sessionID, s)
				time.Sleep(3 * time.Second) //nolint:forbidigo // synctest advances virtual time past the monitor's two-second delay
				synctest.Wait()

				if got := tornDown.Load(); got != tc.wantTornDown {
					t.Fatalf("session torn down = %v, want %v", got, tc.wantTornDown)
				}
				if got := m.GetTranscodeSession(sessionID) == s; got != tc.wantRegistered {
					t.Fatalf("session still registered = %v, want %v", got, tc.wantRegistered)
				}
				if tc.wantTornDown {
					return
				}
				args, err := os.ReadFile(argsPath)
				if err != nil {
					t.Fatalf("the exit monitor did not restart the session: %v", err)
				}
				restart := "\n" + string(args)
				if strings.Contains(restart, "\n-hwaccel\n") {
					t.Fatalf("the restart repeated the GPU decode that had just failed:\n%s", restart)
				}
				// It resumes at the last segment the client downloaded.
				if !strings.Contains(restart, "\n-start_number\n312\n") {
					t.Fatalf("the restart did not resume at the client's position:\n%s", restart)
				}
			})
		})
	}
}
