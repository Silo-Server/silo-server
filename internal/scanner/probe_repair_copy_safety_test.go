package scanner

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestEnsureCopySafetyFailureDisablesCopyWithoutCaching(t *testing.T) {
	ensurer := &PlaybackProbeEnsurer{
		ffmpegPath: filepath.Join(t.TempDir(), "missing-ffmpeg"),
	}
	file := &models.MediaFile{
		ID:          42,
		FilePath:    "/library/movie.mkv",
		FileSize:    1234,
		CodecVideo:  "h264",
		VideoTracks: []models.VideoTrack{{Codec: "h264"}},
	}

	got, err := ensurer.ensureCopySafety(context.Background(), file)
	if err != nil {
		t.Fatalf("ensureCopySafety() error = %v", err)
	}
	if got == file {
		t.Fatal("ensureCopySafety() returned the caller's file instead of an annotated copy")
	}
	track := got.VideoTracks[0]
	if !track.VideoCopyUnsafe {
		t.Fatal("VideoCopyUnsafe = false, want true after an inconclusive scan")
	}
	if track.MultiplePPS != nil {
		t.Fatalf("MultiplePPS = %v, want nil when the scan did not produce a result", *track.MultiplePPS)
	}
	if _, ok := ensurer.copySafety.Load(file.ID); ok {
		t.Fatal("inconclusive scan result was cached")
	}
	if file.VideoTracks[0].VideoCopyUnsafe || file.VideoTracks[0].MultiplePPS != nil {
		t.Fatal("ensureCopySafety() mutated the caller's file")
	}
}
