package chapterthumbs

import (
	"context"
	"errors"
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
		wantError  string
	}{
		{
			name:       "prefers tonemapx bt2390",
			output:     " .S. zscale V->V\n .S. tonemap V->V\n .S. tonemapx V->V\n",
			wantFilter: softwareToneMapFilterBT2390,
		},
		{
			name:       "falls back to standard hable",
			output:     " .S. zscale V->V\n .S. tonemap V->V\n",
			wantFilter: softwareToneMapFilterHable,
		},
		{
			name:      "requires zscale",
			output:    " .S. tonemapx V->V\n",
			wantError: "lacks the required zscale filter",
		},
		{
			name:      "requires a tone map filter",
			output:    " .S. zscale V->V\n",
			wantError: "lacks the required tonemapx or tonemap filter",
		},
		{
			name:      "reports probe failure",
			output:    "probe stderr",
			probeErr:  errors.New("exit status 1"),
			wantError: "FFmpeg filter probe failed: exit status 1: probe stderr",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			resolver := newSoftwareToneMapFilterResolver(func(ffmpegPath string) ([]byte, error) {
				calls++
				if ffmpegPath != "/test/ffmpeg" {
					t.Fatalf("ffmpegPath = %q, want /test/ffmpeg", ffmpegPath)
				}
				return []byte(tt.output), tt.probeErr
			})

			for _, ffmpegPath := range []string{"/test/ffmpeg", " /test/../test/ffmpeg "} {
				filter, err := resolver.resolve(ffmpegPath)
				if tt.wantError != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantError) {
						t.Fatalf("resolve() error = %v, want containing %q", err, tt.wantError)
					}
					continue
				}
				if err != nil {
					t.Fatalf("resolve() error = %v", err)
				}
				if filter != tt.wantFilter {
					t.Fatalf("resolve() filter = %q, want %q", filter, tt.wantFilter)
				}
			}
			if calls != 1 {
				t.Fatalf("probe calls = %d, want 1 cached call", calls)
			}
		})
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
	if got, want := remoteExtractTimeout(false), hwExtractTimeoutSDR+cpuExtractTimeoutSDR+3*time.Second; got != want {
		t.Fatalf("remoteExtractTimeout(false) = %s, want %s", got, want)
	}
	if got, want := remoteExtractTimeout(true), hwExtractTimeoutHDR+softwareToneMapProbeTimeout+cpuExtractTimeoutHDR+3*time.Second; got != want {
		t.Fatalf("remoteExtractTimeout(true) = %s, want %s", got, want)
	}
}

func resolverWithFilters(t *testing.T, filters ...string) *softwareToneMapFilterResolver {
	t.Helper()
	output := strings.Join(filters, "\n")
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
