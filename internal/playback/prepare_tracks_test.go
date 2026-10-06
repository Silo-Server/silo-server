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

func preparedTracksTestFile() *models.MediaFile {
	return &models.MediaFile{
		CodecAudio: "eac3",
		AudioTracks: []models.AudioTrack{
			{Codec: "eac3", Channels: 6, Language: "en", EmbeddedTitle: "Surround", Title: "Surround"},
			{Codec: "truehd", Channels: 8, Language: "en", Title: "TRUEHD", Default: true},
			{Codec: "aac", Channels: 2, Language: "ja", EmbeddedTitle: "Commentary", Title: "Commentary"},
		},
		SubtitleTracks: []models.SubtitleTrack{
			{Codec: "subrip", Language: "fr", EmbeddedTitle: "Forced", Forced: true},
			{Codec: "hdmv_pgs_subtitle", Language: "en"},
			{Codec: "ass", Language: "en", HearingImpaired: true},
			{Codec: "dvd_subtitle", Language: "de"},
			{Codec: "webvtt", Language: "es", HearingImpaired: true},
		},
	}
}

func TestPlanPreparedTracksKeepsEveryAudioTrackAndTextSubtitle(t *testing.T) {
	file := preparedTracksTestFile()

	remux := PlanPreparedTracks(file, "copy", -1)
	wantRemuxAudio := []PreparedAudioTrack{
		{SourceIndex: 0, Codec: "copy", Language: "en", Title: "Surround"},
		{SourceIndex: 1, Codec: "aac", SourceChannels: 8, Language: "en", Default: true},
		{SourceIndex: 2, Codec: "copy", Language: "ja", Title: "Commentary"},
	}
	if !reflect.DeepEqual(remux.Audio, wantRemuxAudio) {
		t.Fatalf("remux audio plan = %+v, want %+v", remux.Audio, wantRemuxAudio)
	}
	wantSubtitles := []PreparedSubtitleTrack{
		{SourceIndex: 0, Language: "fr", Title: "Forced", Forced: true},
		{SourceIndex: 4, Language: "es", HearingImpaired: true},
	}
	if !reflect.DeepEqual(remux.Subtitles, wantSubtitles) {
		t.Fatalf("subtitle plan = %+v, want %+v", remux.Subtitles, wantSubtitles)
	}
	// wantRemuxAudio marks the source default track (1) as the default output.

	transcode := PlanPreparedTracks(file, "aac", 2)
	for i, track := range transcode.Audio {
		if track.Codec != "aac" {
			t.Fatalf("transcode track %d codec = %q, want aac", i, track.Codec)
		}
		if track.SourceChannels != file.AudioTracks[i].Channels {
			t.Fatalf("transcode track %d source channels = %d, want %d", i, track.SourceChannels, file.AudioTracks[i].Channels)
		}
	}
	if !transcode.Audio[2].Default || transcode.Audio[1].Default {
		t.Fatalf("explicit audio track index must be the only default output: %+v", transcode.Audio)
	}
}

func TestPlanPreparedTracksEncodesCodecsMP4CannotCarry(t *testing.T) {
	file := &models.MediaFile{
		CodecAudio: "truehd",
		AudioTracks: []models.AudioTrack{
			{Codec: "truehd", Channels: 8, Language: "en"},
			{Codec: "truehd", Channels: 6, Language: "de"},
			{Codec: "pcm_s24le", Channels: 2, Language: "fr"},
			{Codec: "aac", Channels: 2, Language: "ja"},
		},
	}
	plan := PlanPreparedTracks(file, "copy", -1)
	want := []string{"aac", "aac", "aac", "copy"}
	if len(plan.Audio) != len(want) {
		t.Fatalf("audio track count = %d, want %d", len(plan.Audio), len(want))
	}
	for i, track := range plan.Audio {
		if track.Codec != want[i] {
			t.Fatalf("track %d codec = %q, want %q (plan %+v)", i, track.Codec, want[i], plan.Audio)
		}
	}
}

func TestPreparedSubtitleSidecarFormat(t *testing.T) {
	for codec, want := range map[string]string{
		"ass": "ass", "ssa": "ass", "hdmv_pgs_subtitle": "sup", "pgssub": "sup",
		"subrip": "", "webvtt": "", "dvd_subtitle": "", "dvb_subtitle": "",
	} {
		if got := PreparedSubtitleSidecarFormat(codec); got != want {
			t.Errorf("PreparedSubtitleSidecarFormat(%q) = %q, want %q", codec, got, want)
		}
	}
}

func TestPreparedTracksAvailableRequiresAudioInventory(t *testing.T) {
	for name, tc := range map[string]struct {
		file *models.MediaFile
		want bool
	}{
		"probed audio":            {file: &models.MediaFile{CodecAudio: "aac", AudioTracks: []models.AudioTrack{{Codec: "aac"}}}, want: true},
		"no probed audio":         {file: &models.MediaFile{}, want: false},
		"codec without inventory": {file: &models.MediaFile{CodecAudio: "eac3"}, want: false},
		"missing file":            {file: nil, want: false},
	} {
		if got := PreparedTracksAvailable(tc.file); got != tc.want {
			t.Errorf("%s: PreparedTracksAvailable = %v, want %v", name, got, tc.want)
		}
	}
}

func TestPlanPreparedTracksWithoutAudio(t *testing.T) {
	plan := PlanPreparedTracks(&models.MediaFile{}, "aac", -1)
	if plan.Audio == nil || len(plan.Audio) != 0 {
		t.Fatalf("empty source plan = %+v", plan)
	}
}

func TestBuildPrepareFileArgsMapsPreparedTracks(t *testing.T) {
	plan := PlanPreparedTracks(preparedTracksTestFile(), "copy", -1)
	args := buildPrepareFileArgs(TranscodeOpts{
		InputPath: "/media/in.mkv", TargetCodecVideo: "copy", TargetCodecAudio: "copy",
		HWAccel: "none", AudioTrackIndex: -1, SubtitleTrackIndex: -1, PreparedTracks: plan,
	}, "/artifacts/out.mp4")
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"-map 0:v:0 -map 0:a:0 -map 0:a:1 -map 0:a:2 -map 0:s:0 -map 0:s:4 -dn",
		"-c:a:0 copy -metadata:s:a:0 language=eng -metadata:s:a:0 handler_name=Surround -disposition:a:0 0",
		"-c:a:1 aac -b:a:1 192k -ac:a:1 2 -filter:a:1 " + stereoDownmixBoostFilterV3 + " -metadata:s:a:1 language=eng -disposition:a:1 default",
		"-c:a:2 copy -metadata:s:a:2 language=jpn -metadata:s:a:2 handler_name=Commentary -disposition:a:2 0",
		"-c:s:0 mov_text -metadata:s:s:0 language=fra -metadata:s:s:0 handler_name=Forced -disposition:s:0 forced",
		"-c:s:1 mov_text -metadata:s:s:1 language=spa -disposition:s:1 hearing_impaired",
		"-movflags +faststart -f mp4",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("prepared track args missing %q:\n%s", want, joined)
		}
	}
	for _, forbidden := range []string{"-sn", "-c:a copy", "-af ", "0:s:1", "0:s:2", "0:s:3"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("prepared track args contain %q:\n%s", forbidden, joined)
		}
	}
	// A mixed copy/AAC plan still encodes audio, so remux keeps its single-thread cap.
	if !strings.Contains(joined, "-threads 1") {
		t.Fatalf("remux with an encoded track must cap threads:\n%s", joined)
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
		"-f", "lavfi", "-i", "testsrc=d=2:s=320x240:r=24",
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
