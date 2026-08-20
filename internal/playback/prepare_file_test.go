package playback

import (
	"context"
	"os"
	"path/filepath"
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

func TestBuildPrepareFileArgsSharesHigh10DecodeFallback(t *testing.T) {
	tests := []struct {
		name      string
		hwAccel   string
		want      []string
		forbidden []string
	}{
		{
			name:      "qsv keeps hardware encode with software decode upload",
			hwAccel:   "qsv",
			want:      []string{"-c:v h264_qsv", "format=nv12,hwupload,hwmap=derive_device=qsv"},
			forbidden: []string{"-hwaccel qsv", "-hwaccel vaapi"},
		},
		{
			name:      "vaapi keeps hardware encode with software decode upload",
			hwAccel:   "vaapi",
			want:      []string{"-c:v h264_vaapi", "scale=-2:720,format=nv12,hwupload"},
			forbidden: []string{"-hwaccel vaapi"},
		},
		{
			name:      "nvenc falls back to software encode",
			hwAccel:   "nvenc",
			want:      []string{"-c:v libx264", "-vf scale=-2:720"},
			forbidden: []string{"-hwaccel cuda", "h264_nvenc", "scale_cuda"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildPrepareFileArgs(TranscodeOpts{
				InputPath:           "/media/high10.mkv",
				SourceVideoCodec:    "h264",
				SourceVideoProfile:  "High 10",
				SourceVideoBitDepth: 10,
				TargetCodecVideo:    "h264",
				TargetCodecAudio:    "aac",
				TargetResolution:    "720p",
				HWAccel:             tt.hwAccel,
				AudioTrackIndex:     -1,
			}, "/artifacts/out.mp4")
			joined := strings.Join(args, " ")
			for _, want := range tt.want {
				if !strings.Contains(joined, want) {
					t.Fatalf("args missing %q: %s", want, joined)
				}
			}
			for _, forbidden := range tt.forbidden {
				if strings.Contains(joined, forbidden) {
					t.Fatalf("args unexpectedly contain %q: %s", forbidden, joined)
				}
			}
		})
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

func TestPrepareFileResolvesOneDeviceAndReleasesAfterExit(t *testing.T) {
	resetDeviceLoad(t)
	devA, devB := "/dev/dri/renderD888", "/dev/dri/renderD889"
	fakeDeviceStat(t, devA, devB)

	// Fake ffmpeg: record argv, create the output (last arg) so finalize works.
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+argsFile+"\neval \"touch \\${$#}\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(dir, "artifact.mp4")
	err := PrepareFile(context.Background(), TranscodeOpts{
		InputPath:        "/nonexistent/input.mkv",
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		FFmpegPath:       script,
		HWAccel:          "vaapi",
		HWDevice:         devA + "," + devB,
	}, outputPath)
	if err != nil {
		t.Fatalf("PrepareFile: %v", err)
	}

	argv, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(argv)
	if !strings.Contains(got, devA) {
		t.Fatalf("ffmpeg args missing resolved device %s:\n%s", devA, got)
	}
	if strings.Contains(got, devA+","+devB) {
		t.Fatalf("ffmpeg args contain the raw device list:\n%s", got)
	}
	if count := hwDeviceActiveCount(devA); count != 0 {
		t.Fatalf("active count after PrepareFile returned = %d, want 0", count)
	}
}
