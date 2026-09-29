package playback

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// These tests verify codec admission, not probe deadlines. Preserve the
// production timeout so package-level parallelism cannot kill healthy fakes.
func setupHEVCEncoderTest(t *testing.T) *hwAccelTestEnv {
	t.Helper()
	timeout := hwProbeCommandTimeout
	env := setupHWAccelTest(t)
	hwProbeCommandTimeout = timeout
	return env
}

func hevcEncoderTestFFmpeg(t *testing.T, hardwareOK, softwareOK bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ffmpeg")
	logPath := filepath.Join(dir, "commands")
	exitStatus := func(ok bool) int {
		if ok {
			return 0
		}
		return 1
	}
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> '%s'
case " $* " in
*' -hwaccels '*) echo videotoolbox; exit 0 ;;
*' -encoders '*) echo ' V....D h264_videotoolbox'; echo ' V....D hevc_videotoolbox'; exit 0 ;;
*' -c:v hevc_'*) exit %d ;;
*' -c:v libx265 '*) exit %d ;;
esac
`, logPath, exitStatus(hardwareOK), exitStatus(softwareOK))
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, logPath
}

func TestStartTranscodeHEVCVerifiesSelectedHardwareEncoder(t *testing.T) {
	for _, backend := range []string{transcodeHWQSV, transcodeHWVAAPI, transcodeHWNVENC} {
		for _, supported := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/supported=%t", backend, supported), func(t *testing.T) {
				setupHEVCEncoderTest(t)
				ffmpeg, logPath := hevcEncoderTestFFmpeg(t, supported, true)
				opts := TranscodeOpts{
					InputPath: "/media/source.mkv", OutputDir: t.TempDir(), FFmpegPath: ffmpeg,
					TargetCodecVideo: transcodeCodecHEVC, TargetCodecAudio: "aac", VideoSampleEntry: VideoSampleEntryHVC1,
					HWAccel: backend, HWDevice: "selected-device", TargetResolution: "1080p",
				}
				session, err := StartTranscode(context.Background(), opts)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(session.cancel)
				<-session.done
				wantBackend := transcodeHWNone
				if supported {
					wantBackend = backend
				}
				actual := session.Opts()
				if actual.HWAccel != wantBackend || actual.TargetCodecVideo != transcodeCodecHEVC || actual.VideoSampleEntry != VideoSampleEntryHVC1 {
					t.Fatalf("executed backend/codec/tag = %q/%q/%q, want %q/hevc/hvc1", actual.HWAccel, actual.TargetCodecVideo, actual.VideoSampleEntry, wantBackend)
				}
				commands, err := os.ReadFile(logPath)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(commands), "selected-device") || !strings.Contains(string(commands), "-c:v hevc_"+backend) {
					t.Fatalf("selected GPU did not receive HEVC probe:\n%s", commands)
				}
			})
		}
	}
}

func TestHEVCSoftwareFallbackPreservesHardwareToneMapping(t *testing.T) {
	for _, backend := range []string{transcodeHWQSV, transcodeHWVAAPI, transcodeHWNVENC, transcodeHWVideoToolbox} {
		for _, subtitle := range []string{"none", "text", "bitmap"} {
			t.Run(backend+"/"+subtitle, func(t *testing.T) {
				setupHEVCEncoderTest(t)
				ffmpeg, _ := hevcEncoderTestFFmpeg(t, false, true)
				filter := map[string]string{
					transcodeHWQSV: tonemap.HardwareFilterOpenCL, transcodeHWVAAPI: tonemap.HardwareFilterVAAPI,
					transcodeHWNVENC: tonemap.HardwareFilterCUDA, transcodeHWVideoToolbox: tonemap.HardwareFilterVideoToolbox,
				}[backend]
				opts := TranscodeOpts{
					InputPath: "/media/source.mkv", OutputDir: t.TempDir(), FFmpegPath: ffmpeg,
					TargetCodecVideo: transcodeCodecHEVC, HWAccel: backend, HWDevice: "selected-device", TargetResolution: "1080p",
					ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourcePQ, ToneMapFilter: filter,
					SourceVideoCodec: transcodeCodecHEVC, SourceVideoProfile: "Main 10", SourceVideoBitDepth: 10,
				}
				if subtitle != "none" {
					opts.SubtitleBurnIn = true
					opts.SubtitleTrackIndex = 0
					opts.SubtitleCodec = "subrip"
					if subtitle == "bitmap" {
						opts.SubtitleCodec = "hdmv_pgs_subtitle"
					}
				}
				resolved, err := resolveHEVCTranscodeEncoder(context.Background(), opts)
				if err != nil {
					t.Fatal(err)
				}
				if resolved.HWAccel != backend || resolved.ToneMapMode != tonemap.ModeHardware || resolved.ToneMapFilter != filter {
					t.Fatalf("fallback changed frozen tone-map executor: %#v", resolved)
				}
				args := strings.Join(buildFFmpegArgs(resolved), " ")
				if !strings.Contains(args, "-c:v libx265") || !strings.Contains(args, filter) {
					t.Fatalf("fallback lost software encode or hardware conversion: %s", args)
				}
				if backend != transcodeHWVideoToolbox {
					end := "hwdownload,format=nv12"
					if subtitle == "bitmap" {
						end += "[vout]"
					}
					if !strings.Contains(args, end+" -") {
						t.Fatalf("fallback does not download converted output for libx265: %s", args)
					}
				}
			})
		}
	}
}

func TestHEVCEncoderRejectsUnverifiedSoftwareFallback(t *testing.T) {
	setupHEVCEncoderTest(t)
	ffmpeg, logPath := hevcEncoderTestFFmpeg(t, false, false)
	session, err := StartTranscode(context.Background(), TranscodeOpts{
		InputPath: "/media/source.mkv", OutputDir: t.TempDir(), FFmpegPath: ffmpeg,
		TargetCodecVideo: transcodeCodecHEVC, HWAccel: transcodeHWVAAPI, HWDevice: "selected-device",
	})
	if err == nil || session != nil {
		if session != nil {
			session.cancel()
			<-session.done
		}
		t.Fatal("started HEVC without a working hardware or software encoder")
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), "/media/source.mkv") {
		t.Fatalf("unverified recipe reached media execution: %s", commands)
	}
	if count := hwDeviceActiveCount("selected-device"); count != 0 {
		t.Fatalf("failed HEVC startup leaked %d GPU reservations", count)
	}
}

func TestHEVCEncoderProbeCacheSeparatesDevicesAndInvalidates(t *testing.T) {
	setupHEVCEncoderTest(t)
	ffmpeg, logPath := hevcEncoderTestFFmpeg(t, false, true)
	opts := TranscodeOpts{FFmpegPath: ffmpeg, TargetCodecVideo: transcodeCodecHEVC, HWAccel: transcodeHWVAAPI}
	for _, device := range []string{"first-device", "first-device", "second-device"} {
		opts.HWDevice = device
		if _, err := resolveHEVCTranscodeEncoder(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
	}
	checkProbes := func(hardwareCount, softwareCount int) {
		t.Helper()
		commands, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(commands), "-c:v hevc_vaapi") != hardwareCount || strings.Count(string(commands), "-c:v libx265") != softwareCount {
			t.Fatalf("unexpected probe cache reuse:\n%s", commands)
		}
	}
	checkProbes(2, 1)
	InvalidateHWProbeCache()
	if _, err := resolveHEVCTranscodeEncoder(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	checkProbes(3, 2)

	working, _ := hevcEncoderTestFFmpeg(t, true, false)
	opts.FFmpegPath = working
	opts.softwareHEVCEncode = true
	opts.EncoderHWAccel = transcodeHWNone
	resolved, err := resolveHEVCTranscodeEncoder(context.Background(), opts)
	if err != nil || resolved.softwareHEVCEncode || resolved.EffectiveEncoderHWAccel() != transcodeHWVAAPI {
		t.Fatalf("healthy HEVC encoder kept a stale software fallback: %v, %#v", err, resolved)
	}
}

func TestStartTranscodeHEVCProbesAllocatedDevice(t *testing.T) {
	env := setupHEVCEncoderTest(t)
	env.addRenderDevice(t, "renderD128", "0x1002")
	env.addRenderDevice(t, "renderD129", "0x1002")
	first := filepath.Join(env.driDir, "renderD128")
	second := filepath.Join(env.driDir, "renderD129")
	releaseFirst := countHWDeviceWorkload(first)
	defer releaseFirst()
	ffmpeg, logPath := hevcEncoderTestFFmpeg(t, true, true)
	session, err := StartTranscode(context.Background(), TranscodeOpts{
		InputPath: "/media/source.mkv", OutputDir: t.TempDir(), FFmpegPath: ffmpeg,
		TargetCodecVideo: transcodeCodecHEVC, HWAccel: transcodeHWVAAPI, HWDevice: first + "," + second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.cancel)
	<-session.done
	if session.Opts().HWDevice != second {
		t.Fatalf("transcode ignored selected device: %q", session.Opts().HWDevice)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(commands), second) || strings.Contains(string(commands), first) {
		t.Fatalf("HEVC probe did not follow allocation:\n%s", commands)
	}
}

func TestHEVCFallbackProducesDecodableMain8FMP4(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe := ffprobePathFromFFmpeg(ffmpeg)
	if _, err := exec.LookPath(ffprobe); err != nil {
		t.Skip("ffprobe is not installed beside ffmpeg")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	encoders, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-encoders").CombinedOutput()
	if err != nil || !strings.Contains(string(encoders), "libx265") {
		t.Skip("ffmpeg does not provide libx265")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mkv")
	if output, err := exec.CommandContext(ctx, ffmpeg,
		"-v", "error", "-f", "lavfi", "-i", "testsrc2=size=128x72:rate=12", "-t", "4", "-c:v", "ffv1", source,
	).CombinedOutput(); err != nil {
		t.Fatalf("generate synthetic source: %v\n%s", err, output)
	}
	session, err := StartTranscode(ctx, TranscodeOpts{
		InputPath: source, OutputDir: dir, FFmpegPath: ffmpeg, SourceVideoCodec: "ffv1",
		TargetCodecVideo: transcodeCodecHEVC, TargetCodecAudio: "copy", VideoSampleEntry: VideoSampleEntryHVC1,
		HWAccel: transcodeHWVAAPI, HWDevice: filepath.Join(dir, "missing-render-device"), SegmentDuration: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.cancel(); <-session.done })
	select {
	case <-session.done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := session.WaitError(); err != nil {
		t.Fatalf("encode fallback: %v\n%s", err, session.stderr.String())
	}
	if session.Opts().HWAccel != transcodeHWNone {
		t.Fatal("unavailable VAAPI encoder did not select software")
	}
	manifest := filepath.Join(dir, "stream.m3u8")
	output, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name,profile,codec_tag_string,pix_fmt", "-of", "json", manifest,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("probe fallback: %v\n%s", err, output)
	}
	var report struct {
		Streams []struct {
			Codec   string `json:"codec_name"`
			Profile string `json:"profile"`
			Tag     string `json:"codec_tag_string"`
			Format  string `json:"pix_fmt"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &report); err != nil || len(report.Streams) != 1 {
		t.Fatalf("invalid stream report: %v\n%s", err, output)
	}
	stream := report.Streams[0]
	if stream.Codec != "hevc" || stream.Profile != "Main" || stream.Tag != "hvc1" || stream.Format != "yuv420p" {
		t.Fatalf("fallback violated negotiated Main 8-bit hvc1 recipe: %+v", stream)
	}
	if output, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-xerror", "-i", manifest, "-f", "null", "-").CombinedOutput(); err != nil {
		t.Fatalf("decode fallback: %v\n%s", err, output)
	}
}
