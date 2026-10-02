package playback

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHardwareSmokeEncodeArgs(t *testing.T) {
	tests := []struct {
		name     string
		backend  string
		device   string
		contains []string
	}{
		{
			name:    "qsv",
			backend: transcodeHWQSV,
			device:  "/dev/dri/renderD129",
			contains: []string{
				"vaapi=va:/dev/dri/renderD129,driver=iHD,kernel_driver=i915,vendor_id=0x8086", "qsv=qs@va",
				"format=nv12,hwupload=extra_hw_frames=64", "h264_qsv",
			},
		},
		{
			name:    "vaapi",
			backend: transcodeHWVAAPI,
			device:  "/dev/dri/renderD130",
			contains: []string{
				"vaapi=hw:/dev/dri/renderD130", "format=nv12,hwupload", "h264_vaapi",
			},
		},
		{
			name:     "nvenc",
			backend:  transcodeHWNVENC,
			device:   "2",
			contains: []string{"cuda=cu:2", "hwupload_cuda", "testsrc2=size=640x360:rate=1", "h264_nvenc"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := hardwareSmokeEncodeArgs(test.backend, test.device)
			for _, want := range test.contains {
				if !slices.Contains(args, want) {
					t.Fatalf("hardwareSmokeEncodeArgs(%q, %q) = %v, missing %q", test.backend, test.device, args, want)
				}
			}
			framesIndex := slices.Index(args, "-frames:v")
			if framesIndex < 0 || framesIndex+1 >= len(args) || args[framesIndex+1] != "1" {
				t.Fatalf("hardwareSmokeEncodeArgs(%q, %q) = %v, want one output frame", test.backend, test.device, args)
			}
			if got := args[len(args)-3:]; !slices.Equal(got, []string{"-f", "null", "-"}) {
				t.Fatalf("hardwareSmokeEncodeArgs(%q, %q) tail = %v, want null sink", test.backend, test.device, got)
			}
		})
	}
}

func TestWarmHardwareEncoderWithRunner(t *testing.T) {
	var calls []string
	err := warmHardwareEncoderWithRunner(context.Background(), time.Second, "/test/ffmpeg", transcodeHWQSV,
		[]string{"/dev/dri/renderD128", "/dev/dri/renderD129"},
		func(ctx context.Context, path string, args ...string) ([]byte, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("runner context has no deadline")
			}
			if path != "/test/ffmpeg" {
				t.Fatalf("runner path = %q", path)
			}
			calls = append(calls, strings.Join(args, " "))
			return nil, nil
		})
	if err != nil {
		t.Fatalf("warmHardwareEncoderWithRunner() error = %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("runner calls = %d, want 2", len(calls))
	}
	for index, device := range []string{"/dev/dri/renderD128", "/dev/dri/renderD129"} {
		if !strings.Contains(calls[index], device) {
			t.Fatalf("runner call %d = %q, missing device %q", index, calls[index], device)
		}
	}
}

func TestWarmHardwareEncoderWithRunnerSkipsDisabledAndUnknownBackends(t *testing.T) {
	calls := 0
	runner := func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("unexpected call")
	}
	for _, backend := range []string{"", HWAccelNone, "unknown"} {
		if err := warmHardwareEncoderWithRunner(context.Background(), time.Second, "ffmpeg", backend, nil, runner); err != nil {
			t.Fatalf("backend %q error = %v", backend, err)
		}
	}
	if calls != 0 {
		t.Fatalf("runner calls = %d, want 0", calls)
	}
}

func TestWarmHardwareEncoderWithRunnerBoundsEachAttempt(t *testing.T) {
	timeout := 20 * time.Millisecond
	started := time.Now()
	err := warmHardwareEncoderWithRunner(context.Background(), timeout, "ffmpeg", transcodeHWNVENC, nil,
		func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
			<-ctx.Done()
			return []byte("timed out"), ctx.Err()
		})
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed < timeout || elapsed > timeout+time.Second {
		t.Fatalf("elapsed = %v, want bounded near %v", elapsed, timeout)
	}
}

func TestWarmHardwareEncoderCachedSharesSuccessfulWarmupPastCallerCancellation(t *testing.T) {
	state := newHardwareEncoderWarmupState()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	runner := func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	resolve := func(context.Context, string, string, string) string { return transcodeHWNVENC }

	callerCtx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- warmHardwareEncoderCached(callerCtx, "/test/ffmpeg-cancel", "auto", "", state, resolve, runner)
	}()
	<-started
	cancel()
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("first warmup error = %v", err)
	}
	if err := warmHardwareEncoderCached(context.Background(), "/test/ffmpeg-cancel", "auto", "", state, resolve, runner); err != nil {
		t.Fatalf("cached warmup error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("runner calls = %d, want one shared cached warmup", got)
	}
}

func TestHardwareEncoderWarmupDevicesHonorsConfiguredSet(t *testing.T) {
	if got := hardwareEncoderWarmupDevices(transcodeHWQSV, "/dev/dri/renderD129,/dev/dri/renderD130"); !slices.Equal(got, []string{"/dev/dri/renderD129", "/dev/dri/renderD130"}) {
		t.Fatalf("QSV devices = %v", got)
	}
	if got := hardwareEncoderWarmupDevices(transcodeHWNVENC, "2,3"); !slices.Equal(got, []string{"2"}) {
		t.Fatalf("NVENC devices = %v, want configured primary device", got)
	}
}
