package playback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// CloseProcess must leave the output directory intact so a session sharing the
// same directory (e.g. a concurrent reconstruct winner) keeps serving its
// segments and init.mp4. Close, by contrast, removes the directory.
func TestCloseProcessKeepsOutputDir(t *testing.T) {
	dir := t.TempDir()
	seg := filepath.Join(dir, "init.mp4")
	if err := os.WriteFile(seg, []byte("data"), 0o644); err != nil {
		t.Fatalf("seed segment: %v", err)
	}

	s := &TranscodeSession{outputDir: dir}
	if err := s.CloseProcess(); err != nil {
		t.Fatalf("CloseProcess: %v", err)
	}
	if _, err := os.Stat(seg); err != nil {
		t.Fatalf("CloseProcess must not remove output dir, but segment is gone: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Close must remove output dir, stat err = %v", err)
	}
}

// restartPauseSink holds a restart at its "ffmpeg process restart" event: the
// restart has registered its flight and stopped the previous generation, and
// has not started FFmpeg yet.
type restartPauseSink struct {
	entered chan struct{}
	release chan struct{}
}

func newRestartPauseSink() *restartPauseSink {
	return &restartPauseSink{entered: make(chan struct{}), release: make(chan struct{})}
}

func (*restartPauseSink) WriteLine(context.Context, string, FFmpegLogAttrs, string) {}

func (p *restartPauseSink) WriteEvent(_ context.Context, _ string, _ FFmpegLogAttrs, message string) {
	if message != "ffmpeg process restart" {
		return
	}
	close(p.entered)
	<-p.release
}

// newRunningFakeSession returns a session running a stand-in FFmpeg that
// lives until killed. The binary sits outside the output directory, which
// Close removes.
func newRunningFakeSession(t *testing.T) *TranscodeSession {
	t.Helper()
	dir := t.TempDir()
	s := &TranscodeSession{
		outputDir: dir,
		opts: TranscodeOpts{
			SessionID:  "sess-close",
			FFmpegPath: fakeFFmpegScript(t),
			OutputDir:  dir,
		},
	}
	t.Cleanup(func() {
		s.StopThrottler()
		_ = s.Close()
	})
	if err := s.Restart(context.Background(), 0, 0); err != nil {
		t.Fatal(err)
	}
	return s
}

// sessionClosers are the two ways a session ends; both are terminal.
var sessionClosers = []struct {
	name  string
	close func(*TranscodeSession) error
}{
	{"Close", (*TranscodeSession).Close},
	{"CloseProcess", (*TranscodeSession).CloseProcess},
}

// requireClosed fails unless s has no live generation: its current done is
// closed and it reports nothing running.
func requireClosed(t *testing.T, s *TranscodeSession, restartErr error) {
	t.Helper()
	select {
	case <-s.Done():
	default:
		t.Fatalf("the session was closed with a generation alive (restart: %v)", restartErr)
	}
	if s.IsRunning() {
		t.Fatalf("a closed session runs (restart: %v)", restartErr)
	}
}

// Closing while a restart is between stopping the old generation and
// starting the new one: the close waits for the restart, which publishes no
// generation and kills the FFmpeg it started, so nothing runs afterwards.
func TestCloseDuringRestartLeavesNothingRunning(t *testing.T) {
	for _, closer := range sessionClosers {
		t.Run(closer.name, func(t *testing.T) {
			s := newRunningFakeSession(t)
			sink := newRestartPauseSink()
			s.mu.Lock()
			s.opts.FFmpegLogSink = sink
			s.mu.Unlock()

			restarted := make(chan error, 1)
			go func() { restarted <- s.Restart(context.Background(), 10, 5) }()
			<-sink.entered

			// Closing stops the throttler first thing, once the session is
			// marked closing. Install one now, after the restart stopped its
			// own, to observe that the close has begun.
			marker := NewTranscodeThrottler(s, nil, minThresholdSeconds, 2)
			s.mu.Lock()
			s.throttler = marker
			s.mu.Unlock()

			closed := make(chan error, 1)
			go func() { closed <- closer.close(s) }()
			<-marker.stopCh
			close(sink.release)

			restartErr := <-restarted
			if err := <-closed; err != nil {
				t.Fatal(err)
			}
			requireClosed(t, s, restartErr)
			if !errors.Is(restartErr, ErrSessionSuperseded) {
				t.Fatalf("restart racing %s = %v, want ErrSessionSuperseded", closer.name, restartErr)
			}
		})
	}
}

// A restart that begins after the session closed is refused: it starts no
// FFmpeg and runs no restart hook.
func TestRestartAfterCloseIsRefused(t *testing.T) {
	for _, closer := range sessionClosers {
		t.Run(closer.name, func(t *testing.T) {
			s := newRunningFakeSession(t)
			hooked := false
			s.SetRestartHook(func(context.Context) { hooked = true })
			if err := closer.close(s); err != nil {
				t.Fatal(err)
			}

			err := s.Restart(context.Background(), 10, 5)
			if !errors.Is(err, ErrSessionSuperseded) {
				t.Fatalf("restart after %s = %v, want ErrSessionSuperseded", closer.name, err)
			}
			requireClosed(t, s, err)
			if hooked {
				t.Fatal("a refused restart ran the restart hook")
			}
		})
	}
}

// A restart hook still running when the session closes, such as the one that
// re-arms the throttler, arms nothing on the closed session.
func TestClosedSessionArmsNoThrottler(t *testing.T) {
	s := newRunningFakeSession(t)
	hookEntered := make(chan struct{})
	releaseHook := make(chan struct{})
	s.SetRestartHook(func(context.Context) {
		close(hookEntered)
		<-releaseHook
		s.StartThrottler(minThresholdSeconds)
	})

	restarted := make(chan error, 1)
	go func() { restarted <- s.Restart(context.Background(), 0, 0) }()
	<-hookEntered
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	close(releaseHook)
	if err := <-restarted; err != nil {
		t.Fatalf("restart published before Close = %v", err)
	}

	s.mu.Lock()
	armed := s.throttler != nil
	s.mu.Unlock()
	if armed {
		t.Fatal("the restart hook armed a throttler on a closed session")
	}
}
