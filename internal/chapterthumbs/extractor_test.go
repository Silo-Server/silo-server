package chapterthumbs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

func TestExtractFrameSoftwareHDRWithoutHardware(t *testing.T) {
	resolver := capabilitiesWithFilters(t, "zscale", "tonemapx")
	var remaining time.Duration
	data, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:            "/media/movie.mkv",
		SeekSeconds:          42.5,
		FFmpegPath:           "/test/ffmpeg",
		HWAccel:              hwAccelNone,
		ToneMap:              true,
		AllowSoftwareToneMap: true,
		loadCapabilities:     resolver,
		RunFunc: func(ctx context.Context, _ string, args []string) ([]byte, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("software HDR extraction has no deadline")
			}
			remaining = time.Until(deadline)
			if !slices.Contains(args, softwareToneMapFilterBT2390) {
				t.Fatalf("software extraction args missing BT.2390 filter: %#v", args)
			}
			return []byte("frame"), nil
		},
	})
	if err != nil {
		t.Fatalf("ExtractFrame() error = %v", err)
	}
	if reason != "" {
		t.Fatalf("ExtractFrame() reason = %q, want empty", reason)
	}
	if string(data) != "frame" {
		t.Fatalf("ExtractFrame() data = %q, want frame", data)
	}
	assertApproximateDeadline(t, remaining, cpuExtractTimeoutHDR)
}

func TestExtractFramePassesCallerContextToHWAccelResolution(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	called := false
	_, _, _ = ExtractFrame(ctx, FrameExtractOptions{
		InputPath:  "/media/movie.mkv",
		FFmpegPath: "/test/ffmpeg",
		HWAccel:    "auto",
		resolveHWAccel: func(gotCtx context.Context, hwAccel, ffmpegPath, _ string) string {
			called = true
			if gotCtx != ctx {
				t.Fatal("hardware probe did not receive the extraction context")
			}
			if hwAccel != "auto" || ffmpegPath != "/test/ffmpeg" {
				t.Fatalf("hardware probe arguments = %q, %q", hwAccel, ffmpegPath)
			}
			return hwAccelNone
		},
		RunFunc: func(context.Context, string, []string) ([]byte, error) {
			return nil, context.Canceled
		},
	})
	if !called {
		t.Fatal("hardware probe was not called")
	}
}

func TestExtractFrameSoftwareHDRDisabledByDefault(t *testing.T) {
	resolver := capabilitiesFromListing(func(string) ([]byte, error) {
		t.Fatal("software filter probe should not run while CPU tone mapping is disabled")
		return nil, nil
	})

	_, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:        "/media/movie.mkv",
		FFmpegPath:       "/test/ffmpeg",
		HWAccel:          hwAccelNone,
		ToneMap:          true,
		loadCapabilities: resolver,
		RunFunc: func(context.Context, string, []string) ([]byte, error) {
			t.Fatal("CPU extraction should not run while CPU tone mapping is disabled")
			return nil, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "software HDR tone mapping is disabled") {
		t.Fatalf("ExtractFrame() error = %v, want disabled software tone-map error", err)
	}
	if reason != reasonToneMapUnsupported {
		t.Fatalf("ExtractFrame() reason = %q, want %q", reason, reasonToneMapUnsupported)
	}
}

func TestExtractFrameVideoToolboxUsesHardwareDecodeOnce(t *testing.T) {
	calls := 0
	data, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:   "/media/movie.mkv",
		SeekSeconds: 42.5,
		HWAccel:     "videotoolbox",
		RunFunc: func(_ context.Context, _ string, args []string) ([]byte, error) {
			calls++
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, "-hwaccel videotoolbox") {
				t.Fatalf("VideoToolbox extraction missing hardware decode: %s", joined)
			}
			if strings.Contains(joined, "hwdownload") || strings.Contains(joined, "-hwaccel_output_format") {
				t.Fatalf("VideoToolbox extraction must keep software frames: %s", joined)
			}
			return []byte("frame"), nil
		},
	})
	if err != nil || reason != "" || string(data) != "frame" {
		t.Fatalf("ExtractFrame() = %q, %q, %v", data, reason, err)
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d, want 1", calls)
	}
}

func TestExtractFrameVideoToolboxHDRUsesSoftwareToneMap(t *testing.T) {
	resolver := capabilitiesWithFilters(t, "zscale", "tonemapx")
	data, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:            "/media/hdr.mkv",
		SeekSeconds:          42.5,
		FFmpegPath:           "/test/ffmpeg",
		HWAccel:              "videotoolbox",
		ToneMap:              true,
		AllowSoftwareToneMap: true,
		loadCapabilities:     resolver,
		RunFunc: func(_ context.Context, _ string, args []string) ([]byte, error) {
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, "-hwaccel videotoolbox") {
				t.Fatalf("VideoToolbox HDR extraction missing hardware decode: %s", joined)
			}
			if !strings.Contains(joined, softwareToneMapFilterBT2390) {
				t.Fatalf("VideoToolbox HDR extraction missing software tone map: %s", joined)
			}
			return []byte("frame"), nil
		},
	})
	if err != nil || reason != "" || string(data) != "frame" {
		t.Fatalf("ExtractFrame() = %q, %q, %v", data, reason, err)
	}
}

func TestExtractFrameUnsupportedHardwarePreservesSDRRetry(t *testing.T) {
	calls := 0
	var firstRemaining, retryRemaining time.Duration
	data, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:   "/media/movie.mkv",
		SeekSeconds: 42.5,
		HWAccel:     "nvenc",
		RunFunc: func(ctx context.Context, _ string, args []string) ([]byte, error) {
			calls++
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatalf("extraction attempt %d has no deadline", calls)
			}
			if strings.Contains(strings.Join(args, " "), "-hwaccel") {
				t.Fatalf("unsupported accelerator used hardware args: %#v", args)
			}
			if calls == 1 {
				firstRemaining = time.Until(deadline)
				return nil, errors.New("transient software decode failure")
			}
			retryRemaining = time.Until(deadline)
			return []byte("frame"), nil
		},
	})
	if err != nil || reason != "" || string(data) != "frame" {
		t.Fatalf("ExtractFrame() = %q, %q, %v", data, reason, err)
	}
	if calls != 2 {
		t.Fatalf("extract calls = %d, want 2", calls)
	}
	assertApproximateDeadline(t, firstRemaining, hwExtractTimeoutSDR)
	assertApproximateDeadline(t, retryRemaining, cpuExtractTimeoutSDR)
}

func TestExtractFrameProbesAndRunsSameRelativeFFmpeg(t *testing.T) {
	var probedPath, extractPath string
	resolver := capabilitiesFromListing(func(ffmpegPath string) ([]byte, error) {
		probedPath = ffmpegPath
		return []byte(" .S. zscale V->V\n .S. tonemap V->V\n"), nil
	})

	_, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:            "/media/movie.mkv",
		FFmpegPath:           " ./ffmpeg ",
		HWAccel:              hwAccelNone,
		ToneMap:              true,
		AllowSoftwareToneMap: true,
		loadCapabilities:     resolver,
		RunFunc: func(_ context.Context, ffmpegPath string, _ []string) ([]byte, error) {
			extractPath = ffmpegPath
			return []byte("frame"), nil
		},
	})
	if err != nil || reason != "" {
		t.Fatalf("ExtractFrame() reason = %q, error = %v", reason, err)
	}
	if probedPath != "./ffmpeg" || extractPath != "./ffmpeg" {
		t.Fatalf("probe path = %q, extract path = %q, want ./ffmpeg for both", probedPath, extractPath)
	}
}

func TestExtractFrameRetriesTransientSoftwareProbeFailure(t *testing.T) {
	probeCalls := 0
	resolver := capabilitiesFromListing(func(string) ([]byte, error) {
		probeCalls++
		if probeCalls == 1 {
			return []byte("temporary stderr"), errors.New("resource temporarily unavailable")
		}
		return []byte(" .S. zscale V->V\n .S. tonemap V->V\n"), nil
	})
	opts := FrameExtractOptions{
		InputPath:            "/media/movie.mkv",
		FFmpegPath:           "/test/ffmpeg",
		HWAccel:              hwAccelNone,
		ToneMap:              true,
		AllowSoftwareToneMap: true,
		loadCapabilities:     resolver,
		RunFunc: func(context.Context, string, []string) ([]byte, error) {
			return []byte("frame"), nil
		},
	}

	if _, reason, err := ExtractFrame(context.Background(), opts); err == nil || reason != reasonFFmpegProbeFailed {
		t.Fatalf("first ExtractFrame() reason = %q, error = %v, want retryable probe failure", reason, err)
	}
	data, reason, err := ExtractFrame(context.Background(), opts)
	if err != nil || reason != "" || string(data) != "frame" {
		t.Fatalf("second ExtractFrame() = %q, %q, %v", data, reason, err)
	}
	if probeCalls != 2 {
		t.Fatalf("probe calls = %d, want 2", probeCalls)
	}
}

func TestExtractFrameHardwareHDRSuccessSkipsSoftwareProbe(t *testing.T) {
	resolver := capabilitiesFromListing(func(string) ([]byte, error) {
		t.Fatal("software filter probe should not run after hardware success")
		return nil, nil
	})
	calls := 0
	data, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:        "/media/movie.mkv",
		SeekSeconds:      42.5,
		HWAccel:          "vaapi",
		HWDevice:         "/dev/dri/renderD128",
		ToneMap:          true,
		loadCapabilities: resolver,
		RunFunc: func(_ context.Context, _ string, args []string) ([]byte, error) {
			calls++
			if !strings.Contains(strings.Join(args, " "), "tonemap_vaapi") {
				t.Fatalf("hardware extraction args missing VAAPI tone map: %#v", args)
			}
			return []byte("frame"), nil
		},
	})
	if err != nil || reason != "" || string(data) != "frame" {
		t.Fatalf("ExtractFrame() = %q, %q, %v", data, reason, err)
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d, want 1", calls)
	}
}

func TestExtractFrameHardwareHDRFailureDoesNotUseSoftwareWhenDisabled(t *testing.T) {
	resolver := capabilitiesFromListing(func(string) ([]byte, error) {
		t.Fatal("software filter probe should not run while CPU tone mapping is disabled")
		return nil, nil
	})
	calls := 0
	_, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:        "/media/movie.mkv",
		HWAccel:          "vaapi",
		HWDevice:         "/dev/dri/renderD128",
		ToneMap:          true,
		loadCapabilities: resolver,
		RunFunc: func(context.Context, string, []string) ([]byte, error) {
			calls++
			return nil, context.DeadlineExceeded
		},
	})
	if err == nil {
		t.Fatal("ExtractFrame() error = nil, want hardware failure")
	}
	if reason != "hw_timeout" {
		t.Fatalf("ExtractFrame() reason = %q, want hw_timeout", reason)
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d, want hardware attempt only", calls)
	}
}

func TestExtractFrameHardwareHDRFailureFallsBackWithFreshDeadline(t *testing.T) {
	resolver := capabilitiesWithFilters(t, "zscale", "tonemapx")
	calls := 0
	var hwRemaining, cpuRemaining time.Duration
	data, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:            "/media/movie.mkv",
		SeekSeconds:          42.5,
		HWAccel:              "vaapi",
		HWDevice:             "/dev/dri/renderD128",
		ToneMap:              true,
		AllowSoftwareToneMap: true,
		loadCapabilities:     resolver,
		RunFunc: func(ctx context.Context, _ string, args []string) ([]byte, error) {
			calls++
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatalf("extraction attempt %d has no deadline", calls)
			}
			if calls == 1 {
				hwRemaining = time.Until(deadline)
				return nil, context.DeadlineExceeded
			}
			cpuRemaining = time.Until(deadline)
			if !slices.Contains(args, softwareToneMapFilterBT2390) {
				t.Fatalf("CPU fallback args missing BT.2390 filter: %#v", args)
			}
			return []byte("frame"), nil
		},
	})
	if err != nil || reason != "" || string(data) != "frame" {
		t.Fatalf("ExtractFrame() = %q, %q, %v", data, reason, err)
	}
	if calls != 2 {
		t.Fatalf("extract calls = %d, want 2", calls)
	}
	assertApproximateDeadline(t, hwRemaining, hwExtractTimeoutHDR)
	assertApproximateDeadline(t, cpuRemaining, cpuExtractTimeoutHDR)
}

func TestExtractFrameInvalidHardwareMediaDoesNotRetryOnCPU(t *testing.T) {
	resolver := capabilitiesFromListing(func(string) ([]byte, error) {
		t.Fatal("software filter probe should not run for invalid media")
		return nil, nil
	})
	calls := 0
	_, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:        "/media/movie.mkv",
		SeekSeconds:      42.5,
		HWAccel:          "vaapi",
		HWDevice:         "/dev/dri/renderD128",
		ToneMap:          true,
		loadCapabilities: resolver,
		RunFunc: func(context.Context, string, []string) ([]byte, error) {
			calls++
			return nil, errors.New("Invalid NAL unit size")
		},
	})
	if err == nil {
		t.Fatal("ExtractFrame() error = nil, want invalid-media error")
	}
	if reason != "decode_invalid_data" {
		t.Fatalf("ExtractFrame() reason = %q, want decode_invalid_data", reason)
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d, want 1", calls)
	}
}

func TestExtractFrameMissingSoftwareFiltersIsActionable(t *testing.T) {
	resolver := capabilitiesWithFilters(t, "zscale")
	calls := 0
	_, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:            "/media/movie.mkv",
		SeekSeconds:          42.5,
		HWAccel:              hwAccelNone,
		ToneMap:              true,
		AllowSoftwareToneMap: true,
		loadCapabilities:     resolver,
		RunFunc: func(context.Context, string, []string) ([]byte, error) {
			calls++
			return nil, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "lacks the required tonemapx or tonemap filter") {
		t.Fatalf("ExtractFrame() error = %v, want actionable missing-filter error", err)
	}
	if reason != "tonemap_unsupported" {
		t.Fatalf("ExtractFrame() reason = %q, want tonemap_unsupported", reason)
	}
	if calls != 0 {
		t.Fatalf("extract calls = %d, want 0", calls)
	}
}

func TestExtractFramePreservesHardwareAndCPUFailures(t *testing.T) {
	resolver := capabilitiesWithFilters(t, "zscale", "tonemap")
	calls := 0
	_, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:            "/media/movie.mkv",
		SeekSeconds:          42.5,
		HWAccel:              "vaapi",
		HWDevice:             "/dev/dri/renderD128",
		ToneMap:              true,
		AllowSoftwareToneMap: true,
		loadCapabilities:     resolver,
		RunFunc: func(context.Context, string, []string) ([]byte, error) {
			calls++
			if calls == 1 {
				return nil, context.DeadlineExceeded
			}
			return nil, errors.New("software decode failed")
		},
	})
	if err == nil {
		t.Fatal("ExtractFrame() error = nil, want combined failure")
	}
	if reason != "chapter_extract_failed" {
		t.Fatalf("ExtractFrame() reason = %q, want chapter_extract_failed", reason)
	}
	for _, want := range []string{"hardware extraction failed", "hw_timeout", "cpu fallback failed", "software decode failed"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ExtractFrame() error = %q, want containing %q", err, want)
		}
	}
}

func TestRemoteExtractTimeoutBudgetsOnlyAllowedAttempts(t *testing.T) {
	sdrExtractBudget := extractTimeoutForAttempt(true, false) + extractTimeoutForAttempt(false, false)
	if got := remoteExtractTimeout(false, false); got <= sdrExtractBudget {
		t.Fatalf("remoteExtractTimeout(SDR) = %s, want more than extract budget %s", got, sdrExtractBudget)
	}

	hardwareHDRBudget := extractTimeoutForAttempt(true, true)
	softwareHDRBudget := extractTimeoutForAttempt(false, true)
	if got := remoteExtractTimeout(true, false); got <= hardwareHDRBudget || got >= hardwareHDRBudget+softwareHDRBudget {
		t.Fatalf("remoteExtractTimeout(HDR hardware only) = %s, want hardware plus overhead without CPU budget", got)
	}

	hdrExtractBudget := hardwareHDRBudget + softwareHDRBudget
	if got := remoteExtractTimeout(true, true); got-hdrExtractBudget <= softwareToneMapProbeTimeout {
		t.Fatalf(
			"remoteExtractTimeout(HDR with CPU) overhead = %s, want more than probe budget %s",
			got-hdrExtractBudget,
			softwareToneMapProbeTimeout,
		)
	}
}

func TestRemoteProbeFailureAllowsPreferredLocalFallback(t *testing.T) {
	if !isInfrastructureRemoteFailure(reasonFFmpegProbeFailed) {
		t.Fatalf("isInfrastructureRemoteFailure(%q) = false, want true", reasonFFmpegProbeFailed)
	}
}

// softwareToneMapFilterBT2390 is the software tone-map chain mediasample picks
// when ffmpeg lists tonemapx, byte for byte.
const softwareToneMapFilterBT2390 = "tonemapx=tonemap=bt2390,zscale=p=bt709:t=bt709:m=bt709:r=tv,format=yuv420p"

// capabilitiesFromListing loads capabilities from the ffmpeg -filters listing
// probe returns.
func capabilitiesFromListing(probe func(ffmpegPath string) ([]byte, error)) func(context.Context, string) (mediasample.Capabilities, error) {
	return func(_ context.Context, ffmpegPath string) (mediasample.Capabilities, error) {
		output, err := probe(ffmpegPath)
		if err != nil {
			return mediasample.Capabilities{}, err
		}
		return mediasample.FilterCapabilities(output), nil
	}
}

func capabilitiesWithFilters(t *testing.T, filters ...string) func(context.Context, string) (mediasample.Capabilities, error) {
	t.Helper()
	lines := make([]string, 0, len(filters))
	for _, filter := range filters {
		lines = append(lines, " .S. "+filter+" V->V")
	}
	output := strings.Join(lines, "\n")
	return capabilitiesFromListing(func(string) ([]byte, error) {
		return []byte(output), nil
	})
}

func assertApproximateDeadline(t *testing.T, got time.Duration, want time.Duration) {
	t.Helper()
	if got < want-time.Second || got > want+time.Second {
		t.Fatalf("deadline remaining = %s, want about %s", got, want)
	}
}

// TestExtractFrameKeepsLegacyReasonsAndFallback pins the persisted reasons
// and fallback rules chapter thumbnails had before they moved onto
// mediasample, where mediasample's own causes would differ.
func TestExtractFrameKeepsLegacyReasonsAndFallback(t *testing.T) {
	tests := []struct {
		name       string
		accel      string
		toneMap    bool
		results    []string
		wantReason string
		wantCalls  int
	}{
		{name: "unknown option is not a tone-map failure", accel: hwAccelNone, results: []string{"Unrecognized option 'x'.\nError splitting the argument list: Option not found"}, wantReason: reasonChapterExtractFailed, wantCalls: 1},
		{name: "missing decoder is not a tone-map failure", accel: hwAccelNone, results: []string{"Decoder not found"}, wantReason: reasonChapterExtractFailed, wantCalls: 1},
		{name: "missing filter is a tone-map failure", accel: hwAccelNone, results: []string{"[AVFilterGraph @ 0x1] No such filter: 'zscale'"}, wantReason: reasonToneMapUnsupported, wantCalls: 1},
		{name: "hardware tone-map error", accel: "vaapi", toneMap: true, results: []string{"[Parsed_tonemap_vaapi_2 @ 0x1] Failed to create processing pipeline.\nError reinitializing filters!"}, wantReason: reasonToneMapUnsupported, wantCalls: 1},
		{name: "hardware-only plan with a missing decoder", accel: "vaapi", toneMap: true, results: []string{"Decoder not found"}, wantReason: reasonChapterExtractFailed, wantCalls: 1},
		{name: "hardware without a stream falls back", accel: "vaapi", results: []string{"Output file #0 does not contain any stream", ""}, wantCalls: 2},
		{name: "hardware killed while logging invalid data stops", accel: "qsv", results: []string{"signal: killed (Invalid data found when processing input)"}, wantReason: reasonDecodeInvalidData, wantCalls: 1},
		{name: "sdr retry after invalid data", accel: "nvenc", results: []string{"Invalid data found when processing input", ""}, wantCalls: 2},
		{name: "sdr retry fails twice", accel: "nvenc", results: []string{"Invalid data found when processing input", "signal: killed"}, wantReason: reasonChapterExtractFailed, wantCalls: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			data, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
				InputPath:      "/media/movie.mkv",
				SeekSeconds:    42.5,
				HWAccel:        tt.accel,
				HWDevice:       "/dev/dri/renderD128",
				ToneMap:        tt.toneMap,
				resolveHWAccel: func(_ context.Context, accel, _, _ string) string { return accel },
				RunFunc: func(context.Context, string, []string) ([]byte, error) {
					calls++
					if message := tt.results[calls-1]; message != "" {
						return nil, errors.New(message)
					}
					return []byte("frame"), nil
				},
			})
			if reason != tt.wantReason || calls != tt.wantCalls {
				t.Fatalf("ExtractFrame() reason %q after %d calls (error %v), want %q after %d", reason, calls, err, tt.wantReason, tt.wantCalls)
			}
			if (tt.wantReason == "") != (err == nil && string(data) == "frame") {
				t.Fatalf("ExtractFrame() = %q, %v, want a frame only without a reason", data, err)
			}
		})
	}
}

func TestExtractReasonForAttemptTimeouts(t *testing.T) {
	timeout := mediasample.AttemptError{Reason: mediasample.ReasonTimeout, Err: context.DeadlineExceeded}
	plan := extractPlan{attempts: extractAttempts("vaapi", false, false), accel: "vaapi"}
	if got := plan.reason(plan.attempts[0], timeout, false); got != reasonHWTimeout {
		t.Fatalf("hardware attempt timeout reason = %q, want %q", got, reasonHWTimeout)
	}
	if got := plan.reason(plan.attempts[1], timeout, true); got != reasonCPUTimeout {
		t.Fatalf("software attempt timeout reason = %q, want %q", got, reasonCPUTimeout)
	}
	canceled := mediasample.AttemptError{Reason: mediasample.ReasonCanceled, Err: context.Canceled}
	if got := plan.reason(plan.attempts[0], canceled, false); got != reasonChapterExtractFailed {
		t.Fatalf("canceled attempt reason = %q, want %q", got, reasonChapterExtractFailed)
	}
}
