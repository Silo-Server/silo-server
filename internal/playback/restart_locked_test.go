package playback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
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
