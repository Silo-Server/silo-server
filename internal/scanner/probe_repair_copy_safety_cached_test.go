package scanner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// EnsureCopySafetyCached is the optimistic half of the remux race: a play must
// never wait on the bitstream scan, so an unknown verdict is left unknown —
// which the planner reads as "stream copy is allowed" — and no ffmpeg runs.
func TestEnsureCopySafetyCachedNeverScans(t *testing.T) {
	ffmpegPath, runs := fakeFFmpeg(t, conflictingPPSAnnexB, 0)
	writer := &fakeCopySafetyWriter{}
	ensurer := &PlaybackProbeEnsurer{ffmpegPath: ffmpegPath, copySafetyRepo: writer}

	file := copySafetyTestFile(time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC))

	got, err := ensurer.EnsureCopySafetyCached(context.Background(), file)
	if err != nil {
		t.Fatalf("EnsureCopySafetyCached() error = %v", err)
	}
	if runs() != 0 {
		t.Fatalf("ffmpeg ran %d times, want 0 — the cached ensure must never exec", runs())
	}
	track := got.VideoTracks[0]
	if track.MultiplePPS != nil {
		t.Fatalf("MultiplePPS = %v, want nil so the planner may still remux optimistically", *track.MultiplePPS)
	}
	if track.VideoCopyUnsafe {
		t.Fatal("VideoCopyUnsafe = true, want false: an unresolved verdict must not disqualify the copy route")
	}
	if writes := writer.recorded(); len(writes) != 0 {
		t.Fatalf("cached ensure recorded %d verdicts, want 0", len(writes))
	}
}

// An inconclusive scan is not evidence: nothing is persisted, nothing is
// memoized, and the caller learns the scan failed rather than being handed a
// fabricated copy-unsafe verdict.
func TestScanCopySafetyErrorRecordsNothing(t *testing.T) {
	writer := &fakeCopySafetyWriter{}
	ensurer := &PlaybackProbeEnsurer{
		ffmpegPath:     filepath.Join(t.TempDir(), "missing-ffmpeg"),
		copySafetyRepo: writer,
	}

	file := copySafetyTestFile(time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC))

	multi, stale, err := ensurer.ScanCopySafety(context.Background(), file)
	if err == nil {
		t.Fatal("ScanCopySafety() error = nil, want the scan failure surfaced to the caller")
	}
	if multi || stale {
		t.Fatalf("ScanCopySafety() = (%t, %t) on error, want (false, false)", multi, stale)
	}
	if writes := writer.recorded(); len(writes) != 0 {
		t.Fatalf("failed scan recorded %d verdicts, want 0", len(writes))
	}
	if !ensurer.NeedsCopySafetyScan(file) {
		t.Fatal("NeedsCopySafetyScan() = false after a failed scan, want true so a later request retries")
	}

	got, err := ensurer.EnsureCopySafetyCached(context.Background(), file)
	if err != nil {
		t.Fatalf("EnsureCopySafetyCached() error = %v", err)
	}
	if track := got.VideoTracks[0]; track.MultiplePPS != nil || track.VideoCopyUnsafe {
		t.Fatalf("track = %+v, want the verdict left unknown after a failed scan", track)
	}
}

// SetFFmpegPath is what makes playback.ffmpeg_path take effect without a
// server restart: the ensurer is built with the path captured at boot, and a
// later config change has to reach both the probe and the copy-safety scan.
func TestSetFFmpegPathOverridesBootPaths(t *testing.T) {
	ensurer := NewPlaybackProbeEnsurer(nil, "boot-ffprobe", "boot-ffmpeg", time.Second)

	if got := ensurer.binaries(); got.ffmpegPath != "boot-ffmpeg" || got.ffprobePath != "boot-ffprobe" {
		t.Fatalf("binaries() before reload = %+v, want the boot pair", got)
	}

	ensurer.SetFFmpegPath("/opt/jellyfin-ffmpeg/ffmpeg")

	got := ensurer.binaries()
	if got.ffmpegPath != "/opt/jellyfin-ffmpeg/ffmpeg" {
		t.Fatalf("binaries().ffmpegPath = %q, want the reloaded path", got.ffmpegPath)
	}
	if want := filepath.Join("/opt/jellyfin-ffmpeg", "ffprobe"); got.ffprobePath != want {
		t.Fatalf("binaries().ffprobePath = %q, want %q derived from the reloaded ffmpeg", got.ffprobePath, want)
	}
}

// A reload that blanks the FFmpeg path disables the copy-safety scan, the same
// as booting without one — the verdict stays unknown instead of being guessed.
func TestSetFFmpegPathEmptyDisablesCopySafetyScan(t *testing.T) {
	ffmpegPath, _ := fakeFFmpeg(t, conflictingPPSAnnexB, 0)
	ensurer := NewPlaybackProbeEnsurer(nil, "ffprobe", ffmpegPath, time.Second)
	file := copySafetyTestFile(time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC))

	if !ensurer.NeedsCopySafetyScan(file) {
		t.Fatal("NeedsCopySafetyScan() = false with a configured ffmpeg, want true")
	}

	ensurer.SetFFmpegPath("")

	if ensurer.NeedsCopySafetyScan(file) {
		t.Fatal("NeedsCopySafetyScan() = true after the path was cleared, want false")
	}
	if _, _, err := ensurer.ScanCopySafety(context.Background(), file); !errors.Is(err, errCopySafetyScanUnavailable) {
		t.Fatalf("ScanCopySafety() error = %v, want errCopySafetyScanUnavailable", err)
	}
}
