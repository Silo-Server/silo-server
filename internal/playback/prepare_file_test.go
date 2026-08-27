package playback

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/tonemap"
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
		{
			name:      "videotoolbox keeps hardware encode with software decode",
			hwAccel:   "videotoolbox",
			want:      []string{"-c:v h264_videotoolbox", "-vf scale=-2:720"},
			forbidden: []string{"-hwaccel videotoolbox"},
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
				// Fake VideoToolbox-capable ffmpeg so the encoder probe in
				// resolveEffectiveTranscodeHWAccel succeeds on Linux CI too.
				FFmpegPath:      videoToolboxTestFFmpeg(t),
				AudioTrackIndex: -1,
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

// TestPrepareFileRemovesFailedPartialOutput verifies that a failed encode
// publishes neither its temporary bytes nor a final artifact.
func TestPrepareFileRemovesFailedPartialOutput(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ffmpeg")
	writtenPathMarker := filepath.Join(dir, "written-path.txt")
	partialExistedMarker := filepath.Join(dir, "partial-existed")
	scriptBody := "#!/bin/sh\n" +
		"eval \"output=\\\"\\${$#}\\\"\"\n" +
		"printf '%s\\n' \"$output\" > " + writtenPathMarker + "\n" +
		"printf partial > \"$output\"\n" +
		"test -f \"$output\" && touch " + partialExistedMarker + "\n" +
		"exit 1\n"
	if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
		t.Fatalf("write failing FFmpeg: %v", err)
	}
	outputPath := filepath.Join(dir, "artifact.mp4")
	err := PrepareFile(context.Background(), TranscodeOpts{
		InputPath:        "/nonexistent/input.mkv",
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		FFmpegPath:       script,
		HWAccel:          HWAccelNone,
	}, outputPath)
	if err == nil {
		t.Fatal("PrepareFile succeeded with a failing FFmpeg process")
	}
	writtenPath, readErr := os.ReadFile(writtenPathMarker)
	if readErr != nil {
		t.Fatalf("read fake FFmpeg output marker: %v", readErr)
	}
	if got, want := strings.TrimSpace(string(writtenPath)), outputPath+".part"; got != want {
		t.Fatalf("fake FFmpeg wrote %q, want %q", got, want)
	}
	if _, statErr := os.Stat(partialExistedMarker); statErr != nil {
		t.Fatalf("fake FFmpeg did not observe its partial output before cleanup: %v", statErr)
	}
	if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
		t.Fatalf("failed prepared output appeared at final path: %v", statErr)
	}
	if _, statErr := os.Stat(outputPath + ".part"); !os.IsNotExist(statErr) {
		t.Fatalf("failed prepared output left a partial file: %v", statErr)
	}
}

func TestPrepareFileRetriesVideoToolboxFailureInSoftware(t *testing.T) {
	setupHWAccelTest(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "ffmpeg")
	logPath := filepath.Join(dir, "invocations.log")
	// Answers capability probes and smoke encodes, fails real VideoToolbox
	// encodes, and succeeds software encodes by writing the output file
	// (ffmpeg's contract PrepareFile finalizes on).
	fake := "#!/bin/sh\n" +
		"printf '%s\n' \"$*\" >> " + logPath + "\n" +
		"case \"$*\" in\n" +
		"  *-hwaccels*) echo videotoolbox; exit 0 ;;\n" +
		"  *-encoders*) echo ' V..... h264_videotoolbox x'; echo ' V..... hevc_videotoolbox x'; exit 0 ;;\n" +
		"  *videotoolbox*'-f null'*) exit 0 ;;\n" +
		"  *videotoolbox*) exit 1 ;;\n" +
		"  *) for last; do :; done; printf x > \"$last\"; exit 0 ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(fake), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}

	outputPath := filepath.Join(t.TempDir(), "artifact.mp4")
	err := PrepareFile(context.Background(), TranscodeOpts{
		InputPath:         "/media/movie.mkv",
		SessionID:         "prepare-vt-retry",
		FFmpegPath:        script,
		SourceVideoCodec:  "h264",
		TargetCodecVideo:  "h264",
		TargetCodecAudio:  "aac",
		TargetResolution:  "720p",
		TargetBitrateKbps: 2000,
		HWAccel:           "videotoolbox",
		AudioTrackIndex:   -1,
	}, outputPath)
	if err != nil {
		t.Fatalf("PrepareFile() error = %v, want software retry to succeed", err)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("finalized artifact missing: %v", err)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read invocation log: %v", err)
	}
	if !strings.Contains(string(log), "h264_videotoolbox -pix_fmt") {
		t.Fatalf("first attempt should encode with VideoToolbox:\n%s", log)
	}
	if !strings.Contains(string(log), "libx264") {
		t.Fatalf("retry should encode with libx264:\n%s", log)
	}
}

func TestPrepareFileDoesNotDropHardwareToneMapGraphOnSoftwareRetry(t *testing.T) {
	setupHWAccelTest(t)
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(inputPath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	track := models.VideoTrack{
		Codec: "hevc", Profile: "Main 10", Level: 153, Width: 3840, Height: 2160, FrameRate: "23.976",
		ColorRange: "tv", ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc",
		BitDepth: 10, PixelFormat: "yuv420p10le",
	}
	revision := tonemap.RevisionForFile(&models.MediaFile{ID: 42, FileSize: info.Size(), VideoTracks: []models.VideoTrack{track}})

	ffmpegPath := filepath.Join(dir, "ffmpeg")
	logPath := filepath.Join(dir, "invocations.log")
	ffmpegScript := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + logPath + "\n" +
		"case \"$*\" in\n" +
		"  *-hwaccels*) echo videotoolbox; exit 0 ;;\n" +
		"  *-encoders*) echo ' V..... h264_videotoolbox x'; echo ' V..... hevc_videotoolbox x'; exit 0 ;;\n" +
		"  *videotoolbox*'-f null'*) exit 0 ;;\n" +
		"  *) exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(ffmpegPath, []byte(ffmpegScript), 0o755); err != nil {
		t.Fatal(err)
	}
	liveJSON := `{"streams":[{"index":0,"codec_name":"hevc","codec_type":"video","profile":"Main 10","level":153,"width":3840,"height":2160,"avg_frame_rate":"24000/1001","pix_fmt":"yuv420p10le","bits_per_raw_sample":"10","color_range":"tv","color_primaries":"bt2020","color_transfer":"smpte2084","color_space":"bt2020nc"}]}`
	if err := os.WriteFile(filepath.Join(dir, "ffprobe"), []byte("#!/bin/sh\nprintf '%s' '"+liveJSON+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(t.TempDir(), "artifact.mp4")
	err = PrepareFile(t.Context(), TranscodeOpts{
		InputPath: inputPath, FFmpegPath: ffmpegPath,
		SourceVideoCodec: "hevc", SourceVideoBitDepth: 10,
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetResolution: "1080p", TargetBitrateKbps: 8000,
		HWAccel:       transcodeHWVideoToolbox,
		ToneMapPolicy: tonemap.PolicyHardwareThenSoftware, ToneMapMode: tonemap.ModeHardware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapFilter: tonemap.HardwareFilterVideoToolbox,
		ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3, ToneMapSourceRevision: revision,
		AudioTrackIndex: -1,
	}, outputPath)
	if err == nil {
		t.Fatal("PrepareFile() succeeded after the hardware tone-map encode failed")
	}
	logData, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(logData), "libx264") {
		t.Fatalf("hardware tone-map failure retried without a valid software recipe:\n%s", logData)
	}
	if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
		t.Fatalf("invalid tone-mapped artifact was published: %v", statErr)
	}
}
