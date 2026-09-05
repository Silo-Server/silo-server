package playback

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func TestStartupSegmentRequirementScopesFastHardwareWindowsToFreshGenerations(t *testing.T) {
	bitmap := TranscodeOpts{
		TargetCodecVideo:   "h264",
		SubtitleBurnIn:     true,
		SubtitleTrackIndex: 0,
		SubtitleCodec:      "hdmv_pgs_subtitle",
		FastStart:          true,
	}
	tests := []struct {
		name string
		opts TranscodeOpts
		want int
	}{
		{name: "fresh hardware bitmap burn in", opts: func() TranscodeOpts { o := bitmap; o.HWAccel = transcodeHWQSV; return o }(), want: 1},
		{name: "CPU bitmap burn in", opts: func() TranscodeOpts { o := bitmap; o.HWAccel = HWAccelNone; return o }(), want: 3},
		{name: "reconstructed hardware bitmap burn in", opts: func() TranscodeOpts { o := bitmap; o.HWAccel = transcodeHWQSV; o.FastStart = false; return o }(), want: 3},
		{name: "ordinary fresh hardware transcode", opts: TranscodeOpts{TargetCodecVideo: "h264", HWAccel: transcodeHWQSV, FastStart: true}, want: 2},
		{name: "ordinary hardware restart", opts: TranscodeOpts{TargetCodecVideo: "h264", HWAccel: transcodeHWQSV, FastStart: false}, want: 3},
		{name: "ordinary CPU transcode", opts: TranscodeOpts{TargetCodecVideo: "h264", HWAccel: HWAccelNone, FastStart: true}, want: 3},
		{name: "unknown backend falls back to CPU", opts: TranscodeOpts{TargetCodecVideo: "h264", HWAccel: "stale-backend", FastStart: true}, want: 3},
		{name: "copy video", opts: TranscodeOpts{TargetCodecVideo: "copy", HWAccel: transcodeHWQSV, FastStart: true}, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := startupSegmentRequirement(tt.opts); got != tt.want {
				t.Fatalf("startupSegmentRequirement() = %d, want %d", got, tt.want)
			}
		})
	}
}

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
		"#EXT-X-PLAYLIST-TYPE:EVENT",
		"#EXT-X-START:TIME-OFFSET=0.001,PRECISE=YES",
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

	tokenless, err := session.BuildPlaybackManifest("segment/", "")
	if err != nil {
		t.Fatalf("BuildPlaybackManifest tokenless: %v", err)
	}
	for _, want := range []string{
		"#EXT-X-MAP:URI=\"segment/init.mp4\"",
		"segment/seg_00009.m4s",
		"segment/seg_00010.m4s",
	} {
		if !strings.Contains(string(tokenless), want) {
			t.Fatalf("tokenless manifest missing %q:\n%s", want, tokenless)
		}
	}
	if strings.Contains(string(tokenless), "?st=") || strings.Contains(string(tokenless), "?token=") {
		t.Fatalf("tokenless manifest propagated a credential query:\n%s", tokenless)
	}
}

func TestBuildPlaybackManifest_AdvancedCopyGenerationKeepsHistoricalRemountPosition(t *testing.T) {
	tempDir := t.TempDir()
	const producedSegments = 160
	lines := []string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-TARGETDURATION:2",
		"#EXT-X-MEDIA-SEQUENCE:0",
		"#EXT-X-INDEPENDENT-SEGMENTS",
		"#EXT-X-MAP:URI=\"init.mp4\"",
	}
	for i := range producedSegments {
		lines = append(lines, "#EXTINF:2.000000,", fmt.Sprintf("seg_%05d.m4s", i))
	}
	lines = append(lines, "")
	manifestPath := filepath.Join(tempDir, "stream.m3u8")
	if err := os.WriteFile(manifestPath, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	for _, name := range []string{"init.mp4", "seg_00000.m4s", "seg_00001.m4s"} {
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
			TotalDuration:    6_519,
		},
	}
	got, err := session.BuildPlaybackManifest("segment/", "token=test")
	if err != nil {
		t.Fatalf("BuildPlaybackManifest: %v", err)
	}
	text := string(got)
	for _, want := range []string{
		"#EXT-X-PLAYLIST-TYPE:EVENT",
		"#EXT-X-START:TIME-OFFSET=0.001,PRECISE=YES",
		"segment/seg_00025.m4s?token=test",
		"segment/seg_00159.m4s?token=test",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("advanced manifest missing %q:\n%s", want, text)
		}
	}
	timeline, err := parseManifestTimeline(got)
	if err != nil {
		t.Fatalf("parse advanced manifest: %v", err)
	}
	// A 51-second remount belongs to historical segment 25, not the produced
	// edge at segment 159. The stable origin lets Media3 apply that seek after
	// rebuilding its MediaSource instead of projecting the default to the edge.
	historical := timeline.entries[int(51/2)]
	if historical.number != 25 || historical.number >= timeline.entries[len(timeline.entries)-1].number {
		t.Fatalf("historical remount segment = %d, produced edge = %d", historical.number, timeline.entries[len(timeline.entries)-1].number)
	}

	// Manifest stabilization is a pure presentation rewrite: when FFmpeg adds
	// the next segment, cadence advances by exactly one and the same stable tags
	// remain without duplication.
	lines = append(lines[:len(lines)-1], "#EXTINF:2.000000,", "seg_00160.m4s", "")
	if err := os.WriteFile(manifestPath, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("advance manifest: %v", err)
	}
	advanced, err := session.BuildPlaybackManifest("segment/", "token=test")
	if err != nil {
		t.Fatalf("BuildPlaybackManifest after cadence advance: %v", err)
	}
	advancedTimeline, err := parseManifestTimeline(advanced)
	if err != nil {
		t.Fatalf("parse advanced cadence: %v", err)
	}
	if len(advancedTimeline.entries) != len(timeline.entries)+1 || advancedTimeline.entries[len(advancedTimeline.entries)-1].number != 160 {
		t.Fatalf("advanced cadence = %d entries ending at %d", len(advancedTimeline.entries), advancedTimeline.entries[len(advancedTimeline.entries)-1].number)
	}
	if strings.Count(string(advanced), "#EXT-X-PLAYLIST-TYPE:EVENT") != 1 || strings.Count(string(advanced), "#EXT-X-START:") != 1 {
		t.Fatalf("stable tags duplicated after manifest advance:\n%s", advanced)
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
	if strings.Contains(text, "#EXT-X-PLAYLIST-TYPE:EVENT") || strings.Contains(text, "#EXT-X-START:") {
		t.Fatalf("unknown-duration manifest cannot promise append-only EVENT semantics:\n%s", text)
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

func TestGenerateFullManifestCompactsRepeatedAuthenticationQuery(t *testing.T) {
	rawQuery := "st=" + strings.Repeat("recipe", 80) + "&token=" + strings.Repeat("access", 40)
	session := &TranscodeSession{opts: TranscodeOpts{
		TargetCodecVideo: "h264",
		SegmentDuration:  1,
		TotalDuration:    300,
	}}

	manifest := string(session.GenerateFullManifest("segment/", rawQuery))
	if strings.Count(manifest, rawQuery) != 1 {
		t.Fatalf("authentication query appears %d times, want exactly one definition", strings.Count(manifest, rawQuery))
	}
	for _, want := range []string{
		"#EXT-X-VERSION:8",
		"#EXT-X-DEFINE:NAME=\"silo_query\",VALUE=\"" + rawQuery + "\"",
		"segment/seg_00000.ts?{$silo_query}",
		"segment/seg_00299.ts?{$silo_query}",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("manifest missing %q", want)
		}
	}
	legacyBytes := 300 * len("segment/seg_00000.ts?"+rawQuery+"\n")
	if len(manifest) >= legacyBytes/4 {
		t.Fatalf("compact manifest size = %d, want less than one quarter of legacy %d", len(manifest), legacyBytes)
	}
}

func TestBuildPlaybackManifest_LongEncodedTranscodeUsesRealManifest(t *testing.T) {
	tempDir := t.TempDir()
	manifest := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXT-X-TARGETDURATION:2",
		"#EXT-X-MEDIA-SEQUENCE:0",
		"#EXTINF:2.000000,",
		"seg_00000.ts",
		"#EXTINF:2.000000,",
		"seg_00001.ts",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(tempDir, "stream.m3u8"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	session := &TranscodeSession{
		outputDir: tempDir,
		opts: TranscodeOpts{
			TargetCodecVideo: "h264",
			TargetCodecAudio: "aac",
			SegmentDuration:  2,
			TotalDuration:    1_000_000,
		},
	}

	got, err := session.BuildPlaybackManifest("segment/", "token=test")
	if err != nil {
		t.Fatalf("BuildPlaybackManifest: %v", err)
	}

	text := string(got)
	if strings.Contains(text, "#EXT-X-PLAYLIST-TYPE:VOD") ||
		strings.Contains(text, "seg_499999.ts") {
		t.Fatalf("long encoded manifest should not synthesize every segment:\n%s", text)
	}
	for _, want := range []string{
		"#EXT-X-MEDIA-SEQUENCE:0",
		"segment/seg_00000.ts?token=test",
		"segment/seg_00001.ts?token=test",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest missing %q:\n%s", want, text)
		}
	}
}

func TestBuildSourceAlignedPlaybackManifestAnchorsSeekedRealPlaylist(t *testing.T) {
	tempDir := t.TempDir()
	manifest := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXT-X-TARGETDURATION:2",
		"#EXT-X-MEDIA-SEQUENCE:8",
		"#EXTINF:2.000000,",
		"seg_00008.ts",
		"#EXTINF:2.000000,",
		"seg_00009.ts",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(tempDir, "stream.m3u8"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	session := &TranscodeSession{
		outputDir: tempDir,
		opts: TranscodeOpts{
			TargetCodecVideo:   "h264",
			TargetCodecAudio:   "aac",
			SegmentDuration:    2,
			TotalDuration:      1_000_000,
			SeekSeconds:        17.3,
			StartSegmentNumber: 8,
		},
	}

	got, err := session.BuildSourceAlignedPlaybackManifest("segment/", "source_timeline=1")
	if err != nil {
		t.Fatalf("BuildSourceAlignedPlaybackManifest: %v", err)
	}
	text := string(got)
	for _, want := range []string{
		"#EXT-X-VERSION:8",
		"#EXT-X-TARGETDURATION:3",
		"#EXT-X-MEDIA-SEQUENCE:0",
		"#EXT-X-GAP\n#EXTINF:2.162500,\nsegment/source_timeline_gap.ts?source_timeline=1",
		"segment/seg_00008.ts?source_timeline=1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("source-aligned manifest missing %q:\n%s", want, text)
		}
	}
	if gap := strings.Index(text, "source_timeline_gap.ts"); gap < 0 || gap > strings.Index(text, "seg_00008.ts") {
		t.Fatalf("timeline gap must precede the first real segment:\n%s", text)
	}
	if count := strings.Count(text, "#EXT-X-GAP"); count != 8 {
		t.Fatalf("timeline gap count = %d, want 8:\n%s", count, text)
	}
}

func TestCanGenerateSyntheticManifestBoundsSegmentCount(t *testing.T) {
	if !CanGenerateSyntheticManifest(100_000, 2) {
		t.Fatal("historical 50,000-segment manifest should remain supported")
	}
	if CanGenerateSyntheticManifest(100_001, 2) {
		t.Fatal("manifest above 50,000 segments should use the real playlist")
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
			SeekSeconds:            18.261,
			StreamOriginSeconds:    18,
			CopySeekAnchorResolved: true,
			TargetCodecVideo:       "copy",
			SegmentDuration:        2,
			StartSegmentNumber:     9,
		},
	}

	got, ok, err := session.RestartSeekTarget(10)
	if err != nil {
		t.Fatalf("RestartSeekTarget: %v", err)
	}
	if !ok {
		t.Fatal("RestartSeekTarget returned ok=false")
	}
	if math.Abs(got-20.669) > 0.0001 {
		t.Fatalf("RestartSeekTarget(10) = %.6f, want 20.669", got)
	}
}

func TestResolveSegmentRecoveryTarget_CopyMapsActualAnchorToManifestNumber(t *testing.T) {
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

	tests := []struct {
		name             string
		anchorMillis     int
		wantStartSegment int
		wantOrigin       float64
	}{
		{name: "Matroska pre-roll uses preceding manifest slot", anchorMillis: 18000, wantStartSegment: 9, wantOrigin: 18},
		{name: "MP4 exact seek keeps requested manifest slot", anchorMillis: 20669, wantStartSegment: 10, wantOrigin: 20.669},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(tempDir, "stream.m3u8"), []byte(manifest), 0o644); err != nil {
				t.Fatalf("write manifest: %v", err)
			}
			ffmpegPath := filepath.Join(tempDir, "ffmpeg")
			probe := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '#tb 0: 1/1000'\nprintf '%%s\\n' '0, %d, %d, 41, 1024, 0x12345678'\n", tt.anchorMillis, tt.anchorMillis)
			if err := os.WriteFile(ffmpegPath, []byte(probe), 0o755); err != nil {
				t.Fatalf("write fake ffmpeg: %v", err)
			}

			session := &TranscodeSession{
				outputDir: tempDir,
				opts: TranscodeOpts{
					InputPath:              "/media/movie.mkv",
					FFmpegPath:             ffmpegPath,
					SeekSeconds:            18.261,
					StreamOriginSeconds:    18,
					CopySeekAnchorResolved: true,
					TargetCodecVideo:       "copy",
					SegmentDuration:        2,
					StartSegmentNumber:     9,
				},
			}

			target, ok, err := session.ResolveSegmentRecoveryTarget(context.Background(), 10)
			if err != nil {
				t.Fatalf("ResolveSegmentRecoveryTarget: %v", err)
			}
			if !ok {
				t.Fatal("ResolveSegmentRecoveryTarget returned ok=false")
			}
			if math.Abs(target.SeekSeconds-20.669) > 0.0001 {
				t.Fatalf("SeekSeconds = %.6f, want 20.669", target.SeekSeconds)
			}
			if target.StartSegmentNumber != tt.wantStartSegment {
				t.Fatalf("StartSegmentNumber = %d, want %d", target.StartSegmentNumber, tt.wantStartSegment)
			}
			if math.Abs(target.StreamOriginSeconds-tt.wantOrigin) > 0.0001 || !target.CopySeekAnchorResolved {
				t.Fatalf("copy anchor = %.6f resolved=%v, want %.6f resolved=true", target.StreamOriginSeconds, target.CopySeekAnchorResolved, tt.wantOrigin)
			}
		})
	}
}

func TestRestartSeekTarget_CopyModeReportsUnresolvedWhenSegmentOutsideManifest(t *testing.T) {
	// Copy-mode fragments have variable durations, so a segment the manifest
	// can't yet place must NOT fall back to fixed-duration seg×dur math (which
	// would seek FFmpeg to the wrong source time and desync A/V). Instead it
	// reports the seek target as unresolved (0, false, nil).
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
	if ok {
		t.Fatalf("RestartSeekTarget(50) returned ok=true (got %f); copy-mode should report unresolved, not seg×dur", got)
	}
	if got != 0 {
		t.Fatalf("RestartSeekTarget(50) = %f, want 0", got)
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

func TestRestartSeekTarget_CopyModeReportsUnresolvedWhenManifestNotReady(t *testing.T) {
	// A freshly reconstructed copy-mode window has a near-empty/absent manifest
	// (ErrManifestNotReady). The seek target must be reported unresolved
	// (0, false, nil) so the caller retries instead of seeking to a fabricated
	// seg×dur position.
	session := &TranscodeSession{
		outputDir: t.TempDir(), // no stream.m3u8 written -> ErrManifestNotReady
		opts: TranscodeOpts{
			TargetCodecVideo: "copy",
			SegmentDuration:  2,
		},
	}

	got, ok, err := session.RestartSeekTarget(10)
	if err != nil {
		t.Fatalf("RestartSeekTarget: %v", err)
	}
	if ok {
		t.Fatalf("RestartSeekTarget(10) returned ok=true (got %f); copy-mode should report unresolved when manifest not ready", got)
	}
	if got != 0 {
		t.Fatalf("RestartSeekTarget(10) = %f, want 0", got)
	}
}

func TestWaitForSegment_RestartingSessionReturnsNotFoundInsteadOfTranscodeFailed(t *testing.T) {
	session := &TranscodeSession{
		outputDir:  t.TempDir(),
		restarting: &restartFlight{done: make(chan struct{})},
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

func TestCleanStaleOutputForRestart_CopyToToneMapEncodeRemovesStaleOutput(t *testing.T) {
	tempDir := t.TempDir()

	files := map[string]bool{
		"init.mp4":      true,  // codec config is source-derived — survives
		"stream.m3u8":   false, // describes the copy stream — removed
		"seg_00005.m4s": true,  // before the restart point — survives
		"seg_00006.m4s": true,  // before the restart point — survives
		"seg_00007.m4s": false, // copy bitstream at/after restart — removed
		"seg_00120.m4s": false, // copy bitstream far ahead — removed
	}
	for name := range files {
		if err := os.WriteFile(filepath.Join(tempDir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	previous := TranscodeOpts{TargetCodecVideo: "copy", SegmentDuration: 2}
	next := TranscodeOpts{
		TargetCodecVideo: "h264",
		SegmentDuration:  2,
		HWAccel:          "qsv",
		ToneMapMode:      tonemap.ModeHardware,
		ToneMapFilter:    "tonemap_opencl",
	}

	session := &TranscodeSession{outputDir: tempDir, opts: previous}

	if !session.cleanStaleOutputForRestart(previous, next, 7) {
		t.Fatal("cleanStaleOutputForRestart = false, want true for a copy -> tone-map restart")
	}

	for name, shouldExist := range files {
		_, err := os.Stat(filepath.Join(tempDir, name))
		if exists := err == nil; exists != shouldExist {
			if shouldExist {
				t.Errorf("expected %s to survive cleanup, but it was removed", name)
			} else {
				t.Errorf("expected %s to be removed, but it still exists", name)
			}
		}
	}
}

func TestCleanStaleOutputForRestart_ToneMapModeChangeRemovesStaleOutput(t *testing.T) {
	tempDir := t.TempDir()
	writeManifestRange(t, tempDir, 7, 9, ".ts")
	if err := os.WriteFile(filepath.Join(tempDir, "seg_00008.ts"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write segment: %v", err)
	}

	previous := TranscodeOpts{TargetCodecVideo: "h264", HWAccel: "qsv", ToneMapMode: tonemap.ModeHardware, ToneMapFilter: "tonemap_opencl"}
	next := TranscodeOpts{TargetCodecVideo: "h264", HWAccel: HWAccelNone, ToneMapMode: tonemap.ModeSoftware, ToneMapFilter: "tonemap"}

	session := &TranscodeSession{outputDir: tempDir, opts: previous}

	if !session.cleanStaleOutputForRestart(previous, next, 7) {
		t.Fatal("cleanStaleOutputForRestart = false, want true for a tone-map mode change")
	}
	if _, err := os.Stat(filepath.Join(tempDir, "stream.m3u8")); err == nil {
		t.Error("expected the previous generation's manifest to be removed")
	}
	if _, err := os.Stat(filepath.Join(tempDir, "seg_00008.ts")); err == nil {
		t.Error("expected the previous generation's segment to be removed")
	}
}

func TestCleanStaleOutputForRestart_SameEncodedRecipeKeepsSegments(t *testing.T) {
	tempDir := t.TempDir()
	writeManifestRange(t, tempDir, 7, 9, ".ts")
	if err := os.WriteFile(filepath.Join(tempDir, "seg_00008.ts"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write segment: %v", err)
	}

	opts := TranscodeOpts{TargetCodecVideo: "h264", HWAccel: "qsv", ToneMapMode: tonemap.ModeHardware, ToneMapFilter: "tonemap_opencl"}
	session := &TranscodeSession{outputDir: tempDir, opts: opts}

	// A backward seek within one generation keeps its segments reusable.
	if session.cleanStaleOutputForRestart(opts, opts, 7) {
		t.Fatal("cleanStaleOutputForRestart = true, want false for an unchanged encoded recipe")
	}
	if _, err := os.Stat(filepath.Join(tempDir, "stream.m3u8")); err != nil {
		t.Errorf("expected the manifest to survive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "seg_00008.ts")); err != nil {
		t.Errorf("expected seg_00008.ts to survive: %v", err)
	}
}

func TestTranscodeThrottlerIgnoresOutputFromAnEarlierGeneration(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()
	staleTime := now.Add(-time.Minute)

	// A previous copy generation raced hundreds of segments ahead and left its
	// manifest behind; the current process has produced nothing yet.
	writeManifestRange(t, tempDir, 225, 293, ".ts")
	if err := os.Chtimes(filepath.Join(tempDir, "stream.m3u8"), staleTime, staleTime); err != nil {
		t.Fatalf("chtimes manifest: %v", err)
	}
	for i := 225; i <= 293; i++ {
		writeSegmentFile(t, tempDir, segmentFilename(i, TranscodeOpts{TargetCodecVideo: "h264"}), []byte("x"), staleTime)
	}

	session := &TranscodeSession{
		outputDir:            tempDir,
		running:              true,
		lastRequestedSegment: 225,
		generationStartedAt:  now,
		opts: TranscodeOpts{
			TargetCodecVideo:   "h264",
			SegmentDuration:    2,
			StartSegmentNumber: 225,
		},
	}
	writer := &recordingWriteCloser{}
	throttler := NewTranscodeThrottler(session, writer, 60, 2)

	throttler.CheckOnce()
	if throttler.paused {
		t.Fatal("throttler paused on output produced before the current generation started")
	}
	if writer.writes != "" {
		t.Fatalf("writes = %q, want no command", writer.writes)
	}

	// A throttler that already paused on stale output must let ffmpeg go again.
	throttler.paused = true
	throttler.CheckOnce()
	if throttler.paused {
		t.Fatal("throttler stayed paused on stale output")
	}
	if writer.writes != "u" {
		t.Fatalf("writes = %q, want u", writer.writes)
	}

	// Once this generation writes its own manifest, throttling works normally.
	//
	// The mtime is set rather than inherited from the write. Staleness is
	// decided by ManifestModTime.Before(GenerationStartedAt), and a filesystem
	// with coarse mtime granularity — which a CI runner's overlay can have and a
	// developer's APFS does not — truncates a write made at `now` back below it,
	// so the fresh manifest reads as older than the generation that produced it.
	fresh := now.Add(time.Second)
	writeManifestRange(t, tempDir, 225, 293, ".ts")
	if err := os.Chtimes(filepath.Join(tempDir, "stream.m3u8"), fresh, fresh); err != nil {
		t.Fatalf("chtimes manifest: %v", err)
	}
	throttler.CheckOnce()
	if !throttler.paused {
		t.Fatal("expected throttler to pause on this generation's own produced head")
	}
	if writer.writes != "up" {
		t.Fatalf("writes = %q, want up", writer.writes)
	}
}

func TestAppendManifestQueryParam(t *testing.T) {
	manifest := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-TARGETDURATION:2",
		"#EXT-X-MAP:URI=\"segment/init.mp4\"",
		"#EXTINF:2.000000,",
		"segment/seg_00000.ts",
		"#EXTINF:2.000000,",
		"segment/seg_00001.ts?existing=1",
		"",
	}, "\n")

	got := string(AppendManifestQueryParam([]byte(manifest), "st", "TOKEN"))
	for _, want := range []string{
		"#EXT-X-MAP:URI=\"segment/init.mp4?st=TOKEN\"",
		"segment/seg_00000.ts?st=TOKEN",
		"segment/seg_00001.ts?existing=1&st=TOKEN",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rewritten manifest missing %q:\n%s", want, got)
		}
	}
	// Tags and EXTINF lines must be untouched.
	if !strings.Contains(got, "#EXT-X-TARGETDURATION:2") || strings.Contains(got, "#EXT-X-TARGETDURATION:2?st") {
		t.Fatalf("non-URI tag was rewritten:\n%s", got)
	}
}

func TestAppendManifestQueryParam_NonManifestUnchanged(t *testing.T) {
	body := []byte("not a manifest\nsegment/seg_00000.ts\n")
	if got := AppendManifestQueryParam(body, "st", "TOKEN"); string(got) != string(body) {
		t.Fatalf("non-manifest body should be unchanged, got:\n%s", got)
	}
	if got := AppendManifestQueryParam([]byte("#EXTM3U\nseg.ts\n"), "", "TOKEN"); !strings.Contains(string(got), "seg.ts\n") || strings.Contains(string(got), "seg.ts?") {
		t.Fatalf("empty key should be a no-op, got:\n%s", got)
	}
}
