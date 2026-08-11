package chapterthumbs

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSoftwareToneMapFilterResolver(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		probeErr   error
		wantFilter string
		wantReason string
		wantError  string
		wantCalls  int
	}{
		{
			name:       "prefers tonemapx bt2390",
			output:     " .S. zscale V->V\n .S. tonemap V->V\n .S. tonemapx V->V\n",
			wantFilter: softwareToneMapFilterBT2390,
			wantCalls:  1,
		},
		{
			name:       "falls back to standard hable",
			output:     " .S. zscale V->V\n .S. tonemap V->V\n",
			wantFilter: softwareToneMapFilterHable,
			wantCalls:  1,
		},
		{
			name:       "requires zscale",
			output:     " .S. tonemapx V->V\n",
			wantReason: reasonToneMapUnsupported,
			wantError:  "lacks the required zscale filter",
			wantCalls:  1,
		},
		{
			name:       "requires a tone map filter",
			output:     " .S. zscale V->V Description mentions tonemap but does not provide it.\n",
			wantReason: reasonToneMapUnsupported,
			wantError:  "lacks the required tonemapx or tonemap filter",
			wantCalls:  1,
		},
		{
			name:       "does not cache transient probe failure",
			output:     "probe stderr",
			probeErr:   errors.New("exit status 1"),
			wantReason: reasonFFmpegProbeFailed,
			wantError:  "FFmpeg filter probe failed: exit status 1: probe stderr",
			wantCalls:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			resolver := newSoftwareToneMapFilterResolver(func(ffmpegPath string) ([]byte, error) {
				calls++
				if cacheKey := softwareToneMapCacheKey(ffmpegPath); cacheKey != "/test/ffmpeg" {
					t.Fatalf("ffmpegPath = %q, cache key = %q, want /test/ffmpeg", ffmpegPath, cacheKey)
				}
				return []byte(tt.output), tt.probeErr
			})

			for _, ffmpegPath := range []string{"/test/ffmpeg", "/test/../test/ffmpeg"} {
				filter, reason, err := resolver.resolve(ffmpegPath)
				if tt.wantError != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantError) {
						t.Fatalf("resolve() error = %v, want containing %q", err, tt.wantError)
					}
					if reason != tt.wantReason {
						t.Fatalf("resolve() reason = %q, want %q", reason, tt.wantReason)
					}
					continue
				}
				if err != nil {
					t.Fatalf("resolve() error = %v", err)
				}
				if filter != tt.wantFilter {
					t.Fatalf("resolve() filter = %q, want %q", filter, tt.wantFilter)
				}
				if reason != "" {
					t.Fatalf("resolve() reason = %q, want empty", reason)
				}
			}
			if calls != tt.wantCalls {
				t.Fatalf("probe calls = %d, want %d", calls, tt.wantCalls)
			}
		})
	}
}

func TestSoftwareToneMapFilterResolverKeepsExecutableSeparateFromCacheKey(t *testing.T) {
	workingDirectory, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}

	calls := 0
	resolver := newSoftwareToneMapFilterResolver(func(ffmpegPath string) ([]byte, error) {
		calls++
		if ffmpegPath != "./ffmpeg" {
			t.Fatalf("probe path = %q, want ./ffmpeg", ffmpegPath)
		}
		return []byte(" .S. zscale V->V\n .S. tonemap V->V\n"), nil
	})

	for _, ffmpegPath := range []string{"./ffmpeg", filepath.Join(workingDirectory, "ffmpeg")} {
		filter, reason, err := resolver.resolve(ffmpegPath)
		if err != nil || reason != "" || filter != softwareToneMapFilterHable {
			t.Fatalf("resolve(%q) = %q, %q, %v", ffmpegPath, filter, reason, err)
		}
	}
	if calls != 1 {
		t.Fatalf("probe calls = %d, want 1 cached call", calls)
	}
}

func TestExtractFrameSoftwareHDRWithoutHardware(t *testing.T) {
	resolver := resolverWithFilters(t, "zscale", "tonemapx")
	var remaining time.Duration
	data, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:               "/media/movie.mkv",
		SeekSeconds:             42.5,
		FFmpegPath:              "/test/ffmpeg",
		HWAccel:                 "none",
		ToneMap:                 true,
		softwareToneMapResolver: resolver,
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

func TestExtractFrameUnsupportedHardwareUsesCPU(t *testing.T) {
	var args []string
	data, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:   "/media/movie.mkv",
		SeekSeconds: 42.5,
		HWAccel:     "nvenc",
		RunFunc: func(_ context.Context, _ string, got []string) ([]byte, error) {
			args = append([]string(nil), got...)
			return []byte("frame"), nil
		},
	})
	if err != nil || reason != "" || string(data) != "frame" {
		t.Fatalf("ExtractFrame() = %q, %q, %v", data, reason, err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-hwaccel") || strings.Contains(joined, "nvenc") {
		t.Fatalf("unsupported chapter-thumbnail accelerator used hardware args: %s", joined)
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
	resolver := newSoftwareToneMapFilterResolver(func(ffmpegPath string) ([]byte, error) {
		probedPath = ffmpegPath
		return []byte(" .S. zscale V->V\n .S. tonemap V->V\n"), nil
	})

	_, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:               "/media/movie.mkv",
		FFmpegPath:              " ./ffmpeg ",
		HWAccel:                 "none",
		ToneMap:                 true,
		softwareToneMapResolver: resolver,
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
	resolver := newSoftwareToneMapFilterResolver(func(string) ([]byte, error) {
		probeCalls++
		if probeCalls == 1 {
			return []byte("temporary stderr"), errors.New("resource temporarily unavailable")
		}
		return []byte(" .S. zscale V->V\n .S. tonemap V->V\n"), nil
	})
	opts := FrameExtractOptions{
		InputPath:               "/media/movie.mkv",
		FFmpegPath:              "/test/ffmpeg",
		HWAccel:                 "none",
		ToneMap:                 true,
		softwareToneMapResolver: resolver,
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
	resolver := newSoftwareToneMapFilterResolver(func(string) ([]byte, error) {
		t.Fatal("software filter probe should not run after hardware success")
		return nil, nil
	})
	calls := 0
	data, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:               "/media/movie.mkv",
		SeekSeconds:             42.5,
		HWAccel:                 "vaapi",
		HWDevice:                "/dev/dri/renderD128",
		ToneMap:                 true,
		softwareToneMapResolver: resolver,
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

func TestExtractFrameHardwareHDRFailureFallsBackWithFreshDeadline(t *testing.T) {
	resolver := resolverWithFilters(t, "zscale", "tonemapx")
	calls := 0
	var hwRemaining, cpuRemaining time.Duration
	data, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:               "/media/movie.mkv",
		SeekSeconds:             42.5,
		HWAccel:                 "vaapi",
		HWDevice:                "/dev/dri/renderD128",
		ToneMap:                 true,
		softwareToneMapResolver: resolver,
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
	resolver := newSoftwareToneMapFilterResolver(func(string) ([]byte, error) {
		t.Fatal("software filter probe should not run for invalid media")
		return nil, nil
	})
	calls := 0
	_, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:               "/media/movie.mkv",
		SeekSeconds:             42.5,
		HWAccel:                 "vaapi",
		HWDevice:                "/dev/dri/renderD128",
		ToneMap:                 true,
		softwareToneMapResolver: resolver,
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
	resolver := resolverWithFilters(t, "zscale")
	calls := 0
	_, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:               "/media/movie.mkv",
		SeekSeconds:             42.5,
		HWAccel:                 "none",
		ToneMap:                 true,
		softwareToneMapResolver: resolver,
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
	resolver := resolverWithFilters(t, "zscale", "tonemap")
	calls := 0
	_, reason, err := ExtractFrame(context.Background(), FrameExtractOptions{
		InputPath:               "/media/movie.mkv",
		SeekSeconds:             42.5,
		HWAccel:                 "vaapi",
		HWDevice:                "/dev/dri/renderD128",
		ToneMap:                 true,
		softwareToneMapResolver: resolver,
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

func TestRemoteExtractTimeoutBudgetsHardwareAndCPUAttempts(t *testing.T) {
	for _, toneMap := range []bool{false, true} {
		extractBudget := extractTimeoutForAttempt(true, toneMap) + extractTimeoutForAttempt(false, toneMap)
		got := remoteExtractTimeout(toneMap)
		if got <= extractBudget {
			t.Fatalf("remoteExtractTimeout(%t) = %s, want more than extract budget %s", toneMap, got, extractBudget)
		}
		if toneMap && got-extractBudget <= softwareToneMapProbeTimeout {
			t.Fatalf(
				"remoteExtractTimeout(true) overhead = %s, want more than probe budget %s",
				got-extractBudget,
				softwareToneMapProbeTimeout,
			)
		}
	}
}

func TestRemoteProbeFailureAllowsPreferredLocalFallback(t *testing.T) {
	if !isInfrastructureRemoteFailure(reasonFFmpegProbeFailed) {
		t.Fatalf("isInfrastructureRemoteFailure(%q) = false, want true", reasonFFmpegProbeFailed)
	}
}

func resolverWithFilters(t *testing.T, filters ...string) *softwareToneMapFilterResolver {
	t.Helper()
	lines := make([]string, 0, len(filters))
	for _, filter := range filters {
		lines = append(lines, " .S. "+filter+" V->V")
	}
	output := strings.Join(lines, "\n")
	return newSoftwareToneMapFilterResolver(func(string) ([]byte, error) {
		return []byte(output), nil
	})
}

func assertApproximateDeadline(t *testing.T, got time.Duration, want time.Duration) {
	t.Helper()
	if got < want-time.Second || got > want+time.Second {
		t.Fatalf("deadline remaining = %s, want about %s", got, want)
	}
}
