package playback

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// An audio track FFmpeg cannot decode would fail the whole encode, so the
// prepared file leaves it out and the next decodable track becomes default.
func TestPlanPreparedTracksSkipsUndecodableAudio(t *testing.T) {
	file := &models.MediaFile{
		CodecAudio: "eac3",
		AudioTracks: []models.AudioTrack{
			{Codec: "", Channels: 6, Language: "zh", Default: true},
			{Codec: "eac3", Channels: 6, Language: "zh"},
			{Codec: "aac", Channels: 2, Language: "zh"},
		},
	}
	plan := PlanPreparedTracks(file, "aac", -1)
	want := []PreparedAudioTrack{
		{SourceIndex: 1, Codec: "aac", SourceChannels: 6, Language: "zh", Default: true},
		{SourceIndex: 2, Codec: "aac", SourceChannels: 2, Language: "zh"},
	}
	if !reflect.DeepEqual(plan.Audio, want) {
		t.Fatalf("audio plan = %+v, want %+v", plan.Audio, want)
	}
	if explicit := PlanPreparedTracks(file, "aac", 0); !reflect.DeepEqual(explicit.Audio, want) {
		t.Fatalf("audio plan with the undecodable track requested = %+v, want %+v", explicit.Audio, want)
	}

	joined := strings.Join(buildPrepareFileArgs(TranscodeOpts{
		InputPath: "/media/in.mp4", TargetCodecVideo: "copy", TargetCodecAudio: "aac",
		HWAccel: "none", AudioTrackIndex: -1, SubtitleTrackIndex: -1, PreparedTracks: plan,
	}, "/artifacts/out.mp4"), " ")
	if !strings.Contains(joined, "-map 0:v:0 -map 0:a:1 -map 0:a:2 -dn") || strings.Contains(joined, "0:a:0") || !strings.Contains(joined, "-disposition:a:0 default") {
		t.Fatalf("prepared track args must map only the decodable tracks:\n%s", joined)
	}
}

func TestBuildPrepareFileArgsWithoutPreparedTracksKeepsLegacyLayout(t *testing.T) {
	joined := strings.Join(buildPrepareFileArgs(TranscodeOpts{
		InputPath: "/media/in.mkv", TargetCodecVideo: "copy", TargetCodecAudio: "copy",
		HWAccel: "none", AudioTrackIndex: -1, SubtitleTrackIndex: -1,
	}, "/artifacts/out.mp4"), " ")
	if !strings.Contains(joined, "-map 0:v:0 -map 0:a:0? -sn -dn") || !strings.Contains(joined, "-c:a copy") {
		t.Fatalf("legacy prepared layout changed:\n%s", joined)
	}
}

type preparedProbeStream struct {
	CodecType   string            `json:"codec_type"`
	CodecName   string            `json:"codec_name"`
	Channels    int               `json:"channels"`
	Tags        map[string]string `json:"tags"`
	Disposition map[string]int    `json:"disposition"`
}

func TestPrepareFileKeepsEveryAudioAndTextSubtitleTrack(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe := ffprobePathFromFFmpeg(ffmpeg)
	if _, err := exec.LookPath(ffprobe); err != nil {
		t.Skip("ffprobe is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	srt := filepath.Join(dir, "cues.srt")
	if err := os.WriteFile(srt, []byte("1\n00:00:00,500 --> 00:00:01,500\nHello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.mkv")
	if output, err := exec.CommandContext(ctx, ffmpeg, "-v", "error",
		"-f", "lavfi", "-i", "testsrc=d=2:s=64x64:r=24",
		"-f", "lavfi", "-i", "sine=f=440:d=2",
		"-f", "lavfi", "-i", "sine=f=880:d=2",
		"-i", srt, "-i", srt,
		"-map", "0", "-map", "1", "-map", "2", "-map", "3", "-map", "4",
		"-c:v", "libx264", "-preset", "ultrafast",
		"-c:a:0", "ac3", "-ac:a:0", "6", "-c:a:1", "aac",
		"-c:s:0", "srt", "-c:s:1", "webvtt",
		"-y", source,
	).CombinedOutput(); err != nil {
		t.Fatalf("create source: %v\n%s", err, output)
	}
	file := &models.MediaFile{
		CodecAudio: "ac3",
		AudioTracks: []models.AudioTrack{
			{Codec: "ac3", Channels: 6, Language: "en", EmbeddedTitle: "Main"},
			{Codec: "aac", Channels: 1, Language: "ja", EmbeddedTitle: "Commentary"},
		},
		SubtitleTracks: []models.SubtitleTrack{
			{Codec: "subrip", Language: "fr", Forced: true},
			{Codec: "webvtt", Language: "en"},
		},
	}

	for _, tc := range []struct {
		name, video, audio string
		wantAudioCodecs    []string
	}{
		{name: "transcode", video: "h264", audio: "aac", wantAudioCodecs: []string{"aac", "aac"}},
		{name: "remux", video: "copy", audio: "copy", wantAudioCodecs: []string{"ac3", "aac"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := filepath.Join(dir, tc.name+".mp4")
			err := PrepareFile(ctx, TranscodeOpts{
				InputPath: source, SourceVideoCodec: "h264", TargetCodecVideo: tc.video, TargetCodecAudio: tc.audio,
				HWAccel: "none", FFmpegPath: ffmpeg, AudioTrackIndex: -1, SubtitleTrackIndex: -1,
				PreparedTracks: PlanPreparedTracks(file, tc.audio, -1),
			}, output)
			if err != nil {
				t.Fatalf("PrepareFile: %v", err)
			}
			probe, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_streams", "-of", "json", output).Output()
			if err != nil {
				t.Fatalf("ffprobe: %v", err)
			}
			var parsed struct {
				Streams []preparedProbeStream `json:"streams"`
			}
			if err := json.Unmarshal(probe, &parsed); err != nil {
				t.Fatal(err)
			}
			var audio, subtitles []preparedProbeStream
			for _, stream := range parsed.Streams {
				switch stream.CodecType {
				case "audio":
					audio = append(audio, stream)
				case "subtitle":
					subtitles = append(subtitles, stream)
				}
			}
			if len(audio) != 2 || len(subtitles) != 2 {
				t.Fatalf("prepared file has %d audio and %d subtitle streams, want 2 and 2", len(audio), len(subtitles))
			}
			for i, want := range tc.wantAudioCodecs {
				if audio[i].CodecName != want {
					t.Fatalf("audio %d codec = %q, want %q", i, audio[i].CodecName, want)
				}
			}
			if tc.audio == "aac" && audio[0].Channels != 2 {
				t.Fatalf("surround track was not downmixed to stereo: %d channels", audio[0].Channels)
			}
			if audio[0].Tags["language"] != "eng" || audio[1].Tags["language"] != "jpn" || audio[1].Tags["handler_name"] != "Commentary" {
				t.Fatalf("audio metadata lost: %+v / %+v", audio[0].Tags, audio[1].Tags)
			}
			if audio[0].Disposition["default"] != 1 || audio[1].Disposition["default"] != 0 {
				t.Fatalf("default audio disposition = %d/%d, want 1/0", audio[0].Disposition["default"], audio[1].Disposition["default"])
			}
			for i, want := range []string{"fra", "eng"} {
				if subtitles[i].CodecName != "mov_text" || subtitles[i].Tags["language"] != want {
					t.Fatalf("subtitle %d = %s/%q, want mov_text/%q", i, subtitles[i].CodecName, subtitles[i].Tags["language"], want)
				}
			}
			if subtitles[0].Disposition["forced"] != 1 {
				t.Fatal("forced subtitle disposition was lost")
			}
		})
	}
}
