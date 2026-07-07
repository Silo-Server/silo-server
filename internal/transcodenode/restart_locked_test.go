package transcodenode

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// The node's segment-recovery restart must also run under the per-session
// lifecycle lock so it cannot race a fresh start or reconstruct into the same
// output directory. These tests exercise the wrapper's gating and liveness
// re-check without spawning ffmpeg (the playback package covers the real spawn).

// restartSessionLocked must block while another lifecycle holder owns the lock,
// and re-check session liveness once it acquires it — a session torn down while
// the restart waited must yield ErrSessionSuperseded rather than a stale spawn.
func TestRestartSessionLocked_WaitsForLockThenRechecks(t *testing.T) {
	s := &Server{sessions: map[string]*playback.TranscodeSession{}}
	const sid = "sess-x"
	sess := &playback.TranscodeSession{}
	s.mu.Lock()
	s.sessions[sid] = sess
	s.mu.Unlock()

	// A concurrent start/reconstruct holds the lifecycle lock.
	release := s.lockSessionLifecycle(sid)

	done := make(chan error, 1)
	go func() {
		done <- s.restartSessionLocked(context.Background(), sid, sess, 0, 0)
	}()

	select {
	case <-done:
		t.Fatal("restartSessionLocked returned while the lifecycle lock was held")
	case <-time.After(150 * time.Millisecond):
	}

	// Simulate a teardown removing the session while the restart is blocked, so
	// the post-lock re-check fails and no ffmpeg is spawned.
	s.mu.Lock()
	delete(s.sessions, sid)
	s.mu.Unlock()
	release()

	select {
	case err := <-done:
		if !errors.Is(err, playback.ErrSessionSuperseded) {
			t.Fatalf("want ErrSessionSuperseded after teardown, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restartSessionLocked did not complete after lock release")
	}
}

func TestRestartSessionLocked_SupersededWhenUnregistered(t *testing.T) {
	s := &Server{sessions: map[string]*playback.TranscodeSession{}}
	err := s.restartSessionLocked(context.Background(), "sess-x", &playback.TranscodeSession{}, 0, 0)
	if !errors.Is(err, playback.ErrSessionSuperseded) {
		t.Fatalf("want ErrSessionSuperseded for an unmapped session, got %v", err)
	}
}

func TestRestartSessionLocked_SupersededWhenReplaced(t *testing.T) {
	s := &Server{sessions: map[string]*playback.TranscodeSession{}}
	const sid = "sess-x"
	s.mu.Lock()
	s.sessions[sid] = &playback.TranscodeSession{} // a different session owns the id
	s.mu.Unlock()

	err := s.restartSessionLocked(context.Background(), sid, &playback.TranscodeSession{}, 0, 0)
	if !errors.Is(err, playback.ErrSessionSuperseded) {
		t.Fatalf("want ErrSessionSuperseded for a replaced session, got %v", err)
	}
}
