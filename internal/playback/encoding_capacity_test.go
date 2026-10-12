package playback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/encodinglimit"
)

func TestTranscodeAndDownloadRespectSharedWorkerCeiling(t *testing.T) {
	encodinglimit.Configure(1)
	t.Cleanup(func() { encodinglimit.Configure(0) })
	release, err := encodinglimit.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	opts := TranscodeOpts{InputPath: "/input.mkv", OutputDir: t.TempDir(), FFmpegPath: "/does-not-exist", HWAccel: "none", TargetCodecVideo: "h264", TargetCodecAudio: "aac"}
	if session, err := StartTranscode(context.Background(), opts); !errors.Is(err, encodinglimit.ErrAtCapacity) {
		if session != nil {
			_ = session.Close()
		}
		t.Errorf("playback did not enforce the ceiling before launch: %v", err)
	}
	if err := PrepareFile(context.Background(), opts, filepath.Join(t.TempDir(), "download.mp4")); !errors.Is(err, encodinglimit.ErrAtCapacity) {
		t.Errorf("download did not share playback capacity: %v", err)
	}
}

func TestCapacityRefusalClosesUnstartedPipes(t *testing.T) {
	encodinglimit.Configure(1)
	t.Cleanup(func() { encodinglimit.Configure(0) })
	release, err := encodinglimit.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	opts := TranscodeOpts{InputPath: "/input.mkv", OutputDir: t.TempDir(), FFmpegPath: "/does-not-exist", HWAccel: "none", TargetCodecVideo: "h264", TargetCodecAudio: "aac"}
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("descriptor inventory requires Linux procfs")
	}
	for range 20 {
		if _, err := StartTranscode(t.Context(), opts); !errors.Is(err, encodinglimit.ErrAtCapacity) {
			t.Fatal(err)
		}
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) > len(before) {
		t.Fatalf("refused launches leaked %d pipe descriptors", len(after)-len(before))
	}
}

func TestOptionalHEVCCapacityFailureInvalidatesRegistry(t *testing.T) {
	previous := hevcRecipeProbeV3
	hevcRecipeProbeV3 = func(context.Context, string, []byte) (error, error) { return encodinglimit.ErrAtCapacity, nil }
	t.Cleanup(func() { hevcRecipeProbeV3 = previous })
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\necho ' A....D aac AAC'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	registry, err := ProbeTransformationRegistryWithToneMapV3Result(t.Context(), ffmpeg, nil)
	if !registry.Available(TransformationAudioToAACV3) {
		t.Fatal("baseline AAC probes did not succeed")
	}
	if !errors.Is(err, encodinglimit.ErrAtCapacity) {
		t.Fatalf("optional HEVC exhaustion published a cacheable snapshot: %v", err)
	}
}

func TestHardwareCapacityFailureIsNotCached(t *testing.T) {
	resetHWProbeCacheForTest()
	t.Cleanup(resetHWProbeCacheForTest)
	calls := 0
	probe := hwBackendProbe{commandCount: 1, run: func(context.Context, string, string, time.Duration) hwProbeResult {
		calls++
		return hwProbeResult{reason: encodinglimit.ErrAtCapacity.Error(), transient: true}
	}}
	for range 2 {
		cachedHardwareProbeContext(context.Background(), "vaapi", "ffmpeg", "device", probe)
	}
	if calls != 2 {
		t.Fatalf("temporary capacity exhaustion cached a broken GPU: %d calls", calls)
	}
}

func TestHardwareResolutionPreservesCapacityFailure(t *testing.T) {
	env := setupHEVCEncoderTest(t)
	env.addNVIDIADevice(t, "nvidia0")
	ffmpeg := writeFakeFFmpeg(t, fullyCapableProbe())
	encodinglimit.Configure(1)
	t.Cleanup(func() { encodinglimit.Configure(0) })
	release, err := encodinglimit.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := DetectHWAccelWithFFmpegContextResult(t.Context(), "auto", ffmpeg.path, ""); !errors.Is(err, encodinglimit.ErrAtCapacity) {
		t.Errorf("detection lost transient failure: %v", err)
	}
	if _, err := ResolveHWAccelWithFFmpegContextResult(t.Context(), "auto", ffmpeg.path, ""); !errors.Is(err, encodinglimit.ErrAtCapacity) {
		t.Errorf("resolution lost transient failure: %v", err)
	}
	release()
	if resolved, err := ResolveHWAccelWithFFmpegContextResult(t.Context(), "auto", ffmpeg.path, ""); err != nil || resolved != "nvenc" {
		t.Fatalf("free node failed immediate recovery: %s, %v", resolved, err)
	}
}

func TestEncoderSelectionRetriesAfterCapacityIsFreed(t *testing.T) {
	for _, target := range []string{"hevc", "vaapi", "software"} {
		t.Run(target, func(t *testing.T) {
			setupHEVCEncoderTest(t)
			ffmpeg, _ := hevcEncoderTestFFmpeg(t, true, true)
			encodinglimit.Configure(1)
			t.Cleanup(func() { encodinglimit.Configure(0) })
			release, err := encodinglimit.Acquire()
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			resolve := func() error {
				opts := TranscodeOpts{FFmpegPath: ffmpeg, HWAccel: "vaapi", HWDevice: "selected-device"}
				switch target {
				case "hevc":
					opts.TargetCodecVideo = "hevc"
					result, err := resolveHEVCTranscodeEncoder(context.Background(), opts)
					if err == nil && result.HWAccel != "vaapi" {
						t.Error("capacity exhaustion forced software fallback")
					}
					return err
				case "vaapi":
					opts.TargetBitrateKbps = 2000
					result, err := resolveVAAPIRateControl(context.Background(), opts)
					if err == nil && result.vaapiRateControl != "VBR" {
						t.Error("temporary saturation cached an unavailable rate-control mode")
					}
					return err
				default:
					return requireSoftwareEncoder(context.Background(), ffmpeg, "hevc")
				}
			}
			if err := resolve(); !errors.Is(err, encodinglimit.ErrAtCapacity) {
				t.Errorf("lost retryable capacity error: %v", err)
			}
			release()
			if err := resolve(); err != nil {
				t.Fatalf("free node did not immediately recover: %v", err)
			}
		})
	}
}
