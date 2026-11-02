package scanner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDurationFromProbeMetadata(t *testing.T) {
	t.Parallel()

	const large = "77507139196"
	video := func(start, duration string) ffprobeStream {
		return ffprobeStream{CodecType: "video", StartTime: start, Duration: duration}
	}
	tests := []struct {
		name    string
		format  ffprobeFormat
		streams []ffprobeStream
		want    int
	}{
		{"uses reasonable video duration", ffprobeFormat{Duration: "1745764.949333"},
			[]ffprobeStream{video("", "4077.708452"), {CodecType: "audio", Duration: "1745764.949333"}}, 4077},
		{"removes absolute timestamp offset", ffprobeFormat{StartTime: "1514891.405000", Duration: "1520667.605000"},
			[]ffprobeStream{video("1514891.405000", "")}, 5776},
		{"rejects implausibly short large video", ffprobeFormat{Duration: "3.022000", Size: "2022705152"},
			[]ffprobeStream{{CodecType: "video", AvgFrameRate: "30/1"}}, 0},
		{"keeps long audio duration in seconds", ffprobeFormat{Duration: "108000.000000"},
			[]ffprobeStream{{CodecType: "audio"}}, 108000},
		{"keeps corroborated long video with ordinary start time", ffprobeFormat{StartTime: "30.000000", Duration: "182930.275000", Size: large},
			[]ffprobeStream{video("30.000000", "182930.196000")}, 182930},
		{"keeps corroborated long video with material start time", ffprobeFormat{StartTime: "300.000000", Duration: "182930.275000", Size: large},
			[]ffprobeStream{video("300.000000", "182930.196000")}, 182930},
		{"normalizes offset before corroborating long video", ffprobeFormat{StartTime: "180000.000000", Duration: "182930.275000", Size: large},
			[]ffprobeStream{video("180000.000000", "182930.196000")}, 2930},
		{"normalizes corroborated long offset span", ffprobeFormat{StartTime: "180000.000000", Duration: "350000.275000", Size: large},
			[]ffprobeStream{video("200000.000000", "370000.196000")}, 170000},
		{"normalizes matching long absolute end timestamps", ffprobeFormat{StartTime: "180000.000000", Duration: "350000.275000", Size: large},
			[]ffprobeStream{video("180000.000000", "350000.196000")}, 170000},
		{"rejects disagreeing absolute end spans", ffprobeFormat{StartTime: "180000.000000", Duration: "350000.275000", Size: large},
			[]ffprobeStream{video("180000.000000", "350300.196000")}, 0},
		{"rejects long video missing stream duration", ffprobeFormat{Duration: "182930.275000", Size: large},
			[]ffprobeStream{video("", "")}, 0},
		{"rejects long video with disagreeing durations", ffprobeFormat{Duration: "182930.275000", Size: large},
			[]ffprobeStream{video("", "150000.000000")}, 0},
		{"rejects long video beyond validated limit", ffprobeFormat{Duration: "1000001.000000", Size: large},
			[]ffprobeStream{video("", "1000001.000000")}, 0},
		{"does not use secondary video for corroboration", ffprobeFormat{Duration: "182930.275000", Size: large},
			[]ffprobeStream{video("", "150000.000000"), video("", "182930.196000")}, 0},
		// Embedded cover art is reported by ffprobe as a video stream; it must
		// not route audiobooks and music through the video duration gauntlet.
		{"ignores cover art video stream", ffprobeFormat{Duration: "108000.000000"},
			[]ffprobeStream{{CodecType: "audio"}, {CodecType: "video", CodecName: "mjpeg", Disposition: ffprobeDisp{AttachedPic: 1}}}, 108000},
		{"rejects absurd audio duration", ffprobeFormat{Duration: "4298357.248000"},
			[]ffprobeStream{{CodecType: "audio"}}, 0},
		// The end-minus-start fallbacks apply the same plausibility guard as the
		// direct duration paths.
		{"rejects collapsed timestamp span for large video", ffprobeFormat{Size: "2022705152"},
			[]ffprobeStream{video("1514891.405000", "1514894.405000")}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := durationFromProbeMetadata(&ffprobeOutput{Format: tc.format, Streams: tc.streams})
			if got != tc.want || ok != (tc.want != 0) {
				t.Fatalf("durationFromProbeMetadata() = %d, %v; want %d, %v", got, ok, tc.want, tc.want != 0)
			}
		})
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
	writeFakeTool(t, ffprobePath, script)

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

func TestEstimateVideoPacketDuration(t *testing.T) {
	t.Parallel()

	repeat := func(first string, line string, n int, last string) string {
		return first + strings.Repeat(line, n) + last
	}
	tests := []struct {
		name      string
		packets   string
		frameRate string
		want      int
	}{
		{"uses packet span", "1514891.405000\n1515000.000000\n1520667.605000\n", "30000/1001", 5776},
		{"keeps validated long duration", "0.000000\n182930.196000\n", "", 182930},
		{"rejects duration beyond validated limit", "0.000000\n1000001.000000\n", "", 0},
		{"keeps ordinary cap for frame rate estimate", repeat("", "3.022000\n", 900, ""), "1/1000", 0},
		{"ignores unusable frame estimate for long span", repeat("0.000000\n", "5.000000\n", 898, "182930.196000\n"), "1/1000", 182930},
		{"rejects outlier span when frame count disagrees", repeat("0.000000\n", "5.000000\n", 298, "500000.000000\n"), "30/1", 10},
	}
	for _, tc := range tests {
		if got := estimateVideoPacketDuration(strings.NewReader(tc.packets), tc.frameRate); got != tc.want {
			t.Errorf("%s: estimateVideoPacketDuration() = %d, want %d", tc.name, got, tc.want)
		}
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
	writeFakeTool(t, ffprobePath, script)

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
	writeFakeTool(t, ffprobePath, script)

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
