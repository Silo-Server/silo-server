package playback

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBuildPlaybackManifest_CopyVideoUsesRealManifest(t *testing.T) {
	tempDir := t.TempDir()
	manifest := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-TARGETDURATION:3",
		"#EXT-X-MEDIA-SEQUENCE:9",
		"#EXT-X-INDEPENDENT-SEGMENTS",
		"#EXT-X-MAP:URI=\"init.mp4\"",
		"#EXTINF:2.669000,",
		"seg_00009.m4s",
		"#EXTINF:1.669000,",
		"seg_00010.m4s",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(tempDir, "stream.m3u8"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	// Create segment files so startupFilesReady passes.
	for _, name := range []string{"init.mp4", "seg_00009.m4s", "seg_00010.m4s"} {
		if err := os.WriteFile(filepath.Join(tempDir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	session := &TranscodeSession{
		outputDir: tempDir,
		opts: TranscodeOpts{
			TargetCodecVideo: "copy",
			TargetCodecAudio: "aac",
			SegmentDuration:  2,
			TotalDuration:    10,
		},
	}

	got, err := session.BuildPlaybackManifest("segment/", "token=test")
	if err != nil {
		t.Fatalf("BuildPlaybackManifest: %v", err)
	}

	text := string(got)
	for _, want := range []string{
		"#EXT-X-MEDIA-SEQUENCE:9",
		"#EXTINF:2.669000,",
		"#EXTINF:1.669000,",
		"#EXT-X-MAP:URI=\"segment/init.mp4?token=test\"",
		"segment/seg_00009.m4s?token=test",
		"segment/seg_00010.m4s?token=test",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "#EXT-X-PLAYLIST-TYPE:VOD") {
		t.Fatalf("copy-mode manifest should not be synthetic VOD:\n%s", text)
	}
}

func TestBuildPlaybackManifest_CopyVideoWithoutDurationUsesRealManifest(t *testing.T) {
	tempDir := t.TempDir()
	manifest := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-TARGETDURATION:3",
		"#EXT-X-MEDIA-SEQUENCE:9",
		"#EXT-X-INDEPENDENT-SEGMENTS",
		"#EXT-X-MAP:URI=\"init.mp4\"",
		"#EXTINF:2.669000,",
		"seg_00009.m4s",
		"#EXTINF:1.669000,",
		"seg_00010.m4s",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(tempDir, "stream.m3u8"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	for _, name := range []string{"init.mp4", "seg_00009.m4s", "seg_00010.m4s"} {
		if err := os.WriteFile(filepath.Join(tempDir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	session := &TranscodeSession{
		outputDir: tempDir,
		opts: TranscodeOpts{
			TargetCodecVideo: "copy",
			TargetCodecAudio: "aac",
			SegmentDuration:  2,
			TotalDuration:    0, // unknown duration — must fall back to real manifest
		},
	}

	got, err := session.BuildPlaybackManifest("segment/", "token=test")
	if err != nil {
		t.Fatalf("BuildPlaybackManifest: %v", err)
	}

	text := string(got)
	for _, want := range []string{
		"#EXT-X-MEDIA-SEQUENCE:9",
		"#EXTINF:2.669000,",
		"#EXTINF:1.669000,",
		"#EXT-X-MAP:URI=\"segment/init.mp4?token=test\"",
		"segment/seg_00009.m4s?token=test",
		"segment/seg_00010.m4s?token=test",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "#EXT-X-PLAYLIST-TYPE:VOD") {
		t.Fatalf("real manifest should not be synthetic VOD:\n%s", text)
	}
}

func TestBuildPlaybackManifest_EncodedTranscodeUsesSyntheticVODManifest(t *testing.T) {
	session := &TranscodeSession{
		opts: TranscodeOpts{
			TargetCodecVideo: "h264",
			TargetCodecAudio: "aac",
			SegmentDuration:  2,
			TotalDuration:    5.1,
		},
	}

	got, err := session.BuildPlaybackManifest("segment/", "token=test")
	if err != nil {
		t.Fatalf("BuildPlaybackManifest: %v", err)
	}

	text := string(got)
	for _, want := range []string{
		"#EXT-X-PLAYLIST-TYPE:VOD",
		"#EXT-X-MEDIA-SEQUENCE:0",
		"#EXTINF:2.000000,",
		"#EXTINF:1.100000,",
		"segment/seg_00000.ts?token=test",
		"segment/seg_00002.ts?token=test",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "#EXT-X-MAP:") {
		t.Fatalf("encoded manifest should not use fMP4 init map:\n%s", text)
	}
}

func TestBuildPlaybackManifest_UnknownDurationRejectsBrokenManifest(t *testing.T) {
	tempDir := t.TempDir()
	manifest := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-TARGETDURATION:0",
		"#EXT-X-MEDIA-SEQUENCE:1390",
		"#EXT-X-MAP:URI=\"init.mp4\"",
		"#EXTINF:0.000000,",
		"seg_01390.m4s",
		"#EXTINF:0.000000,",
		"seg_01391.m4s",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(tempDir, "stream.m3u8"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	for _, name := range []string{"init.mp4", "seg_01390.m4s", "seg_01391.m4s"} {
		if err := os.WriteFile(filepath.Join(tempDir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	session := &TranscodeSession{
		outputDir: tempDir,
		running:   true,
		opts: TranscodeOpts{
			TargetCodecVideo: "copy",
			TargetCodecAudio: "aac",
			SegmentDuration:  2,
			TotalDuration:    0, // unknown duration — forces real manifest path
		},
	}

	if _, err := session.BuildPlaybackManifest("segment/", "token=test"); err == nil {
		t.Fatal("expected BuildPlaybackManifest to reject zero-duration manifest")
	} else if !strings.Contains(err.Error(), "invalid copy playback manifest") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRewriteManifestPaths_RejectsInvalidManifest(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
	}{
		{name: "empty", manifest: ""},
		{name: "missing header", manifest: "#EXTINF:2.0,\nseg_00000.m4s\n"},
		{name: "bad map", manifest: "#EXTM3U\n#EXT-X-MAP:BYTERANGE=\"720@0\"\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RewriteManifestPaths([]byte(tc.manifest), "segment/", ""); err == nil {
				t.Fatalf("expected RewriteManifestPaths to fail for %s", tc.name)
			}
		})
	}
}

func TestTranscodeSession_SegmentStartTimeUsesManifestTimeline(t *testing.T) {
	tempDir := t.TempDir()
	manifest := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-TARGETDURATION:3",
		"#EXT-X-MEDIA-SEQUENCE:9",
		"#EXT-X-MAP:URI=\"init.mp4\"",
		"#EXTINF:2.669000,",
		"seg_00009.m4s",
		"#EXTINF:1.669000,",
		"seg_00010.m4s",
		"#EXTINF:1.668000,",
		"seg_00011.m4s",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(tempDir, "stream.m3u8"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	session := &TranscodeSession{
		outputDir: tempDir,
		opts: TranscodeOpts{
			SeekSeconds:      18.261,
			TargetCodecVideo: "copy",
		},
	}

	tests := []struct {
		segment int
		want    float64
	}{
		{segment: 9, want: 18.261},
		{segment: 10, want: 20.93},
		{segment: 11, want: 22.599},
	}

	for _, tc := range tests {
		got, ok, err := session.SegmentStartTime(tc.segment)
		if err != nil {
			t.Fatalf("SegmentStartTime(%d): %v", tc.segment, err)
		}
		if !ok {
			t.Fatalf("SegmentStartTime(%d) reported segment missing", tc.segment)
		}
		if math.Abs(got-tc.want) > 0.0001 {
			t.Fatalf("SegmentStartTime(%d) = %.6f, want %.6f", tc.segment, got, tc.want)
		}
	}

	if _, ok, err := session.SegmentStartTime(12); err != nil {
		t.Fatalf("SegmentStartTime(12): %v", err)
	} else if ok {
		t.Fatal("SegmentStartTime(12) should report missing segment")
	}
}

func TestRestartSeekTarget_CopyModeUsesManifestTimelineWhenAvailable(t *testing.T) {
	tempDir := t.TempDir()
	manifest := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-TARGETDURATION:3",
		"#EXT-X-MEDIA-SEQUENCE:9",
		"#EXT-X-MAP:URI=\"init.mp4\"",
		"#EXTINF:2.669000,",
		"seg_00009.m4s",
		"#EXTINF:1.669000,",
		"seg_00010.m4s",
		"#EXTINF:1.668000,",
		"seg_00011.m4s",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(tempDir, "stream.m3u8"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	session := &TranscodeSession{
		outputDir: tempDir,
		opts: TranscodeOpts{
			SeekSeconds:      18.261,
			TargetCodecVideo: "copy",
			SegmentDuration:  2,
		},
	}

	got, ok, err := session.RestartSeekTarget(10)
	if err != nil {
		t.Fatalf("RestartSeekTarget: %v", err)
	}
	if !ok {
		t.Fatal("RestartSeekTarget returned ok=false")
	}
	if math.Abs(got-20.93) > 0.0001 {
		t.Fatalf("RestartSeekTarget(10) = %.6f, want 20.93", got)
	}
}

func TestRestartSeekTarget_CopyModeFallsBackToFixedDurationWhenSegmentOutsideManifest(t *testing.T) {
	tempDir := t.TempDir()
	manifest := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-TARGETDURATION:3",
		"#EXT-X-MEDIA-SEQUENCE:9",
		"#EXT-X-MAP:URI=\"init.mp4\"",
		"#EXTINF:2.669000,",
		"seg_00009.m4s",
		"#EXTINF:1.669000,",
		"seg_00010.m4s",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(tempDir, "stream.m3u8"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	session := &TranscodeSession{
		outputDir: tempDir,
		opts: TranscodeOpts{
			SeekSeconds:      18.261,
			TargetCodecVideo: "copy",
			SegmentDuration:  2,
		},
	}

	got, ok, err := session.RestartSeekTarget(50)
	if err != nil {
		t.Fatalf("RestartSeekTarget: %v", err)
	}
	if !ok {
		t.Fatal("RestartSeekTarget returned ok=false")
	}
	if got != 100 {
		t.Fatalf("RestartSeekTarget(50) = %f, want 100", got)
	}
}

func TestRestartSeekTarget_EncodedUsesFixedDurationMath(t *testing.T) {
	session := &TranscodeSession{
		opts: TranscodeOpts{
			TargetCodecVideo: "h264",
			SegmentDuration:  2,
		},
	}

	got, ok, err := session.RestartSeekTarget(50)
	if err != nil {
		t.Fatalf("RestartSeekTarget: %v", err)
	}
	if !ok {
		t.Fatal("RestartSeekTarget returned ok=false")
	}
	if got != 100 {
		t.Fatalf("RestartSeekTarget(50) = %f, want 100", got)
	}
}

func TestRestartSeekTarget_CopyModeFallsBackWhenManifestNotReady(t *testing.T) {
	session := &TranscodeSession{
		opts: TranscodeOpts{
			TargetCodecVideo: "copy",
			SegmentDuration:  2,
		},
	}

	got, ok, err := session.RestartSeekTarget(10)
	if err != nil {
		t.Fatalf("RestartSeekTarget: %v", err)
	}
	if !ok {
		t.Fatal("RestartSeekTarget returned ok=false")
	}
	if got != 20 {
		t.Fatalf("RestartSeekTarget(10) = %f, want 20", got)
	}
}

func TestWaitForSegment_RestartingSessionReturnsNotFoundInsteadOfTranscodeFailed(t *testing.T) {
	session := &TranscodeSession{
		outputDir:  t.TempDir(),
		restarting: true,
		waitErr:    errors.New("signal: killed"),
	}

	_, err := session.WaitForSegment("seg_00010.m4s", 5*time.Millisecond)
	if !errors.Is(err, ErrSegmentNotFound) {
		t.Fatalf("WaitForSegment error = %v, want ErrSegmentNotFound", err)
	}
}

func TestSegmentProgressUsesManifestSequenceAndReadyFiles(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()
	writeManifestRange(t, tempDir, 224, 226, ".ts")
	writeSegmentFile(t, tempDir, "seg_00224.ts", []byte("x"), now.Add(-2*time.Second))
	writeSegmentFile(t, tempDir, "seg_00225.ts", []byte("x"), now.Add(-time.Second))

	session := &TranscodeSession{
		outputDir: tempDir,
		opts: TranscodeOpts{
			TargetCodecVideo:   "h264",
			SegmentDuration:    2,
			StartSegmentNumber: 224,
		},
	}

	progress := session.SegmentProgress(now)
	if progress.ProducedHead != 225 {
		t.Fatalf("ProducedHead = %d, want 225", progress.ProducedHead)
	}
	if progress.ProducedCount != 2 {
		t.Fatalf("ProducedCount = %d, want 2", progress.ProducedCount)
	}
	if !progress.HasManifest {
		t.Fatal("expected HasManifest=true")
	}
}

func TestSegmentProgressIgnoresZeroByteFiles(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()
	writeManifestRange(t, tempDir, 224, 226, ".ts")
	writeSegmentFile(t, tempDir, "seg_00224.ts", []byte("x"), now.Add(-2*time.Second))
	writeSegmentFile(t, tempDir, "seg_00225.ts", []byte("x"), now.Add(-time.Second))
	writeSegmentFile(t, tempDir, "seg_00226.ts", nil, now)

	session := &TranscodeSession{
		outputDir: tempDir,
		opts: TranscodeOpts{
			TargetCodecVideo:   "h264",
			SegmentDuration:    2,
			StartSegmentNumber: 224,
		},
	}

	progress := session.SegmentProgress(now)
	if progress.ProducedHead != 225 {
		t.Fatalf("ProducedHead = %d, want 225", progress.ProducedHead)
	}
	if progress.ProducedCount != 2 {
		t.Fatalf("ProducedCount = %d, want 2", progress.ProducedCount)
	}
}

func TestSegmentRecoveryDecisionWaitsForFreshNextSegment(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()
	writeManifestRange(t, tempDir, 224, 226, ".ts")
	writeSegmentFile(t, tempDir, "seg_00224.ts", []byte("x"), now.Add(-2*time.Second))
	writeSegmentFile(t, tempDir, "seg_00225.ts", []byte("x"), now.Add(-time.Second))

	session := &TranscodeSession{
		outputDir:            tempDir,
		running:              true,
		lastRequestedSegment: 225,
		opts: TranscodeOpts{
			TargetCodecVideo:   "h264",
			SegmentDuration:    2,
			StartSegmentNumber: 224,
		},
	}

	decision := session.SegmentRecoveryDecision(226, now)
	if !decision.Wait {
		t.Fatalf("Wait = false, want true (reason=%s)", decision.Reason)
	}
	if decision.WaitTimeout != 12*time.Second {
		t.Fatalf("WaitTimeout = %s, want 12s", decision.WaitTimeout)
	}
	if decision.Reason != "near_produced_head" {
		t.Fatalf("Reason = %q, want near_produced_head", decision.Reason)
	}
	if decision.RestartOnTimeout {
		t.Fatal("RestartOnTimeout = true, want false")
	}
}

func TestSegmentRecoveryDecisionUsesLongerWaitForStartupSegment(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()

	session := &TranscodeSession{
		outputDir: tempDir,
		running:   true,
		opts: TranscodeOpts{
			TargetCodecVideo:   "h264",
			SegmentDuration:    2,
			StartSegmentNumber: 0,
		},
	}

	decision := session.SegmentRecoveryDecision(0, now)
	if !decision.Wait {
		t.Fatalf("Wait = false, want true (reason=%s)", decision.Reason)
	}
	if decision.WaitTimeout != 12*time.Second {
		t.Fatalf("WaitTimeout = %s, want 12s", decision.WaitTimeout)
	}
	if decision.Reason != "startup_manifest_not_ready" {
		t.Fatalf("Reason = %q, want startup_manifest_not_ready", decision.Reason)
	}
	if decision.RestartOnTimeout {
		t.Fatal("RestartOnTimeout = true, want false")
	}
}

func TestSegmentRecoveryDecisionRestartsWhenProducedOutputIsStale(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()
	writeManifestRange(t, tempDir, 224, 226, ".ts")
	writeSegmentFile(t, tempDir, "seg_00224.ts", []byte("x"), now.Add(-12*time.Second))
	writeSegmentFile(t, tempDir, "seg_00225.ts", []byte("x"), now.Add(-10*time.Second))

	session := &TranscodeSession{
		outputDir:            tempDir,
		running:              true,
		lastRequestedSegment: 225,
		opts: TranscodeOpts{
			TargetCodecVideo:   "h264",
			SegmentDuration:    2,
			StartSegmentNumber: 224,
		},
	}

	decision := session.SegmentRecoveryDecision(226, now)
	if decision.Wait {
		t.Fatal("Wait = true, want false")
	}
	if !decision.RestartOnTimeout {
		t.Fatal("RestartOnTimeout = false, want true")
	}
	if decision.Reason != "produced_output_stale" {
		t.Fatalf("Reason = %q, want produced_output_stale", decision.Reason)
	}
}

func TestSegmentRecoveryDecisionRestartsForJumpAheadRequest(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()
	writeManifestRange(t, tempDir, 224, 226, ".ts")
	writeSegmentFile(t, tempDir, "seg_00224.ts", []byte("x"), now.Add(-2*time.Second))
	writeSegmentFile(t, tempDir, "seg_00225.ts", []byte("x"), now.Add(-time.Second))

	session := &TranscodeSession{
		outputDir:            tempDir,
		running:              true,
		lastRequestedSegment: 225,
		opts: TranscodeOpts{
			TargetCodecVideo:   "h264",
			SegmentDuration:    2,
			StartSegmentNumber: 224,
		},
	}

	decision := session.SegmentRecoveryDecision(261, now)
	if decision.Wait {
		t.Fatal("Wait = true, want false")
	}
	if decision.Reason != "request_beyond_produced_window" {
		t.Fatalf("Reason = %q, want request_beyond_produced_window", decision.Reason)
	}
}

func TestSegmentRecoveryDecisionDoesNotUseStaleRequestHistoryAsProducedHead(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()
	writeManifestRange(t, tempDir, 833, 833, ".ts")
	writeSegmentFile(t, tempDir, "seg_00833.ts", []byte("x"), now.Add(-time.Second))

	session := &TranscodeSession{
		outputDir:            tempDir,
		lastRequestedSegment: 2446,
		running:              true,
		opts: TranscodeOpts{
			TargetCodecVideo:   "h264",
			SegmentDuration:    2,
			StartSegmentNumber: 833,
		},
	}

	decision := session.SegmentRecoveryDecision(1597, now)
	if decision.Wait {
		t.Fatal("Wait = true, want false")
	}
	if decision.Progress.ProducedHead != 833 {
		t.Fatalf("ProducedHead = %d, want 833", decision.Progress.ProducedHead)
	}
	if decision.Reason != "request_beyond_produced_window" {
		t.Fatalf("Reason = %q, want request_beyond_produced_window", decision.Reason)
	}
}

func TestTranscodeThrottlerUsesProducedHeadForGap(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()
	writeManifestRange(t, tempDir, 225, 293, ".ts")
	for i := 225; i <= 293; i++ {
		writeSegmentFile(t, tempDir, segmentFilename(i, TranscodeOpts{TargetCodecVideo: "h264"}), []byte("x"), now)
	}

	session := &TranscodeSession{
		outputDir:            tempDir,
		running:              true,
		lastRequestedSegment: 225,
		opts: TranscodeOpts{
			TargetCodecVideo:   "h264",
			SegmentDuration:    2,
			StartSegmentNumber: 225,
		},
	}
	writer := &recordingWriteCloser{}
	throttler := NewTranscodeThrottler(session, writer, 60, 2)

	throttler.CheckOnce()
	if !throttler.paused {
		t.Fatal("expected throttler to pause")
	}
	if writer.writes != "p" {
		t.Fatalf("writes = %q, want p", writer.writes)
	}

	writeManifestRange(t, tempDir, 225, 254, ".ts")
	throttler.CheckOnce()
	if throttler.paused {
		t.Fatal("expected throttler to resume")
	}
	if writer.writes != "pu" {
		t.Fatalf("writes = %q, want pu", writer.writes)
	}
}

type recordingWriteCloser struct {
	writes string
}

func (w *recordingWriteCloser) Write(p []byte) (int, error) {
	w.writes += string(p)
	return len(p), nil
}

func (w *recordingWriteCloser) Close() error {
	return nil
}

func writeManifestRange(t *testing.T, dir string, first int, last int, ext string) {
	t.Helper()

	lines := []string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXT-X-TARGETDURATION:2",
		"#EXT-X-MEDIA-SEQUENCE:" + strconv.Itoa(first),
		"#EXT-X-INDEPENDENT-SEGMENTS",
	}
	for i := first; i <= last; i++ {
		lines = append(lines, "#EXTINF:2.000000,", fmt.Sprintf("seg_%05d%s", i, ext))
	}
	lines = append(lines, "")

	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func writeSegmentFile(t *testing.T, dir string, name string, data []byte, modTime time.Time) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatalf("write segment %s: %v", name, err)
	}
	if err := os.Chtimes(filepath.Join(dir, name), modTime, modTime); err != nil {
		t.Fatalf("chtimes segment %s: %v", name, err)
	}
}

func TestCleanStaleSegments(t *testing.T) {
	tempDir := t.TempDir()

	// Create test files.
	files := map[string]bool{
		"init.mp4":      true,  // should survive
		"stream.m3u8":   false, // should be removed
		"seg_00005.m4s": true,  // before start segment — should survive
		"seg_00006.m4s": true,  // before start segment — should survive
		"seg_00007.m4s": false, // at start segment — should be removed
		"seg_00008.m4s": false, // after start segment — should be removed
		"seg_00010.m4s": false, // after start segment — should be removed
		"something.txt": true,  // non-segment file — should survive
	}

	for name := range files {
		if err := os.WriteFile(filepath.Join(tempDir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	session := &TranscodeSession{
		outputDir: tempDir,
		opts: TranscodeOpts{
			TargetCodecVideo: "copy",
		},
	}

	session.cleanStaleSegments(7)

	for name, shouldExist := range files {
		_, err := os.Stat(filepath.Join(tempDir, name))
		exists := err == nil
		if exists != shouldExist {
			if shouldExist {
				t.Errorf("expected %s to survive cleanup, but it was removed", name)
			} else {
				t.Errorf("expected %s to be removed, but it still exists", name)
			}
		}
	}
}
