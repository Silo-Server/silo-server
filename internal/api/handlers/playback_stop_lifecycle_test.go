package handlers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestStopPlaybackSessionFinalizesUnderLifecycleLock(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	session := &playback.Session{ID: "stop-lifecycle", UserID: 1, PlayMethod: playback.PlayTranscode}
	manager.RegisterReconstructed(session)
	handler := NewPlaybackHandler(manager)
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nread line\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	encoder, err := playback.StartTranscode(t.Context(), playback.TranscodeOpts{
		SessionID: session.ID, InputPath: "/input.mkv", OutputDir: t.TempDir(),
		FFmpegPath: bin, TargetCodecVideo: "copy", TargetCodecAudio: "copy",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = encoder.Close() })
	handler.tm.RegisterTranscodeSession(session.ID, encoder)
	done := make(chan error, 1)
	go func() {
		_, err := handler.stopPlaybackSessionWithResult(context.Background(), session, false)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop finalization reacquired its already-held lifecycle lock")
	}
	if encoder.IsRunning() || handler.tm.GetTranscodeSession(session.ID) != nil {
		t.Fatal("stop finalization left the encoder running or registered")
	}
	select {
	case <-encoder.Done():
	default:
		t.Fatal("stop finalization returned before reaping the encoder")
	}
}

func TestLocalEncoderCrashFinalizesUnderLifecycleLock(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	session := &playback.Session{ID: "crash-lifecycle", UserID: 1, PlayMethod: playback.PlayTranscode}
	manager.RegisterReconstructed(session)
	handler := NewPlaybackHandler(manager)
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	encoder, err := playback.StartTranscode(t.Context(), playback.TranscodeOpts{
		SessionID: session.ID, InputPath: "/input.mkv", OutputDir: t.TempDir(),
		FFmpegPath: bin, TargetCodecVideo: "copy", TargetCodecAudio: "copy",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = encoder.Close() })
	handler.tm.RegisterTranscodeSession(session.ID, encoder)
	callback := handler.tm.OnFFmpegCrash
	finished := make(chan struct{})
	handler.tm.OnFFmpegCrash = func(ctx context.Context, id string, dead *playback.TranscodeSession) {
		callback(ctx, id, dead)
		close(finished)
	}
	handler.tm.MonitorLocalTranscodeExit(session.ID, encoder)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("crash callback reacquired its already-held lifecycle lock")
	}
	if _, err := manager.GetSession(session.ID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("crash callback did not stop the playback session: %v", err)
	}
	if handler.tm.GetTranscodeSession(session.ID) != nil {
		t.Fatal("crash callback left the encoder registered")
	}
}
