package transcodenode

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestRestartSegmentLocked_UsesResolvedCopyAnchorAndManifestNumber(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	manifest := "#EXTM3U\\n#EXT-X-VERSION:7\\n#EXT-X-TARGETDURATION:3\\n#EXT-X-MEDIA-SEQUENCE:9\\n#EXT-X-MAP:URI=\"init.mp4\"\\n#EXTINF:2.669000,\\nseg_00009.m4s\\n#EXTINF:1.669000,\\nseg_00010.m4s\\n#EXTINF:1.668000,\\nseg_00011.m4s\\n"
	ffmpeg := `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "framecrc" ]; then
    printf '%s\n' '#tb 0: 1/1000'
    printf '%s\n' '0, 18000, 18000, 41, 1024, 0x12345678'
    exit 0
  fi
done
printf '%b' '` + manifest + `' > '` + filepath.Join(dir, "stream.m3u8") + `'
exec sleep 30
`
	if err := os.WriteFile(ffmpegPath, []byte(ffmpeg), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}

	const sessionID = "copy-node-recovery"
	session, err := playback.StartTranscode(context.Background(), playback.TranscodeOpts{
		SessionID:              sessionID,
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
	})
	if err != nil {
		t.Fatalf("StartTranscode: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	deadline := time.Now().Add(2 * time.Second)
	for {
		if data, readErr := os.ReadFile(filepath.Join(dir, "stream.m3u8")); readErr == nil && bytes.Contains(data, []byte("seg_00011.m4s\n")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initial fake transcode did not publish its manifest")
		}
		time.Sleep(time.Millisecond)
	}

	server := &Server{sessions: map[string]*playback.TranscodeSession{sessionID: session}}
	target, ok, err := server.restartSegmentLocked(context.Background(), sessionID, session, 10)
	if err != nil {
		t.Fatalf("restartSegmentLocked: %v", err)
	}
	if !ok {
		t.Fatal("restartSegmentLocked returned ok=false")
	}
	if math.Abs(target.SeekSeconds-20.669) > 0.0001 || math.Abs(target.StreamOriginSeconds-18) > 0.0001 ||
		target.StartSegmentNumber != 9 || !target.CopySeekAnchorResolved {
		t.Fatalf("restart target = %+v, want seek=20.669 origin=18 start=9 resolved=true", target)
	}
	opts := session.Opts()
	if opts.StartSegmentNumber != 9 || !opts.CopySeekAnchorResolved || math.Abs(opts.StreamOriginSeconds-18) > 0.0001 {
		t.Fatalf("node recovery opts = %+v, want start=9 origin=18 resolved=true", opts)
	}
}

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
