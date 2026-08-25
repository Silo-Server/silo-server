package tonemap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

// TestProbeSuccessfulCapabilitiesExpire verifies a positive-but-potentially-partial
// discovery is periodically refreshed rather than pinned until process restart.
func TestProbeSuccessfulCapabilitiesExpire(t *testing.T) {
	resetProbeCache(t)
	now := time.Unix(100, 0)
	var calls atomic.Int32
	runner := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls.Add(1)
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
	firstCalls := calls.Load()
	now = now.Add(probePositiveTTL - time.Second)
	got, err = probeCached(context.Background(), "/ffmpeg-success", BackendSoftware, "", runner, func() time.Time { return now })
	if err != nil {
		t.Fatalf("cached successful probe error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("cached successful probe = %#v", got)
	}
	if calls.Load() != firstCalls {
		t.Fatalf("unexpired successful probe reran: calls = %d, want %d", calls.Load(), firstCalls)
	}
	now = now.Add(2 * time.Second)
	got, err = probeCached(context.Background(), "/ffmpeg-success", BackendSoftware, "", runner, func() time.Time { return now })
	if err != nil || len(got) != 1 {
		t.Fatalf("refreshed successful probe = %#v, error = %v", got, err)
	}
	refreshExpiry := now.Add(probePositiveTTL)
	refreshDeadline := time.After(time.Second)
	for {
		probeCache.Lock()
		refreshed := probeCache.entries[probeCacheKey("/ffmpeg-success", BackendSoftware, "")].expiresAt.Equal(refreshExpiry)
		probeCache.Unlock()
		if refreshed {
			break
		}
		select {
		case <-refreshDeadline:
			t.Fatal("expired successful probe was not refreshed in the background")
		default:
			runtime.Gosched()
		}
	}
}

func TestProbeExpiredPositiveReturnsWhileRefreshIsRunning(t *testing.T) {
	resetProbeCache(t)
	var nowUnix atomic.Int64
	nowUnix.Store(100)
	now := func() time.Time { return time.Unix(nowUnix.Load(), 0) }
	var blockRefresh atomic.Bool
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	var startOnce sync.Once
	runner := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if blockRefresh.Load() {
			startOnce.Do(func() { close(refreshStarted) })
			<-releaseRefresh
		}
		if len(args) > 0 && args[len(args)-1] == "-filters" {
			return []byte(" .S. zscale V->V\n .S. tonemapx V->V\n .S. sidedata V->V\n"), nil
		}
		if len(args) > 0 && args[len(args)-1] == "-encoders" {
			return []byte("libx264"), nil
		}
		return nil, nil
	}
	seed, err := probeCached(context.Background(), "/ffmpeg-stale", BackendSoftware, "", runner, now)
	if err != nil || len(seed) != 1 {
		t.Fatalf("seed probe = %#v, error = %v", seed, err)
	}

	nowUnix.Add(int64((probePositiveTTL + time.Second) / time.Second))
	blockRefresh.Store(true)
	stale, err := probeCached(context.Background(), "/ffmpeg-stale", BackendSoftware, "", runner, now)
	if err != nil || len(stale) != 1 {
		t.Fatalf("stale probe = %#v, error = %v", stale, err)
	}
	select {
	case <-refreshStarted:
	case <-time.After(time.Second):
		t.Fatal("expired positive did not start a background refresh")
	}
	close(releaseRefresh)
	wantExpiry := now().Add(probePositiveTTL)
	refreshDeadline := time.After(time.Second)
	for {
		probeCache.Lock()
		refreshed := probeCache.entries[probeCacheKey("/ffmpeg-stale", BackendSoftware, "")].expiresAt.Equal(wantExpiry)
		probeCache.Unlock()
		if refreshed {
			break
		}
		select {
		case <-refreshDeadline:
			t.Fatal("background refresh did not replace the stale positive")
		default:
			runtime.Gosched()
		}
	}
}

func TestProbeExpiredPositiveBacksOffAfterRefreshDeadline(t *testing.T) {
	resetProbeCache(t)
	var nowUnix atomic.Int64
	nowUnix.Store(100)
	now := func() time.Time { return time.Unix(nowUnix.Load(), 0) }
	var failRefresh atomic.Bool
	var refreshCalls atomic.Int32
	runner := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if failRefresh.Load() {
			refreshCalls.Add(1)
			return nil, context.DeadlineExceeded
		}
		if len(args) > 0 && args[len(args)-1] == "-filters" {
			return []byte(" .S. zscale V->V\n .S. tonemapx V->V\n .S. sidedata V->V\n"), nil
		}
		if len(args) > 0 && args[len(args)-1] == "-encoders" {
			return []byte("libx264"), nil
		}
		return nil, nil
	}
	seed, err := probeCached(context.Background(), "/ffmpeg-refresh-backoff", BackendSoftware, "", runner, now)
	if err != nil || len(seed) != 1 {
		t.Fatalf("seed probe = %#v, error = %v", seed, err)
	}

	nowUnix.Add(int64((probePositiveTTL + time.Second) / time.Second))
	failRefresh.Store(true)
	stale, err := probeCached(context.Background(), "/ffmpeg-refresh-backoff", BackendSoftware, "", runner, now)
	if err != nil || len(stale) != 1 {
		t.Fatalf("stale probe = %#v, error = %v", stale, err)
	}
	wantBackoff := now().Add(probeNegativeTTL)
	deadline := time.After(time.Second)
	for {
		probeCache.Lock()
		backedOff := probeCache.entries[probeCacheKey("/ffmpeg-refresh-backoff", BackendSoftware, "")].expiresAt.Equal(wantBackoff)
		probeCache.Unlock()
		if backedOff {
			break
		}
		select {
		case <-deadline:
			t.Fatal("failed background refresh did not advance its retry deadline")
		default:
			runtime.Gosched()
		}
	}
	failedCalls := refreshCalls.Load()
	stale, err = probeCached(context.Background(), "/ffmpeg-refresh-backoff", BackendSoftware, "", runner, now)
	if err != nil || len(stale) != 1 {
		t.Fatalf("backed-off stale probe = %#v, error = %v", stale, err)
	}
	if refreshCalls.Load() != failedCalls {
		t.Fatal("stale positive retried before the refresh backoff expired")
	}

	nowUnix.Add(int64((probeNegativeTTL + time.Second) / time.Second))
	_, _ = probeCached(context.Background(), "/ffmpeg-refresh-backoff", BackendSoftware, "", runner, now)
	deadline = time.After(time.Second)
	for refreshCalls.Load() == failedCalls {
		select {
		case <-deadline:
			t.Fatal("stale positive did not retry after the refresh backoff expired")
		default:
			runtime.Gosched()
		}
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
