package tonemap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
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

func TestVideoToolboxSmokeUsesHardwareFramesAndEightBitOutput(t *testing.T) {
	args := hardwareSmokeArgs("/tmp/probe.hevc", BackendVideoToolbox, "", SourcePQ)
	joined := strings.Join(args, " ")
	for _, token := range []string{
		"-hwaccel videotoolbox",
		"-hwaccel_output_format videotoolbox_vld",
		SourceParameters(SourcePQ),
		"scale_vt=w=iw:h=ih:color_matrix=bt709:color_primaries=bt709:color_transfer=bt709",
		"hwdownload,format=p010le,format=nv12",
		"sidedata=mode=delete:type=DOVI_RPU_BUFFER",
		"-c:v h264_videotoolbox",
	} {
		if !strings.Contains(joined, token) {
			t.Fatalf("VideoToolbox smoke args missing %q: %s", token, joined)
		}
	}
}

func TestVideoToolboxSmokeDeclaresEverySourceSignal(t *testing.T) {
	for _, kind := range AllSourceKinds() {
		t.Run(string(kind), func(t *testing.T) {
			filter := hardwareSmokeFilter(BackendVideoToolbox, kind, decodeProbeFixtureBitDepth)
			if !strings.HasPrefix(filter, SourceParameters(kind)+",") {
				t.Fatalf("hardwareSmokeFilter() = %q, want source declaration %q first", filter, SourceParameters(kind))
			}
		})
	}
}

func TestVideoToolboxListingGateRequiresCompletePipeline(t *testing.T) {
	filters := []byte("scale_vt hwdownload sidedata")
	encoders := []byte("h264_videotoolbox")
	if !hardwareProbeAvailable(BackendVideoToolbox, filters, encoders) {
		t.Fatal("complete VideoToolbox pipeline was not accepted")
	}
	if hardwareProbeAvailable(BackendVideoToolbox, []byte("scale_vt hwdownload"), encoders) {
		t.Fatal("VideoToolbox pipeline without metadata removal was accepted")
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
		{name: "VideoToolbox", backend: BackendVideoToolbox, count: 12},
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
	if got, want := ProbeEndpointTimeout(BackendQSV, "/dev/dri/renderD128"), 106*time.Second; got != want {
		t.Fatalf("ProbeEndpointTimeout() = %s, want %s", got, want)
	}
	if got, want := ProbeEndpointTimeout("auto", "/dev/dri/renderD128,/dev/dri/renderD129"), 131*time.Second; got != want {
		t.Fatalf("ProbeEndpointTimeout(auto) = %s, want %s", got, want)
	}
	if got, want := ProbeRequestTimeout(BackendQSV, "/dev/dri/renderD128"), 111*time.Second; got != want {
		t.Fatalf("ProbeRequestTimeout() = %s, want %s", got, want)
	}
	// The slack has to outlast a full hardware detection walk plus the
	// transformation registry probe, or a node answers 503 while its own
	// detection is still running.
	if probeEndpointSlack < 30*time.Second+3*3*time.Second {
		t.Fatalf("probeEndpointSlack = %s, too small for detection and registry probes", probeEndpointSlack)
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

func TestProbePartialHardwareCapabilitiesExpire(t *testing.T) {
	resetProbeCache(t)
	now := time.Unix(100, 0)
	hardwareReady := false
	calls := 0
	runner := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls++
		if len(args) > 0 && args[len(args)-1] == "-filters" {
			return []byte(" .S. zscale V->V\n .S. tonemap V->V\n .S. scale_vt V->V\n .S. hwdownload V->V\n .S. sidedata V->V\n"), nil
		}
		if len(args) > 0 && args[len(args)-1] == "-encoders" {
			return []byte("libx264 h264_videotoolbox"), nil
		}
		if strings.Contains(strings.Join(args, " "), "-hwaccel videotoolbox") && !hardwareReady {
			return nil, errors.New("VideoToolbox session temporarily unavailable")
		}
		return nil, nil
	}

	got, err := probeCached(t.Context(), "/ffmpeg-partial", BackendVideoToolbox, "", runner, func() time.Time { return now })
	if err != nil {
		t.Fatalf("partial probe error = %v", err)
	}
	if len(got) != 1 || !got.Supports(ModeSoftware, SourcePQ) || got.Supports(ModeHardware, SourcePQ) {
		t.Fatalf("partial probe = %#v, want software only", got)
	}
	firstCalls := calls
	hardwareReady = true
	got, err = probeCached(t.Context(), "/ffmpeg-partial", BackendVideoToolbox, "", runner, func() time.Time { return now })
	if err != nil {
		t.Fatalf("cached partial probe error = %v", err)
	}
	if calls != firstCalls || got.Supports(ModeHardware, SourcePQ) {
		t.Fatalf("partial probe was not cached until expiry: calls = %d, capabilities = %#v", calls, got)
	}

	now = now.Add(probeNegativeTTL + time.Second)
	got, err = probeCached(t.Context(), "/ffmpeg-partial", BackendVideoToolbox, "", runner, func() time.Time { return now })
	if err != nil {
		t.Fatalf("retried partial probe error = %v", err)
	}
	if calls == firstCalls || !got.Supports(ModeHardware, SourcePQ) {
		t.Fatalf("expired partial probe was not retried: calls = %d, capabilities = %#v", calls, got)
	}
}

func TestProbeIncompleteSourceKindCapabilitiesExpire(t *testing.T) {
	resetProbeCache(t)
	now := time.Unix(100, 0)
	allKindsReady := false
	calls := 0
	runner := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls++
		if len(args) > 0 && args[len(args)-1] == "-filters" {
			return []byte(" .S. zscale V->V\n .S. tonemap V->V\n .S. scale_vt V->V\n .S. hwdownload V->V\n .S. sidedata V->V\n"), nil
		}
		if len(args) > 0 && args[len(args)-1] == "-encoders" {
			return []byte("libx264 h264_videotoolbox"), nil
		}
		if !allKindsReady && strings.Contains(strings.Join(args, " "), SourceParameters(SourceHLGBT709)) {
			return nil, errors.New("executor session temporarily unavailable")
		}
		return nil, nil
	}

	got, err := probeCached(t.Context(), "/ffmpeg-subset", BackendVideoToolbox, "", runner, func() time.Time { return now })
	if err != nil {
		t.Fatalf("subset probe error = %v", err)
	}
	if len(got) != 2 || !got.Supports(ModeSoftware, SourcePQ) || !got.Supports(ModeHardware, SourcePQ) ||
		got.Supports(ModeSoftware, SourceHLGBT709) || got.Supports(ModeHardware, SourceHLGBT709) {
		t.Fatalf("subset probe = %#v, want both executors without HLG BT.709", got)
	}
	firstCalls := calls
	allKindsReady = true
	got, err = probeCached(t.Context(), "/ffmpeg-subset", BackendVideoToolbox, "", runner, func() time.Time { return now })
	if err != nil {
		t.Fatalf("cached subset probe error = %v", err)
	}
	if calls != firstCalls || got.Supports(ModeSoftware, SourceHLGBT709) || got.Supports(ModeHardware, SourceHLGBT709) {
		t.Fatalf("subset probe was not cached until expiry: calls = %d, capabilities = %#v", calls, got)
	}

	now = now.Add(probeNegativeTTL + time.Second)
	got, err = probeCached(t.Context(), "/ffmpeg-subset", BackendVideoToolbox, "", runner, func() time.Time { return now })
	if err != nil {
		t.Fatalf("retried subset probe error = %v", err)
	}
	if calls == firstCalls || !got.Supports(ModeSoftware, SourceHLGBT709) || !got.Supports(ModeHardware, SourceHLGBT709) {
		t.Fatalf("expired subset probe was not retried: calls = %d, capabilities = %#v", calls, got)
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

// resetProbeCache clears shared probe state between tests. It delegates to the
// exported invalidation so tests exercise the same seam the operator-facing
// re-probe action uses.
func resetProbeCache(t *testing.T) {
	t.Helper()
	InvalidateProbeCache()
}

// A tone-map probe outlives its caller by design, so a component that released
// its own claim on the GPU when its call returned can leave smoke encodes
// running with nothing accounting for them. The count is what lets the transcode
// node's re-probe gate see that.
func TestProbesInFlightCountsADetachedProbe(t *testing.T) {
	awaitNoProbesInFlight(t)

	started := make(chan struct{})
	release := make(chan struct{})
	// The probe runs several commands; only the first needs to announce itself.
	var announce, released sync.Once
	t.Cleanup(func() { released.Do(func() { close(release) }) })

	// The probe has to be running before the caller gives up, or the flight
	// finishes on the canceled context and there is nothing detached to count.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := probeCached(ctx, "ffmpeg", BackendQSV, "/dev/dri/renderD128",
			func(context.Context, string, ...string) ([]byte, error) {
				announce.Do(func() { close(started) })
				<-release
				return nil, errors.New("probe abandoned")
			}, time.Now)
		done <- err
	}()

	<-started
	cancel()
	if err := <-done; err == nil {
		t.Fatal("an abandoned probe reported success")
	}

	// Checked the instant the caller returned, which is when its own claim on
	// the encoder goes away while the smoke encode keeps running.
	if got := ProbesInFlight(); got < 1 {
		t.Fatalf("ProbesInFlight() = %d the moment the caller returned, want at least 1", got)
	}
	released.Do(func() { close(release) })
	awaitNoProbesInFlight(t)
}

// awaitNoProbesInFlight waits for every claim on the encoder to be released,
// including ones detached from a caller that has already returned. Waiting on
// the counter rather than on a delay keeps this independent of machine load.
func awaitNoProbesInFlight(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got := ProbesInFlight(); got == 0 {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("ProbesInFlight() = %d, want every detached probe released", got)
		}
		runtime.Gosched()
	}
}

// The advertised budget every caller clamps to assumes a largest device set. If
// the worker probed past it, it would advertise a budget those callers then cut
// below what it actually needs, and cold capability requests would be canceled
// short forever. The probe set is capped so the two ends agree.
func TestProbeDevicesCapsTheConfiguredSet(t *testing.T) {
	configured := make([]string, 0, MaxProbedDevices+4)
	for i := range MaxProbedDevices + 4 {
		configured = append(configured, defaultDRIRenderDevice+strconv.Itoa(i))
	}

	got := probeDevices(strings.Join(configured, ","), BackendQSV)
	if len(got) != MaxProbedDevices {
		t.Fatalf("probed %d devices, want the %d cap", len(got), MaxProbedDevices)
	}
	if !slices.Equal(got, configured[:MaxProbedDevices]) {
		t.Fatalf("probed %v, want the first %d configured", got, MaxProbedDevices)
	}

	// The budget a node advertises for that capped set is therefore never above
	// what its callers allow — which is the property the cap exists for.
	if advertised, ceiling := ProbeRequestTimeout(BackendQSV, strings.Join(configured, ",")),
		MaxProbeRequestTimeout(); advertised > ceiling {
		t.Fatalf("advertised %v exceeds the %v callers allow", advertised, ceiling)
	}

	// A set inside the cap is untouched.
	small := configured[:3]
	if got := probeDevices(strings.Join(small, ","), BackendQSV); !slices.Equal(got, small) {
		t.Fatalf("probed %v, want the configured %v unchanged", got, small)
	}
}
