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

func TestDurationFromProbeMetadataKeepsCorroboratedLongVideoDuration(t *testing.T) {
	t.Parallel()

	raw := &ffprobeOutput{
		Format: ffprobeFormat{
			Duration: "182930.275000",
			Size:     "77507139196",
		},
		Streams: []ffprobeStream{{
			CodecType:    "video",
			Duration:     "182930.196000",
			AvgFrameRate: "24/1",
		}},
	}

	got, ok := durationFromProbeMetadata(raw)
	if !ok || got != 182930 {
		t.Fatalf("durationFromProbeMetadata() = %d, %v; want 182930, true", got, ok)
	}
}

func TestDurationFromProbeMetadataKeepsCorroboratedLongVideoWithOrdinaryStartTime(t *testing.T) {
	t.Parallel()

	raw := &ffprobeOutput{
		Format: ffprobeFormat{
			StartTime: "30.000000",
			Duration:  "182930.275000",
			Size:      "77507139196",
		},
		Streams: []ffprobeStream{{
			CodecType: "video",
			StartTime: "30.000000",
			Duration:  "182930.196000",
		}},
	}

	got, ok := durationFromProbeMetadata(raw)
	if !ok || got != 182930 {
		t.Fatalf("durationFromProbeMetadata() = %d, %v; want 182930, true", got, ok)
	}
}

func TestDurationFromProbeMetadataKeepsCorroboratedLongVideoWithMaterialStartTime(t *testing.T) {
	t.Parallel()

	raw := &ffprobeOutput{
		Format: ffprobeFormat{
			StartTime: "300.000000",
			Duration:  "182930.275000",
			Size:      "77507139196",
		},
		Streams: []ffprobeStream{{
			CodecType: "video",
			StartTime: "300.000000",
			Duration:  "182930.196000",
		}},
	}

	got, ok := durationFromProbeMetadata(raw)
	if !ok || got != 182930 {
		t.Fatalf("durationFromProbeMetadata() = %d, %v; want 182930, true", got, ok)
	}
}

func TestDurationFromProbeMetadataNormalizesOffsetBeforeCorroboratingLongVideo(t *testing.T) {
	t.Parallel()

	raw := &ffprobeOutput{
		Format: ffprobeFormat{
			StartTime: "180000.000000",
			Duration:  "182930.275000",
			Size:      "77507139196",
		},
		Streams: []ffprobeStream{{
			CodecType: "video",
			StartTime: "180000.000000",
			Duration:  "182930.196000",
		}},
	}

	got, ok := durationFromProbeMetadata(raw)
	if !ok || got != 2930 {
		t.Fatalf("durationFromProbeMetadata() = %d, %v; want 2930, true", got, ok)
	}
}

func TestDurationFromProbeMetadataNormalizesCorroboratedLongOffsetSpan(t *testing.T) {
	t.Parallel()

	raw := &ffprobeOutput{
		Format: ffprobeFormat{
			StartTime: "180000.000000",
			Duration:  "350000.275000",
			Size:      "77507139196",
		},
		Streams: []ffprobeStream{{
			CodecType: "video",
			StartTime: "200000.000000",
			Duration:  "370000.196000",
		}},
	}

	got, ok := durationFromProbeMetadata(raw)
	if !ok || got != 170000 {
		t.Fatalf("durationFromProbeMetadata() = %d, %v; want 170000, true", got, ok)
	}
}

func TestDurationFromProbeMetadataNormalizesMatchingLongAbsoluteEndTimestamps(t *testing.T) {
	t.Parallel()

	raw := &ffprobeOutput{
		Format: ffprobeFormat{
			StartTime: "180000.000000",
			Duration:  "350000.275000",
			Size:      "77507139196",
		},
		Streams: []ffprobeStream{{
			CodecType: "video",
			StartTime: "180000.000000",
			Duration:  "350000.196000",
		}},
	}

	got, ok := durationFromProbeMetadata(raw)
	if !ok || got != 170000 {
		t.Fatalf("durationFromProbeMetadata() = %d, %v; want 170000, true", got, ok)
	}
}

func TestDurationFromProbeMetadataRejectsDisagreeingAbsoluteEndSpans(t *testing.T) {
	t.Parallel()

	raw := &ffprobeOutput{
		Format: ffprobeFormat{
			StartTime: "180000.000000",
			Duration:  "350000.275000",
			Size:      "77507139196",
		},
		Streams: []ffprobeStream{{
			CodecType: "video",
			StartTime: "180000.000000",
			Duration:  "350300.196000",
		}},
	}

	got, ok := durationFromProbeMetadata(raw)
	if ok || got != 0 {
		t.Fatalf("durationFromProbeMetadata() = %d, %v; want 0, false", got, ok)
	}
}

func TestDurationFromProbeMetadataRejectsUncorroboratedLongVideoDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		formatDuration string
		streamDuration string
	}{
		{name: "missing stream duration", formatDuration: "182930.275000"},
		{name: "disagreeing durations", formatDuration: "182930.275000", streamDuration: "150000.000000"},
		{name: "beyond validated limit", formatDuration: "1000001.000000", streamDuration: "1000001.000000"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := &ffprobeOutput{
				Format: ffprobeFormat{Duration: tc.formatDuration, Size: "77507139196"},
				Streams: []ffprobeStream{{
					CodecType: "video",
					Duration:  tc.streamDuration,
				}},
			}

			got, ok := durationFromProbeMetadata(raw)
			if ok || got != 0 {
				t.Fatalf("durationFromProbeMetadata() = %d, %v; want 0, false", got, ok)
			}
		})
	}
}

func TestDurationFromProbeMetadataDoesNotUseSecondaryVideoForCorroboration(t *testing.T) {
	t.Parallel()

	raw := &ffprobeOutput{
		Format: ffprobeFormat{Duration: "182930.275000", Size: "77507139196"},
		Streams: []ffprobeStream{
			{CodecType: "video", Duration: "150000.000000"},
			{CodecType: "video", Duration: "182930.196000"},
		},
	}

	got, ok := durationFromProbeMetadata(raw)
	if ok || got != 0 {
		t.Fatalf("durationFromProbeMetadata() = %d, %v; want 0, false", got, ok)
	}
}

func TestProbeFileSkipsPacketScanForCorroboratedLongVideo(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	ffprobePath := filepath.Join(tempDir, "ffprobe")
	packetScanMarker := filepath.Join(tempDir, "packet-scan-called")
	script := `#!/bin/sh
case " $* " in
  *" -show_format "*)
    printf '%s\n' '{"format":{"duration":"182930.275000","size":"77507139196"},"streams":[{"codec_type":"video","duration":"182930.196000","avg_frame_rate":"24/1"}]}'
    ;;
  *)
    : > "` + packetScanMarker + `"
    exit 1
    ;;
esac
`
	if err := os.WriteFile(ffprobePath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake ffprobe: %v", err)
	}

	probe, err := ProbeFile(context.Background(), ffprobePath, "long.mp4")
	if err != nil {
		t.Fatalf("ProbeFile() returned error: %v", err)
	}
	if probe.Duration != 182930 {
		t.Fatalf("ProbeFile() duration = %d, want 182930", probe.Duration)
	}
	if _, err := os.Stat(packetScanMarker); !os.IsNotExist(err) {
		t.Fatalf("packet fallback ran for corroborated metadata; stat error = %v", err)
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

func TestEstimateVideoPacketDurationKeepsValidatedLongDuration(t *testing.T) {
	t.Parallel()

	packets := strings.NewReader("0.000000\n182930.196000\n")
	got := estimateVideoPacketDuration(packets, "")
	if got != 182930 {
		t.Fatalf("estimateVideoPacketDuration() = %d, want 182930", got)
	}
}

func TestEstimateVideoPacketDurationRejectsDurationBeyondValidatedLimit(t *testing.T) {
	t.Parallel()

	packets := strings.NewReader("0.000000\n1000001.000000\n")
	got := estimateVideoPacketDuration(packets, "")
	if got != 0 {
		t.Fatalf("estimateVideoPacketDuration() = %d, want 0", got)
	}
}

func TestEstimateVideoPacketDurationKeepsOrdinaryCapForFrameRateEstimate(t *testing.T) {
	t.Parallel()

	var packets strings.Builder
	for range 900 {
		packets.WriteString("3.022000\n")
	}

	got := estimateVideoPacketDuration(strings.NewReader(packets.String()), "1/1000")
	if got != 0 {
		t.Fatalf("estimateVideoPacketDuration() = %d, want 0", got)
	}
}

func TestEstimateVideoPacketDurationIgnoresUnusableFrameEstimateForLongSpan(t *testing.T) {
	t.Parallel()

	var packets strings.Builder
	packets.WriteString("0.000000\n")
	for range 898 {
		packets.WriteString("5.000000\n")
	}
	packets.WriteString("182930.196000\n")

	got := estimateVideoPacketDuration(strings.NewReader(packets.String()), "1/1000")
	if got != 182930 {
		t.Fatalf("estimateVideoPacketDuration() = %d, want 182930", got)
	}
}

func TestEstimateVideoPacketDurationRejectsOutlierSpanWhenFrameCountDisagrees(t *testing.T) {
	t.Parallel()

	var packets strings.Builder
	packets.WriteString("0.000000\n")
	for range 298 {
		packets.WriteString("5.000000\n")
	}
	packets.WriteString("500000.000000\n")

	got := estimateVideoPacketDuration(strings.NewReader(packets.String()), "30/1")
	if got != 10 {
		t.Fatalf("estimateVideoPacketDuration() = %d, want 10", got)
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

// A feature film that probes far short of its real runtime clears the absolute
// floor but implies an impossible bitrate. This is the case that reached
// clients as a 90-minute movie displayed as ~1 minute.
func TestDurationFromProbeMetadataRejectsImpossibleImpliedBitrate(t *testing.T) {
	t.Parallel()

	raw := &ffprobeOutput{
		Format: ffprobeFormat{
			Duration: "61.000000",
			Size:     "107374182400", // 100 GiB => ~14 Gbps at 61s
		},
		Streams: []ffprobeStream{{
			CodecType:    "video",
			AvgFrameRate: "24/1",
		}},
	}

	got, ok := durationFromProbeMetadata(raw)
	if ok || got != 0 {
		t.Fatalf("durationFromProbeMetadata() = %d, %v; want 0, false", got, ok)
	}
}

// The implied-bitrate rule must not reject genuinely short clips. A 30-second
// 4K clip at 100 MiB implies ~28 Mbps, which is ordinary.
func TestDurationFromProbeMetadataKeepsGenuineShortHighBitrateClip(t *testing.T) {
	t.Parallel()

	raw := &ffprobeOutput{
		Format: ffprobeFormat{
			Duration: "30.000000",
			Size:     "104857600",
		},
		Streams: []ffprobeStream{{
			CodecType:    "video",
			AvgFrameRate: "60/1",
		}},
	}

	got, ok := durationFromProbeMetadata(raw)
	if !ok || got != 30 {
		t.Fatalf("durationFromProbeMetadata() = %d, %v; want 30, true", got, ok)
	}
}

func TestVideoDurationImplausible(t *testing.T) {
	t.Parallel()

	const gib = int64(1024 * 1024 * 1024)
	tests := []struct {
		name     string
		duration float64
		size     int64
		hasVideo bool
		want     bool
	}{
		{name: "feature film probed as one minute", duration: 61, size: 100 * gib, want: true, hasVideo: true},
		{name: "legacy microsecond collapse", duration: 3, size: 2 * gib, want: true, hasVideo: true},
		{name: "genuine short clip", duration: 30, size: 100 * 1024 * 1024, want: false, hasVideo: true},
		{name: "ordinary feature film", duration: 5400, size: 8 * gib, want: false, hasVideo: true},
		{name: "uhd remux at full runtime", duration: 7200, size: 80 * gib, want: false, hasVideo: true},
		{name: "audio only is never flagged", duration: 1, size: 100 * gib, want: false, hasVideo: false},
		{name: "unknown size cannot be judged", duration: 61, size: 0, want: false, hasVideo: true},
		{name: "unknown duration is not this rule's job", duration: 0, size: 100 * gib, want: false, hasVideo: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := videoDurationImplausible(tc.duration, tc.size, tc.hasVideo); got != tc.want {
				t.Fatalf("videoDurationImplausible(%v, %d, %v) = %v; want %v",
					tc.duration, tc.size, tc.hasVideo, got, tc.want)
			}
		})
	}
}
