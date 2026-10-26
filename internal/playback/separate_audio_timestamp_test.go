package playback

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Dense lossless audio can consume the whole packet budget before the second
// reordered video packet. The probe must budget each selected stream separately.
func TestSeparateAudioTimestampDenseAudioDoesNotStarveVideo(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
stream=""
while [ "$#" -gt 0 ]; do
 if [ "$1" = "-select_streams" ]; then shift; stream="$1"; fi
 shift
done
case "$stream" in
 v:0) printf '%s' '{"format":{"start_time":"0"},"streams":[{"index":0,"codec_type":"video"}],"packets":[{"stream_index":0,"duration_time":"0.041"},{"stream_index":0,"dts_time":"0"}]}' ;;
 a:0) printf '%s' '{"format":{"start_time":"0"},"streams":[{"index":1,"codec_type":"audio","sample_rate":"48000"}],"packets":[{"stream_index":1,"dts_time":"0"}]}' ;;
 *) printf '%s' '{"format":{"start_time":"0"},"streams":[{"index":0,"codec_type":"video"},{"index":1,"codec_type":"audio","sample_rate":"48000"}],"packets":[{"stream_index":0,"duration_time":"0.041"},{"stream_index":1,"dts_time":"0"}]}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "ffprobe"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := prepareSeparateAudioTimestampOffset(context.Background(), TranscodeOpts{SeparateAudioHLS: true, FFmpegPath: filepath.Join(dir, "ffmpeg"), InputPath: "synthetic.mkv", TargetCodecAudio: "aac"})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.separateAudioTimestampOffset-0.041) > 0.000001 {
		t.Fatalf("offset=%v, want 0.041", got.separateAudioTimestampOffset)
	}
}

func timestampProbeFixture(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ffprobe"), []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "ffmpeg")
}

func TestSeparateAudioTimestampSelectsNondefaultAudioAndOriginalIndices(t *testing.T) {
	ffmpeg := timestampProbeFixture(t, `stream=""
while [ "$#" -gt 0 ]; do
 if [ "$1" = "-select_streams" ]; then shift; stream="$1"; fi
 shift
done
case "$stream" in
 v:0) printf '%s' '{"format":{"start_time":"0"},"streams":[{"index":2,"codec_type":"video"}],"packets":[{"stream_index":2,"dts_time":"-0.08"}]}' ;;
 a:1) printf '%s' '{"format":{"start_time":"0"},"streams":[{"index":4,"codec_type":"audio","sample_rate":"48000"}],"packets":[{"stream_index":4,"dts_time":"-0.1","side_data_list":[{"skip_samples":480}]}]}' ;;
 *) exit 1 ;;
esac
`)
	got, err := prepareSeparateAudioTimestampOffset(context.Background(), TranscodeOpts{SeparateAudioHLS: true, FFmpegPath: ffmpeg, InputPath: "synthetic.mkv", TargetCodecAudio: "aac", AudioTrackIndex: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Audio starts at -0.1 + 480/48000 - 1024/48000, earlier than video.
	if math.Abs(got.separateAudioTimestampOffset-0.11133333333333334) > 0.000001 {
		t.Fatalf("offset=%v", got.separateAudioTimestampOffset)
	}
}

func TestSeparateAudioTimestampRefusesIncompleteEvidence(t *testing.T) {
	for _, body := range []string{
		`not-json`,
		`{"format":{"start_time":"0"},"streams":[{"index":0,"codec_type":"video"}],"packets":[{"stream_index":0,"duration_time":"0.04"}]}`,
		`{"format":{"start_time":"NaN"},"streams":[{"index":0,"codec_type":"video"}],"packets":[{"stream_index":0,"dts_time":"0"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			ffmpeg := timestampProbeFixture(t, "printf '%s' '"+body+"'\n")
			_, err := prepareSeparateAudioTimestampOffset(context.Background(), TranscodeOpts{SeparateAudioHLS: true, FFmpegPath: ffmpeg, InputPath: "synthetic.mkv", TargetCodecAudio: "copy"})
			if err == nil {
				t.Fatal("incomplete timestamp evidence accepted")
			}
		})
	}
}

func TestSeparateAudioTimestampHonorsCallerCancellation(t *testing.T) {
	ffmpeg := timestampProbeFixture(t, "exec sleep 60\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := prepareSeparateAudioTimestampOffset(ctx, TranscodeOpts{SeparateAudioHLS: true, FFmpegPath: ffmpeg, InputPath: "synthetic.mkv"})
	if err == nil {
		t.Fatal("canceled probe accepted")
	}
}

func TestSeparateAudioTimestampSharesCallerDeadlineAcrossStreams(t *testing.T) {
	ffmpeg := timestampProbeFixture(t, `sleep 0.15
printf '%s' '{"format":{"start_time":"0"},"streams":[{"index":0,"codec_type":"video"},{"index":1,"codec_type":"audio","sample_rate":"48000"}],"packets":[{"stream_index":0,"dts_time":"0"},{"stream_index":1,"dts_time":"0"}]}'
`)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := prepareSeparateAudioTimestampOffset(ctx, TranscodeOpts{SeparateAudioHLS: true, FFmpegPath: ffmpeg, InputPath: "synthetic.mkv", TargetCodecAudio: "copy"})
	if err == nil {
		t.Fatal("two stream probes exceeded the shared caller deadline")
	}
}

func TestSeparateAudioTimestampAllowsSlowStorage(t *testing.T) {
	ffmpeg := timestampProbeFixture(t, `sleep 1.6
printf '%s' '{"format":{"start_time":"0"},"streams":[{"index":0,"codec_type":"video"},{"index":1,"codec_type":"audio","sample_rate":"48000"}],"packets":[{"stream_index":0,"dts_time":"-0.08"},{"stream_index":1,"dts_time":"0"}]}'
`)
	got, err := prepareSeparateAudioTimestampOffset(t.Context(), TranscodeOpts{SeparateAudioHLS: true, FFmpegPath: ffmpeg, InputPath: "synthetic.mkv", TargetCodecAudio: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.separateAudioTimestampOffset-0.08) > 0.000001 {
		t.Fatalf("offset=%v, want 0.08", got.separateAudioTimestampOffset)
	}
}
