package tonemap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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

// TestDecodeProbeFixtureCoversPascalNVDECMinimum prevents the embedded decoder
// sample from regressing below the minimum frame size accepted by Pascal NVDEC.
func TestDecodeProbeFixtureCoversPascalNVDECMinimum(t *testing.T) {
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	fixturePath, cleanup, err := writeDecodeProbeFixture()
	if err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	defer cleanup()

	output, err := exec.Command(
		ffprobePath,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height,pix_fmt",
		"-of", "json",
		fixturePath,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("probe fixture: %v: %s", err, output)
	}
	var result struct {
		Streams []struct {
			Width  int    `json:"width"`
			Height int    `json:"height"`
			PixFmt string `json:"pix_fmt"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode fixture metadata: %v: %s", err, output)
	}
	if len(result.Streams) != 1 {
		t.Fatalf("fixture streams = %d, want 1", len(result.Streams))
	}
	const pascalNVDECMinimumDimension = 144
	stream := result.Streams[0]
	if stream.Width < pascalNVDECMinimumDimension || stream.Height < pascalNVDECMinimumDimension {
		t.Fatalf("fixture dimensions = %dx%d, must be at least %dx%d for Pascal NVDEC", stream.Width, stream.Height, pascalNVDECMinimumDimension, pascalNVDECMinimumDimension)
	}
	if stream.PixFmt != "yuv420p10le" {
		t.Fatalf("fixture pixel format = %q, want yuv420p10le", stream.PixFmt)
	}
}

// TestDecodeProbeFixtureIsMain10 pins the fixture's HEVC profile by reading
// the SPS profile_tier_level directly, so it holds without ffprobe. Intel
// VAAPI/QSV decoders accept general_profile_idc 2 (Main 10) and refuse 4
// (Rext); a Rext fixture makes every hardware tone-map smoke fail while the
// software smoke, which decodes Rext, still passes.
func TestDecodeProbeFixtureIsMain10(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString(decodeProbeFixtureBase64)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	const (
		nalSPS         = 33
		hevcMain10IDC  = 2
		nalHeaderBytes = 2
		spsPrefixBytes = 1 // sps_video_parameter_set_id, max_sub_layers_minus1, temporal_id_nesting
	)
	for i := 0; i+3 < len(data); i++ {
		if data[i] != 0 || data[i+1] != 0 || data[i+2] != 1 {
			continue
		}
		header := i + 3
		if (data[header]>>1)&0x3f != nalSPS {
			continue
		}
		ptl := header + nalHeaderBytes + spsPrefixBytes
		if ptl >= len(data) {
			t.Fatal("fixture SPS is truncated")
		}
		if got := data[ptl] & 0x1f; got != hevcMain10IDC {
			t.Fatalf("fixture general_profile_idc = %d, want %d (Main 10)", got, hevcMain10IDC)
		}
		return
	}
	t.Fatal("fixture has no SPS NAL unit")
}

// TestHardwareSmokeFilterNVENCPreservesSourceBitDepth verifies that the CUDA
// fallback graph downloads SDR base layers using their actual source bit depth.
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
		name     string
		backend  string
		device   string
		expected time.Duration
	}{
		{name: "software", backend: BackendSoftware, expected: 36 * time.Second},
		{name: "one QSV device", backend: BackendQSV, device: "/dev/dri/renderD128", expected: 61 * time.Second},
		{name: "two VAAPI devices", backend: BackendVAAPI, device: "/dev/dri/renderD128,/dev/dri/renderD129", expected: 86 * time.Second},
		{name: "one NVENC device", backend: BackendNVENC, device: "0", expected: 186 * time.Second},
		{name: "VideoToolbox", backend: BackendVideoToolbox, expected: 61 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ProbeTotalTimeout(tt.backend, tt.device); got != tt.expected {
				t.Fatalf("ProbeTotalTimeout() = %s, want %s", got, tt.expected)
			}
		})
	}
}

// TestProbeEndpointTimeoutCoversDetectionAndProbeBudgets verifies that endpoint
// and transport budgets include their complete backend-specific probe matrix.
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
	if got, want := ProbeEndpointTimeout(BackendNVENC, "0"), 231*time.Second; got != want {
		t.Fatalf("ProbeEndpointTimeout(NVENC) = %s, want %s", got, want)
	}
	if got, want := ProbeRequestTimeout(BackendNVENC, "0"), 236*time.Second; got != want {
		t.Fatalf("ProbeRequestTimeout(NVENC) = %s, want %s", got, want)
	}
	// The slack has to outlast a full hardware detection walk plus the
	// transformation registry probe, or a node answers 503 while its own
	// detection is still running.
	if probeEndpointSlack < 30*time.Second+3*3*time.Second {
		t.Fatalf("probeEndpointSlack = %s, too small for detection and registry probes", probeEndpointSlack)
	}
}

// TestHardwareProbeCommandTimeoutExtendsOnlyNVENC prevents cold-start headroom
// from silently widening the existing limits for other probe backends.
func TestHardwareProbeCommandTimeoutExtendsOnlyNVENC(t *testing.T) {
	if got := hardwareProbeCommandTimeout(BackendNVENC); got != nvencProbeCommandTimeout {
		t.Fatalf("NVENC command timeout = %s, want %s", got, nvencProbeCommandTimeout)
	}
	for _, backend := range []string{BackendQSV, BackendVAAPI, BackendVideoToolbox, BackendSoftware, ""} {
		if got := hardwareProbeCommandTimeout(backend); got != probeCommandTimeout {
			t.Fatalf("%q command timeout = %s, want %s", backend, got, probeCommandTimeout)
		}
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
	for _, backend := range []string{BackendQSV, BackendVAAPI, BackendNVENC, BackendVideoToolbox} {
		if advertised, ceiling := ProbeRequestTimeout(backend, strings.Join(configured, ",")),
			MaxProbeRequestTimeout(); advertised > ceiling {
			t.Fatalf("%s advertised %v exceeds the %v callers allow", backend, advertised, ceiling)
		}
	}

	// A set inside the cap is untouched.
	small := configured[:3]
	if got := probeDevices(strings.Join(small, ","), BackendQSV); !slices.Equal(got, small) {
		t.Fatalf("probed %v, want the configured %v unchanged", got, small)
	}
}
