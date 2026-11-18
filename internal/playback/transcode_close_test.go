package playback

import (
	"context"
	"errors"
	"testing"
	"time"
)

// restartPauseSink holds a restart at its "ffmpeg process restart" event: the
// restart has registered its flight and stopped the previous generation, and
// has not started FFmpeg yet.
type restartPauseSink struct {
	entered chan struct{}
	release chan struct{}
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

// A first start can finish during a restart, after the restart stopped the
// throttler it found (none here), and arm one on the pipe of the process being
// replaced. When the restart hook then times out, its fallback must not take
// that throttler for the new process's, and must keep its threshold.
func TestRestartHookTimeoutReplacesAThrottlerOnTheOldPipe(t *testing.T) {
	setRestartHookTimeout(t, 50*time.Millisecond)
	s := newRunningFakeSession(t)
	s.mu.Lock()
	oldPipe := s.stdinPipe
	s.mu.Unlock()
	stale := NewTranscodeThrottler(s, oldPipe, 90, 2)
	s.SetRestartHook(func(ctx context.Context) {
		s.mu.Lock()
		s.throttler = stale
		s.mu.Unlock()
		<-ctx.Done()
	})

	if err := s.Restart(context.Background(), 0, 0); err != nil {
		t.Fatal(err)
	}

	s.mu.Lock()
	got := s.throttler
	current := got != nil && got.stdinPipe == s.stdinPipe
	s.mu.Unlock()
	if !current {
		t.Fatal("after a timed-out restart hook, the throttler does not write to the new process")
	}
	if got.thresholdSeconds != 90 {
		t.Fatalf("threshold = %d, want the 90 the first start read", got.thresholdSeconds)
	}
	select {
	case <-stale.stopCh:
	default:
		t.Fatal("the throttler on the replaced pipe is still running")
	}
}
