package playback

import (
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestBuildPrepareFileArgsEmitsFaststartMP4(t *testing.T) {
	cases := []struct {
		name  string
		video string
		audio string
	}{
		{"remux", "copy", "copy"},
		{"transcode", "h264", "aac"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := buildPrepareFileArgs(TranscodeOpts{
				InputPath:        "/media/in.mkv",
				SourceVideoCodec: "h264",
				TargetCodecVideo: tc.video,
				TargetCodecAudio: tc.audio,
				HWAccel:          "none",
				AudioTrackIndex:  -1,
			}, "/artifacts/out.mp4")
			joined := strings.Join(args, " ")

			if !strings.Contains(joined, "-movflags +faststart") {
				t.Fatalf("%s args missing -movflags +faststart: %s", tc.name, joined)
			}
			if !strings.Contains(joined, "-f mp4") {
				t.Fatalf("%s args missing -f mp4: %s", tc.name, joined)
			}
			if strings.Contains(joined, "-f hls") || strings.Contains(joined, "hls_segment") {
				t.Fatalf("%s args must not emit HLS: %s", tc.name, joined)
			}
			if args[len(args)-1] != "/artifacts/out.mp4" {
				t.Fatalf("%s output path must be last arg: %s", tc.name, joined)
			}
		})
	}

	// Remux copies the video stream rather than re-encoding.
	remux := strings.Join(buildPrepareFileArgs(TranscodeOpts{
		InputPath: "/m.mkv", TargetCodecVideo: "copy", TargetCodecAudio: "copy", HWAccel: "none", AudioTrackIndex: -1,
	}, "/o.mp4"), " ")
	if !strings.Contains(remux, "-c:v copy") {
		t.Fatalf("remux must copy video: %s", remux)
	}
}

func TestResolvePrepareTarget(t *testing.T) {
	settings := AdminSettings{TranscodeEnabled: true, Allow4KTranscode: true}
	file := &models.MediaFile{CodecVideo: "h264", CodecAudio: "dts", Container: "mkv", Resolution: "1080p"}

	// remux with an undecodable audio codec → copy video, transcode audio to AAC.
	caps := ClientCapabilities{CodecsVideo: []string{"h264"}, CodecsAudio: []string{"aac"}, Containers: []string{"mp4"}, MaxResolution: "2160p"}
	rt := ResolvePrepareTarget(file, "remux", caps, settings)
	if rt.Container != "mp4" || rt.CodecVideo != "copy" || rt.CodecAudio != "aac" {
		t.Fatalf("remux target = %+v, want copy video / aac audio / mp4", rt)
	}

	// remux with a decodable audio codec → copy both streams.
	capsAudioOK := ClientCapabilities{CodecsVideo: []string{"h264"}, CodecsAudio: []string{"aac", "dts"}, Containers: []string{"mp4"}, MaxResolution: "2160p"}
	rt = ResolvePrepareTarget(file, "remux", capsAudioOK, settings)
	if rt.CodecAudio != "copy" {
		t.Fatalf("remux audio = %q, want copy", rt.CodecAudio)
	}

	// transcode → H.264/AAC, downscaled to the client max when the source exceeds it.
	rt = ResolvePrepareTarget(file, "transcode", ClientCapabilities{MaxResolution: "720p"}, settings)
	if rt.CodecVideo != "h264" || rt.CodecAudio != "aac" || rt.Resolution != "720p" {
		t.Fatalf("transcode target = %+v, want h264/aac/720p", rt)
	}

	// transcode where the source already fits → keep source resolution (no scale).
	rt = ResolvePrepareTarget(file, "transcode", ClientCapabilities{MaxResolution: "1080p"}, settings)
	if rt.Resolution != "" {
		t.Fatalf("transcode resolution = %q, want empty (source)", rt.Resolution)
	}
}
