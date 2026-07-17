package scanner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDurationFromProbeMetadataUsesReasonableVideoDuration(t *testing.T) {
	t.Parallel()

	raw := &ffprobeOutput{
		Format: ffprobeFormat{Duration: "1745764.949333"},
		Streams: []ffprobeStream{
			{CodecType: "video", Duration: "4077.708452"},
			{CodecType: "audio", Duration: "1745764.949333"},
		},
	}

	got, ok := durationFromProbeMetadata(raw)
	if !ok || got != 4077 {
		t.Fatalf("durationFromProbeMetadata() = %d, %v; want 4077, true", got, ok)
	}
}

func TestDurationFromProbeMetadataRemovesAbsoluteTimestampOffset(t *testing.T) {
	t.Parallel()

	raw := &ffprobeOutput{
		Format: ffprobeFormat{
			StartTime: "1514891.405000",
			Duration:  "1520667.605000",
		},
		Streams: []ffprobeStream{{
			CodecType: "video",
			StartTime: "1514891.405000",
		}},
	}

	got, ok := durationFromProbeMetadata(raw)
	if !ok || got != 5776 {
		t.Fatalf("durationFromProbeMetadata() = %d, %v; want 5776, true", got, ok)
	}
}

func TestDurationFromProbeMetadataRejectsAbsurdSubtitleTimeline(t *testing.T) {
	t.Parallel()

	raw := &ffprobeOutput{
		Format: ffprobeFormat{Duration: "4298357.248000"},
		Streams: []ffprobeStream{
			{CodecType: "video", AvgFrameRate: "30000/1001"},
			{CodecType: "subtitle", Duration: "4298357.248000"},
		},
	}

	got, ok := durationFromProbeMetadata(raw)
	if ok || got != 0 {
		t.Fatalf("durationFromProbeMetadata() = %d, %v; want 0, false", got, ok)
	}
}

func TestDurationFromProbeMetadataRejectsImplausiblyShortLargeVideo(t *testing.T) {
	t.Parallel()

	raw := &ffprobeOutput{
		Format: ffprobeFormat{
			Duration: "3.022000",
			Size:     "2022705152",
		},
		Streams: []ffprobeStream{{
			CodecType:    "video",
			AvgFrameRate: "30/1",
		}},
	}

	got, ok := durationFromProbeMetadata(raw)
	if ok || got != 0 {
		t.Fatalf("durationFromProbeMetadata() = %d, %v; want 0, false", got, ok)
	}
}

func TestDurationFromProbeMetadataKeepsLongAudioDurationInSeconds(t *testing.T) {
	t.Parallel()

	raw := &ffprobeOutput{
		Format:  ffprobeFormat{Duration: "108000.000000"},
		Streams: []ffprobeStream{{CodecType: "audio"}},
	}

	got, ok := durationFromProbeMetadata(raw)
	if !ok || got != 108000 {
		t.Fatalf("durationFromProbeMetadata() = %d, %v; want 108000, true", got, ok)
	}
}

func TestEstimateVideoPacketDurationUsesPacketSpan(t *testing.T) {
	t.Parallel()

	packets := strings.NewReader("1514891.405000\n1515000.000000\n1520667.605000\n")
	got := estimateVideoPacketDuration(packets, "30000/1001")
	if got != 5776 {
		t.Fatalf("estimateVideoPacketDuration() = %d, want 5776", got)
	}
}

func TestEstimateVideoPacketDurationUsesFrameCountForCollapsedTimestamps(t *testing.T) {
	t.Parallel()

	var packets strings.Builder
	for i := 0; i < 300; i++ {
		packets.WriteString("3.022000\n")
	}

	got := estimateVideoPacketDuration(strings.NewReader(packets.String()), "30/1")
	if got != 10 {
		t.Fatalf("estimateVideoPacketDuration() = %d, want 10", got)
	}
}

func TestProbeFileFallsBackToVideoPacketsForInvalidMetadata(t *testing.T) {
	t.Parallel()

	ffprobePath := filepath.Join(t.TempDir(), "ffprobe")
	script := `#!/bin/sh
case " $* " in
  *" -show_format "*)
    printf '%s\n' '{"format":{"duration":"4298357.248000","size":"1258000000"},"streams":[{"codec_type":"video","avg_frame_rate":"30/1"},{"codec_type":"subtitle","duration":"4298357.248000"}]}'
    ;;
  *)
    i=0
    while [ "$i" -lt 300 ]; do
      printf '%s\n' '3.022000'
      i=$((i + 1))
    done
    ;;
esac
`
	if err := os.WriteFile(ffprobePath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake ffprobe: %v", err)
	}

	probe, err := ProbeFile(context.Background(), ffprobePath, "broken.mkv")
	if err != nil {
		t.Fatalf("ProbeFile() returned error: %v", err)
	}
	if probe.Duration != 10 {
		t.Fatalf("ProbeFile() duration = %d, want 10", probe.Duration)
	}
}

// A failed packet fallback must not discard the codec/track metadata that
// already parsed successfully; the file imports with an unknown duration and
// the repair layer retries later.
func TestProbeFileKeepsMetadataWhenPacketFallbackFails(t *testing.T) {
	t.Parallel()

	ffprobePath := filepath.Join(t.TempDir(), "ffprobe")
	script := `#!/bin/sh
case " $* " in
  *" -show_format "*)
    printf '%s\n' '{"format":{"duration":"4298357.248000","size":"1258000000"},"streams":[{"codec_type":"video","codec_name":"h264","avg_frame_rate":"30/1"}]}'
    ;;
  *)
    exit 1
    ;;
esac
`
	if err := os.WriteFile(ffprobePath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake ffprobe: %v", err)
	}

	probe, err := ProbeFile(context.Background(), ffprobePath, "broken.mkv")
	if err != nil {
		t.Fatalf("ProbeFile() returned error: %v", err)
	}
	if probe.Duration != 0 {
		t.Fatalf("ProbeFile() duration = %d, want 0 (unknown)", probe.Duration)
	}
	if probe.CodecVideo != "h264" {
		t.Fatalf("ProbeFile() codec = %q, want parsed metadata to survive the failed fallback", probe.CodecVideo)
	}
}

// Embedded cover art is reported by ffprobe as a video stream; it must not
// route audiobooks and music through the video duration gauntlet.
func TestDurationFromProbeMetadataIgnoresCoverArtVideoStream(t *testing.T) {
	t.Parallel()

	raw := &ffprobeOutput{
		Format: ffprobeFormat{Duration: "108000.000000"},
		Streams: []ffprobeStream{
			{CodecType: "audio"},
			{CodecType: "video", CodecName: "mjpeg", Disposition: ffprobeDisp{AttachedPic: 1}},
		},
	}

	got, ok := durationFromProbeMetadata(raw)
	if !ok || got != 108000 {
		t.Fatalf("durationFromProbeMetadata() = %d, %v; want 108000, true", got, ok)
	}
}

func TestDurationFromProbeMetadataRejectsAbsurdAudioDuration(t *testing.T) {
	t.Parallel()

	raw := &ffprobeOutput{
		Format:  ffprobeFormat{Duration: "4298357.248000"},
		Streams: []ffprobeStream{{CodecType: "audio"}},
	}

	got, ok := durationFromProbeMetadata(raw)
	if ok || got != 0 {
		t.Fatalf("durationFromProbeMetadata() = %d, %v; want 0, false", got, ok)
	}
}

// The end-minus-start fallbacks must apply the same plausibility guard as the
// direct duration paths: a multi-GB video whose absolute timestamps collapse
// to a few seconds needs the packet fallback, not a persisted 3s duration.
func TestDurationFromProbeMetadataRejectsCollapsedTimestampSpanForLargeVideo(t *testing.T) {
	t.Parallel()

	raw := &ffprobeOutput{
		Format: ffprobeFormat{Size: "2022705152"},
		Streams: []ffprobeStream{{
			CodecType: "video",
			StartTime: "1514891.405000",
			Duration:  "1514894.405000",
		}},
	}

	got, ok := durationFromProbeMetadata(raw)
	if ok || got != 0 {
		t.Fatalf("durationFromProbeMetadata() = %d, %v; want 0, false", got, ok)
	}
}
