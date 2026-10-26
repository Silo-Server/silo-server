package playback

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSeparateAudioHLSRecoveryKeepsAudioURIMeaning(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg recovery integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	source := filepath.Join(t.TempDir(), "source.mkv")
	if out, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=64x64:rate=24", "-f", "lavfi", "-i", "anoisesrc=sample_rate=48000:seed=1234", "-t", "16", "-c:v", "libx264", "-g", "48", "-c:a", "eac3", "-ac", "6", source).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	for _, origin := range []float64{0, 2} {
		t.Run(time.Duration(origin*float64(time.Second)).String(), func(t *testing.T) {
			dir := t.TempDir()
			s, err := StartTranscode(ctx, TranscodeOpts{InputPath: source, OutputDir: dir, FFmpegPath: ffmpeg, TargetCodecVideo: "copy", SourceVideoCodec: "h264", TargetCodecAudio: "aac", SourceAudioChannels: 6, SeparateAudioHLS: true, SegmentDuration: 2, SeekSeconds: origin, SegmentRetentionSeconds: 1})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			select {
			case <-s.Done():
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			first := filepath.Join(dir, "audio_00000.ts")
			before, err := os.ReadFile(first)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Restart(ctx, origin+4, 2); err != nil {
				t.Fatal(err)
			}
			select {
			case <-s.Done():
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			after, err := os.ReadFile(first)
			if err != nil {
				t.Fatal("original audio URI disappeared", err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("audio URI changed bytes or timestamps across video recovery")
			}
			checkSeparateAudioRecovery(t, ctx, dir, s)
		})
	}
}

func checkSeparateAudioRecovery(t *testing.T, ctx context.Context, dir string, s *TranscodeSession) {
	t.Helper()
	manager := NewTranscodeManager()
	manager.RegisterTranscodeSession("audio", s)
	for _, number := range []int{2, 0} {
		name := fmt.Sprintf("audio_%05d.ts", number)
		lease, err := s.OpenMediaSegment(name)
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if number == 2 {
			old := time.Now().Add(-time.Minute)
			if err := os.Chtimes(filepath.Join(dir, name), old, old); err != nil {
				t.Fatal(err)
			}
			// Age the fixture without waiting for the production freshness guard.
			s.mu.Lock()
			s.generationStartedAt = old.Add(-time.Second)
			s.mu.Unlock()
			timeline, _, err := s.audioTimelineSnapshot(dir)
			if err != nil {
				t.Fatal(err)
			}
			head := timeline.entries[len(timeline.entries)-1].number
			s.ReportMediaSegmentDownloadedForGeneration(fmt.Sprintf("audio_%05d.ts", head), lease.Generation)
			waitForPrunerFileMissing(t, filepath.Join(dir, name))
		} else if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
		errs := make(chan error, 4)
		for range 4 {
			go func() { errs <- manager.RecoverAudioSegmentLocked(ctx, "audio", s, name) }()
		}
		for range 4 {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		recovered, err := s.OpenMediaSegment(name)
		if err != nil {
			t.Fatalf("evicted %s did not recover: %v", name, err)
		}
		got, err := io.ReadAll(recovered.File)
		if err != nil {
			t.Fatal(err)
		}
		if recovered.Generation != lease.Generation+1 || !bytes.Equal(want, got) {
			t.Fatalf("evicted %s changed source bytes or did not replace generation", name)
		}
		_ = recovered.Close()
		held, err := io.ReadAll(lease.File)
		if err != nil || !bytes.Equal(want, held) {
			t.Fatalf("held audio lease changed: %v", err)
		}
		_ = lease.Close()
		select {
		case <-s.Done():
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
