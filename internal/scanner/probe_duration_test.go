package scanner

import (
	"context"

	"path/filepath"

	"testing"
)

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

// A container stretched past the film by a subtitle track lists HLS segments
// past the end of the video, which a transcode never writes, so a video
// file's duration comes from its video and audio tracks even when they fall
// short by only a few seconds. A stretch that no track ending after the video
// accounts for keeps the container duration.
func TestDurationFromProbeMetadataIgnoresSubtitleStretch(t *testing.T) {
	t.Parallel()

	tagged := func(codecType, length string) ffprobeStream {
		return ffprobeStream{CodecType: codecType, Duration: "N/A", Tags: map[string]string{"DURATION": length}}
	}
	for _, tt := range []struct {
		name     string
		format   string
		audio    string
		subtitle string
		// second is another subtitle track, when set.
		second string
		want   int
	}{
		{"subtitle stretches by seconds", "154.390000", "00:02:30.000000000", "00:02:34.390000000", "", 150},
		{"a partial last second stays listed", "154.390000", "00:02:30.900000000", "00:02:34.390000000", "", 151},
		{"subtitle ends before the video", "152.000000", "00:02:30.000000000", "00:02:28.000000000", "", 152},
		{"nothing accounts for the stretch", "154.390000", "00:02:30.000000000", "00:02:00.000000000", "", 154},
		{"a subtitle ending well before a long container", "7200.000000", "01:59:20.000000000", "01:59:21.000000000", "", 7200},
		{"a subtitle ending well after a long container", "7200.000000", "01:59:20.000000000", "02:00:40.000000000", "", 7200},
		{"a subtitle ending with the container beside one ending later", "3600.000000", "00:59:30.000000000", "01:00:00.000000000", "01:00:20.000000000", 3570},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := &ffprobeOutput{
				Format: ffprobeFormat{Duration: tt.format, StartTime: "0.000000"},
				Streams: []ffprobeStream{
					tagged("video", "00:02:29.999000000"),
					tagged("audio", tt.audio),
					tagged("subtitle", tt.subtitle),
				},
			}
			if tt.second != "" {
				raw.Streams = append(raw.Streams, tagged("subtitle", tt.second))
			}
			got, ok := durationFromProbeMetadata(raw)
			if !ok || got != tt.want {
				t.Fatalf("durationFromProbeMetadata() = %d, %v; want %d", got, ok, tt.want)
			}
		})
	}
}
