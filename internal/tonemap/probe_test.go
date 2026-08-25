package tonemap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHardwareSmokeFilterNVENCPreservesSourceBitDepth(t *testing.T) {
	for _, test := range []struct {
		name     string
		bitDepth int
		want     string
		reject   string
	}{
		{name: "8-bit", bitDepth: 8, want: "hwdownload,format=nv12", reject: "hwdownload,format=p010le"},
		{name: "10-bit", bitDepth: 10, want: "hwdownload,format=p010le", reject: "hwdownload,format=nv12"},
	} {
		t.Run(test.name, func(t *testing.T) {
			filter := hardwareSmokeFilter(BackendNVENC, SourceSDRBT2020, test.bitDepth)
			if !strings.Contains(filter, test.want) || strings.Contains(filter, test.reject) {
				t.Fatalf("hardwareSmokeFilter() = %q, want %q without %q", filter, test.want, test.reject)
			}
		})
	}
}

// TestProbeTotalTimeoutCoversBoundedCommandMatrix verifies the deadline covers every possible probe command.
func TestProbeTotalTimeoutCoversBoundedCommandMatrix(t *testing.T) {
	tests := []struct {
		name    string
		backend string
		device  string
		count   int
	}{
		{name: "software", backend: BackendSoftware, count: 7},
		{name: "one hardware device", backend: BackendQSV, device: "/dev/dri/renderD128", count: 12},
		{name: "two hardware devices", backend: BackendVAAPI, device: "/dev/dri/renderD128,/dev/dri/renderD129", count: 17},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := time.Duration(tt.count)*probeCommandTimeout + probeTimeoutSlack
			if got := ProbeTotalTimeout(tt.backend, tt.device); got != want {
				t.Fatalf("ProbeTotalTimeout() = %s, want %s", got, want)
			}
		})
	}
}

func TestProbeEndpointTimeoutCoversDetectionAndProbeBudgets(t *testing.T) {
	if got, want := ProbeEndpointTimeout(BackendQSV, "/dev/dri/renderD128"), 81*time.Second; got != want {
		t.Fatalf("ProbeEndpointTimeout() = %s, want %s", got, want)
	}
	if got, want := ProbeEndpointTimeout("auto", "/dev/dri/renderD128,/dev/dri/renderD129"), 106*time.Second; got != want {
		t.Fatalf("ProbeEndpointTimeout(auto) = %s, want %s", got, want)
	}
	if got, want := ProbeRequestTimeout(BackendQSV, "/dev/dri/renderD128"), 86*time.Second; got != want {
		t.Fatalf("ProbeRequestTimeout() = %s, want %s", got, want)
	}
}

// TestProbeEmptyCapabilitiesExpire verifies failed discovery is retried after a short interval.
func TestProbeEmptyCapabilitiesExpire(t *testing.T) {
	resetProbeCache(t)
	now := time.Unix(100, 0)
	calls := 0
	runner := func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("temporarily unavailable")
	}
	for attempt := 0; attempt < 2; attempt++ {
		got, err := probeCached(context.Background(), "/ffmpeg-empty", BackendSoftware, "", runner, func() time.Time { return now })
		if err != nil {
			t.Fatalf("empty probe error = %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("empty probe = %#v", got)
		}
	}
	if calls != 2 {
		t.Fatalf("listing calls = %d, want two from one cached empty probe", calls)
	}
	now = now.Add(probeNegativeTTL + time.Second)
	_, _ = probeCached(context.Background(), "/ffmpeg-empty", BackendSoftware, "", runner, func() time.Time { return now })
	if calls != 4 {
		t.Fatalf("listing calls = %d, want a fresh probe after expiry", calls)
	}
}

func TestProbeCommandDeadlineIsTransientAndNotCached(t *testing.T) {
	resetProbeCache(t)
	calls := 0
	runner := func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, context.DeadlineExceeded
	}

	for attempt := 0; attempt < 2; attempt++ {
		capabilities, err := probeCached(context.Background(), "/ffmpeg-timeout", BackendSoftware, "", runner, time.Now)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("probe error = %v, want context deadline", err)
		}
		if len(capabilities) != 0 {
			t.Fatalf("timed-out probe capabilities = %#v", capabilities)
		}
	}
	if calls != 4 {
		t.Fatalf("timed-out listing calls = %d, want two fresh listing commands per attempt", calls)
	}
}

// TestProbeSuccessfulCapabilitiesDoNotExpire verifies unchanged positive discovery remains cached.
func TestProbeSuccessfulCapabilitiesDoNotExpire(t *testing.T) {
	resetProbeCache(t)
	now := time.Unix(100, 0)
	calls := 0
	runner := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls++
		if len(args) > 0 && args[len(args)-1] == "-filters" {
			return []byte(" .S. zscale V->V\n .S. tonemapx V->V\n .S. sidedata V->V\n"), nil
		}
		if len(args) > 0 && args[len(args)-1] == "-encoders" {
			return []byte("libx264"), nil
		}
		return nil, nil
	}
	got, err := probeCached(context.Background(), "/ffmpeg-success", BackendSoftware, "", runner, func() time.Time { return now })
	if err != nil {
		t.Fatalf("successful probe error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("successful probe = %#v", got)
	}
	firstCalls := calls
	now = now.Add(24 * time.Hour)
	got, err = probeCached(context.Background(), "/ffmpeg-success", BackendSoftware, "", runner, func() time.Time { return now })
	if err != nil {
		t.Fatalf("cached successful probe error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("cached successful probe = %#v", got)
	}
	if calls != firstCalls {
		t.Fatalf("successful probe reran: calls = %d, want %d", calls, firstCalls)
	}
}

// TestProbeCacheInvalidatesWhenFFmpegBinaryChangesInPlace verifies that a
// positive result is rechecked after the configured executable is replaced.
func TestProbeCacheInvalidatesWhenFFmpegBinaryChangesInPlace(t *testing.T) {
	resetProbeCache(t)
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(ffmpegPath, []byte("first"), 0o755); err != nil {
		t.Fatalf("write first FFmpeg binary: %v", err)
	}
	calls := 0
	runner := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls++
		if len(args) > 0 && args[len(args)-1] == "-filters" {
			return []byte(" .S. zscale V->V\n .S. tonemapx V->V\n .S. sidedata V->V\n"), nil
		}
		if len(args) > 0 && args[len(args)-1] == "-encoders" {
			return []byte("libx264"), nil
		}
		return nil, nil
	}

	got, err := probeCached(context.Background(), ffmpegPath, BackendSoftware, "", runner, time.Now)
	if err != nil {
		t.Fatalf("first probe error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("first probe = %#v", got)
	}
	firstCalls := calls
	got, err = probeCached(context.Background(), ffmpegPath, BackendSoftware, "", runner, time.Now)
	if err != nil {
		t.Fatalf("cached probe error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("cached probe = %#v", got)
	}
	if calls != firstCalls {
		t.Fatalf("unchanged binary reran probe: calls = %d, want %d", calls, firstCalls)
	}

	if err := os.WriteFile(ffmpegPath, []byte("replacement-binary"), 0o755); err != nil {
		t.Fatalf("replace FFmpeg binary: %v", err)
	}
	got, err = probeCached(context.Background(), ffmpegPath, BackendSoftware, "", runner, time.Now)
	if err != nil {
		t.Fatalf("probe after replacement error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("probe after replacement = %#v", got)
	}
	if calls == firstCalls {
		t.Fatal("replaced FFmpeg binary reused stale positive capabilities")
	}
}

// TestProbeCallerCancellationDoesNotCancelSharedProbe verifies one request cannot abort shared discovery.
func TestProbeCallerCancellationDoesNotCancelSharedProbe(t *testing.T) {
	resetProbeCache(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var starts atomic.Int32
	var sharedCancelled atomic.Bool
	runner := func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		if starts.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return nil, errors.New("unavailable")
		case <-ctx.Done():
			sharedCancelled.Store(true)
			return nil, ctx.Err()
		}
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	type probeResult struct {
		capabilities Capabilities
		err          error
	}
	first := make(chan probeResult, 1)
	go func() {
		capabilities, err := probeCached(firstCtx, "/ffmpeg-shared", BackendSoftware, "", runner, time.Now)
		first <- probeResult{capabilities: capabilities, err: err}
	}()
	<-started
	second := make(chan probeResult, 1)
	go func() {
		capabilities, err := probeCached(context.Background(), "/ffmpeg-shared", BackendSoftware, "", runner, time.Now)
		second <- probeResult{capabilities: capabilities, err: err}
	}()
	cancelFirst()
	select {
	case result := <-first:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("canceled caller error = %v, want context.Canceled", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled caller did not stop waiting")
	}
	close(release)
	select {
	case result := <-second:
		if result.err != nil {
			t.Fatalf("remaining caller error = %v", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("remaining caller did not receive the shared probe result")
	}
	if sharedCancelled.Load() {
		t.Fatal("caller cancellation propagated into the shared probe context")
	}
}

// resetProbeCache clears shared probe state between tests.
func resetProbeCache(t *testing.T) {
	t.Helper()
	probeCache.Lock()
	probeCache.entries = make(map[string]probeCacheEntry)
	probeCache.Unlock()
}
