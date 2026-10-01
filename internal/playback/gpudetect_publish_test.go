package playback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A detection walk that ran out of budget marks backends it never reached
// Verified=false, which is byte-identical to a real hardware failure. A node
// that hashed and published that report would tell the API its GPU regressed,
// and the API would persist a capability_drift note, latch it until a clean
// report arrives, and route the node to software in the meantime — all for
// hardware that is fine. So the incompleteness has to reach the publisher.
func TestDetectHWAccelReportsAnIncompleteWalk(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	ffmpeg := writeFakeFFmpeg(t, fullyCapableProbe())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	info, err := DetectHWAccelWithFFmpegContextResult(ctx, hwAccelAuto, ffmpeg.path, "")
	if !errors.Is(err, ErrHardwareDetectionIncomplete) {
		t.Fatalf("error = %v, want ErrHardwareDetectionIncomplete", err)
	}
	// The report still comes back for an operator-facing surface to show; it is
	// only publishing it as this host's capabilities that is refused.
	if info.Resolved != HWAccelNone {
		t.Fatalf("Resolved = %q, want an abandoned walk to resolve to software", info.Resolved)
	}
}

// The complement: a walk that reached every candidate backend publishes
// normally, or nothing would ever be inventoried.
func TestDetectHWAccelReportsACompleteWalk(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	ffmpeg := writeFakeFFmpeg(t, fullyCapableProbe())

	info, err := DetectHWAccelWithFFmpegContextResult(context.Background(), hwAccelAuto, ffmpeg.path, "")
	if err != nil {
		t.Fatalf("DetectHWAccelWithFFmpegContextResult: %v", err)
	}
	if info.Resolved != transcodeHWQSV {
		t.Fatalf("Resolved = %q, want qsv", info.Resolved)
	}
}

// Detection walks a backend's candidates in order and stops at the first that
// passes a smoke encode; execution with no configured playback.hw_device used to
// fall back to PickRenderDevice, which returns whatever sorts first under
// /dev/dri. On a mixed-vendor host those are different GPUs, so a report saying
// "qsv verified" was paired with a transcode initializing a card the probe had
// never touched.
func TestAcquireHWDeviceUsesTheVerifiedRenderDevice(t *testing.T) {
	env := setupHWAccelTest(t)
	// renderD128 sorts first and is AMD, so it is not a QSV candidate at all;
	// only renderD129 can pass the probe.
	env.addRenderDevice(t, "renderD128", "0x1002")
	env.addRenderDevice(t, "renderD129", "0x8086")
	ffmpeg := writeFakeFFmpeg(t, fullyCapableProbe())

	if got := ResolveHWAccelWithFFmpeg(hwAccelAuto, ffmpeg.path, ""); got != transcodeHWQSV {
		t.Fatalf("ResolveHWAccelWithFFmpeg() = %q, want qsv", got)
	}
	verified := VerifiedHWDevice(transcodeHWQSV)
	if want := env.devicePath("renderD129"); verified != want {
		t.Fatalf("VerifiedHWDevice(qsv) = %q, want %q", verified, want)
	}
	// The unverified device is the one auto-detection would otherwise pick.
	if fallback := PickRenderDevice(""); fallback == verified {
		t.Skip("test setup no longer distinguishes the verified device from the first render node")
	}

	device, release := AcquireHWDevice("", transcodeHWQSV)
	defer release()
	if device != verified {
		t.Fatalf("AcquireHWDevice() = %q, want the verified device %q", device, verified)
	}
	// Counting it is the other half: a default-configured node reported zero
	// sessions beside a busy engine because an unnamed device was never counted.
	if got := hwDeviceActiveCount(verified); got != 1 {
		t.Fatalf("active workloads on %s = %d, want 1", verified, got)
	}
	release()
	if got := hwDeviceActiveCount(verified); got != 0 {
		t.Fatalf("active workloads after release = %d, want 0", got)
	}

	// An operator re-probe discards the verdicts, so the device they blessed
	// goes with them: answering from the old generation would let execution
	// keep using a device the re-probe was asked to re-verify.
	InvalidateHWProbeCache()
	if got := VerifiedHWDevice(transcodeHWQSV); got != "" {
		t.Fatalf("VerifiedHWDevice(qsv) after invalidation = %q, want empty", got)
	}
}

// Invalidation has to supersede a probe already in flight, not merely clear the
// map in front of it. The operator-facing re-probe exists to force a cold
// re-verification; if a probe that started before the invalidation could hand
// its pre-invalidation verdict to the caller that invalidated, the action would
// republish exactly what it was asked to discard and report "nothing changed".
func TestInvalidateHWProbeCacheSupersedesAnInFlightProbe(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	ffmpeg := writeFakeFFmpeg(t, successfulVAAPIProbe())
	device := env.devicePath("renderD128")

	// The race is decided by channel receipts rather than a sleep: the first
	// flight parks inside the probe until the invalidation has landed, so this
	// cannot pass or fail on how loaded the machine is.
	started := make(chan struct{})
	blocked := make(chan struct{})
	var flights atomic.Int32
	hwProbeFlightStarted = func() {
		if flights.Add(1) == 1 {
			close(started)
			<-blocked
		}
	}
	t.Cleanup(func() { hwProbeFlightStarted = nil })

	var wg sync.WaitGroup
	wg.Go(func() {
		if ok, reason := ffmpegSupportsBackend(transcodeHWVAAPI, ffmpeg.path, device); !ok {
			t.Errorf("in-flight probe failed: %s", reason)
		}
	})

	<-started
	InvalidateHWProbeCache()
	close(blocked)

	if ok, reason := ffmpegSupportsBackend(transcodeHWVAAPI, ffmpeg.path, device); !ok {
		t.Fatalf("post-invalidation probe failed: %s", reason)
	}
	wg.Wait()

	// Two independent smoke encodes ran: the second call started its own probe
	// rather than joining the flight the invalidation superseded, which is the
	// whole difference between clearing the map and moving the key.
	if got := smokeEncodeCount(t, ffmpeg.logPath); got < 2 {
		t.Fatalf("smoke encodes = %d, want the post-invalidation probe to run its own", got)
	}
}

// smokeEncodeCount counts the synthetic single-frame encodes in a fake ffmpeg's
// command log. Every hardware probe ends in exactly one.
func smokeEncodeCount(t *testing.T, logPath string) int {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read ffmpeg probe log: %v", err)
	}
	return strings.Count(string(data), "testsrc2")
}

// devicePath is the full path of a render device added to this test's /dev/dri
// stand-in.
func (e *hwAccelTestEnv) devicePath(name string) string {
	return filepath.Join(e.driDir, name)
}

// NVENC takes the configured hw_device through to -hwaccel_device, so probing
// with an empty device lets a working GPU 0 verify the backend on behalf of a
// configured GPU 1 that is absent or broken — and the real transcode then fails.
func TestNVENCProbesTheConfiguredCUDADevice(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addNVIDIADevice(t, "nvidia0")
	probe := fullyCapableProbe()
	// Only the default CUDA device works; the configured one does not.
	probe.smokeDeviceFailures = []string{"1"}
	ffmpeg := writeFakeFFmpeg(t, probe)

	info, err := DetectHWAccelWithFFmpegContextResult(context.Background(), hwAccelAuto, ffmpeg.path, "1")
	if err != nil {
		t.Fatalf("DetectHWAccelWithFFmpegContextResult: %v", err)
	}
	for _, backend := range info.DetectedBackends {
		if backend.Backend != transcodeHWNVENC {
			continue
		}
		if backend.Verified {
			t.Fatalf("nvenc reported verified while the configured CUDA device fails: %+v", backend)
		}
		return
	}
	t.Fatalf("no nvenc entry in %+v", info.DetectedBackends)
}

// A CUDA index is not a filesystem path, so the accessibility filter that keeps
// a proxy from probing a render node it cannot open must not silently skip it.
func TestNVENCConfiguredDeviceIsProbedNotSkipped(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addNVIDIADevice(t, "nvidia0")
	ffmpeg := writeFakeFFmpeg(t, fullyCapableProbe())

	info, err := DetectHWAccelWithFFmpegContextResult(context.Background(), hwAccelAuto, ffmpeg.path, "0")
	if err != nil {
		t.Fatalf("DetectHWAccelWithFFmpegContextResult: %v", err)
	}
	for _, backend := range info.DetectedBackends {
		if backend.Backend != transcodeHWNVENC {
			continue
		}
		if backend.Skipped {
			t.Fatalf("nvenc was skipped for an unopenable CUDA index: %+v", backend)
		}
		if !backend.Verified {
			t.Fatalf("nvenc should verify against a working CUDA device: %+v", backend)
		}
		return
	}
	t.Fatalf("no nvenc entry in %+v", info.DetectedBackends)
}

// An explicitly configured backend short-circuits resolution, so the detection
// walk never runs and nothing is ever recorded as verified. Without a fallback
// the workload went uncounted and the node reported zero GPU sessions while it
// transcoded — the same reporting hole the auto path had, on the branch that
// never probes.
func TestAcquireHWDeviceCountsTheAutoDetectedDeviceWithoutAProbe(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")

	// No walk has run, so nothing is verified — exactly the state a host with
	// hw_accel=qsv and no hw_device is in.
	if got := VerifiedHWDevice(transcodeHWQSV); got != "" {
		t.Fatalf("VerifiedHWDevice = %q, want nothing verified for this test", got)
	}

	device, release := AcquireHWDevice("", transcodeHWQSV)
	defer release()

	want := env.devicePath("renderD128")
	if device != want {
		t.Fatalf("AcquireHWDevice() = %q, want the device execution will pick, %q", device, want)
	}
	if got := hwDeviceActiveCount(want); got != 1 {
		t.Fatalf("active workloads on %s = %d, want the transcode counted", want, got)
	}
	release()
	if got := hwDeviceActiveCount(want); got != 0 {
		t.Fatalf("active workloads after release = %d, want 0", got)
	}
}

// The identity listing used to be a sync.Once, so an nvidia-smi that was
// missing at first call was never asked again and a card swapped into the same
// slot kept answering to its predecessor's uuid — both of which feed drift
// detection and shared-GPU placement. A re-probe is exactly when either becomes
// true, so it drops the listing too.
func TestInvalidateHWProbeCacheRequeriesNVIDIAIdentities(t *testing.T) {
	setupHWAccelTest(t)

	queries := 0
	answer := ""
	previous := nvidiaSMIQuery
	nvidiaSMIQuery = func(context.Context) ([]byte, error) {
		queries++
		if answer == "" {
			return nil, errors.New("nvidia-smi not installed")
		}
		return []byte(answer), nil
	}
	t.Cleanup(func() {
		nvidiaSMIQuery = previous
		resetNVIDIAGPUUUIDs()
	})
	resetNVIDIAGPUUUIDs()

	if got := nvidiaGPUUUIDsByPCIAddress(); len(got) != 0 {
		t.Fatalf("identities = %v, want none while nvidia-smi is unavailable", got)
	}
	if nvidiaGPUUUIDsByPCIAddress(); queries != 1 {
		t.Fatalf("queries = %d, want the failure cached within a generation", queries)
	}

	// The toolkit is installed, or the card is replaced. Only a re-probe should
	// make the process notice.
	answer = "GPU-new, 00000000:03:00.0\n"
	if got := nvidiaGPUUUIDsByPCIAddress(); len(got) != 0 {
		t.Fatalf("identities = %v, want the cached answer until the caches are dropped", got)
	}

	InvalidateHWProbeCache()
	got := nvidiaGPUUUIDsByPCIAddress()
	if got["0000:03:00.0"] != "GPU-new" {
		t.Fatalf("identities = %v, want the re-probe to pick up the new uuid", got)
	}
}

// On a mixed NVIDIA/Intel host NVENC stays a candidate through hasNVIDIADevice
// even when the node is deliberately configured for QSV on a render node. Smoke
// encoding CUDA against /dev/dri/renderD128 fails for a reason that has nothing
// to do with the NVIDIA card, and a non-skipped failure latches a drift warning
// that cannot clear while the QSV policy stands.
func TestNVENCSkippedWhenTheConfiguredDeviceIsARenderPath(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	env.addNVIDIADevice(t, "nvidia0")
	ffmpeg := writeFakeFFmpeg(t, fullyCapableProbe())

	info, err := DetectHWAccelWithFFmpegContextResult(
		context.Background(), hwAccelAuto, ffmpeg.path, env.devicePath("renderD128"))
	if err != nil {
		t.Fatalf("DetectHWAccelWithFFmpegContextResult: %v", err)
	}
	for _, backend := range info.DetectedBackends {
		if backend.Backend != transcodeHWNVENC {
			continue
		}
		if !backend.Skipped {
			t.Fatalf("nvenc = %+v, want it skipped rather than failed for a render-path device", backend)
		}
		if backend.Verified {
			t.Fatalf("nvenc = %+v, want no verification claimed", backend)
		}
		return
	}
	t.Fatalf("no nvenc entry in %+v", info.DetectedBackends)
}

// A CUDA identity is still probed: skipping is about the device being the wrong
// *kind* of name, not about avoiding NVENC.
func TestNVENCProbedWhenTheConfiguredDeviceIsACUDAIdentity(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addNVIDIADevice(t, "nvidia0")
	ffmpeg := writeFakeFFmpeg(t, fullyCapableProbe())

	for _, device := range []string{"0", "cuda:1", "GPU-a1b2c3d4"} {
		t.Run(device, func(t *testing.T) {
			InvalidateHWProbeCache()
			info, err := DetectHWAccelWithFFmpegContextResult(context.Background(), hwAccelAuto, ffmpeg.path, device)
			if err != nil {
				t.Fatalf("DetectHWAccelWithFFmpegContextResult: %v", err)
			}
			for _, backend := range info.DetectedBackends {
				if backend.Backend != transcodeHWNVENC {
					continue
				}
				if backend.Skipped || !backend.Verified {
					t.Fatalf("nvenc = %+v, want a CUDA identity probed and verified", backend)
				}
				return
			}
			t.Fatalf("no nvenc entry in %+v", info.DetectedBackends)
		})
	}
}

// A configured multi-device hw_device is a set the balancer allocates *across*,
// not a list of alternatives detection picks from. Stopping the walk at the
// first pass left every later entry untested while the report said the backend
// was verified, and acquireHWDevice balanced onto them regardless — so a share
// of the node's transcodes started on a card that had already failed its smoke
// encode, and each of them died at ffmpeg init.
func TestConfiguredDeviceListIsProbedInFullAndBalancedOnlyAcrossPasses(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	env.addRenderDevice(t, "renderD129", "0x8086")
	probe := successfulQSVProbe()
	probe.smokeDeviceFailures = []string{"renderD129"}
	ffmpeg := writeFakeFFmpeg(t, probe)

	working, broken := env.devicePath("renderD128"), env.devicePath("renderD129")
	configured := working + "," + broken

	info, err := DetectHWAccelWithFFmpegContextResult(context.Background(), hwAccelAuto, ffmpeg.path, configured)
	if err != nil {
		t.Fatalf("DetectHWAccelWithFFmpegContextResult: %v", err)
	}
	if info.Resolved != transcodeHWQSV {
		t.Fatalf("resolved = %q, want qsv from the device that passed", info.Resolved)
	}

	if got := VerifiedHWDevices(transcodeHWQSV); !slices.Equal(got, []string{working}) {
		t.Fatalf("verified devices = %v, want only the card whose probe passed", got)
	}

	// Ten acquisitions is far more than the balancer needs to reach a second
	// device: with both present it alternates on the very next one.
	releases := make([]func(), 0, 10)
	t.Cleanup(func() {
		for _, release := range releases {
			release()
		}
	})
	for i := range 10 {
		device, _, release := acquireHWDevice(configured, transcodeHWQSV, "")
		releases = append(releases, release)
		if device != working {
			t.Fatalf("acquisition %d selected %q, want the verified device %q", i, device, working)
		}
	}
}

// With nothing verified — a cold process, or hw_accel named explicitly so the
// walk never ran — the balancer must not narrow to an empty set and must keep
// using every present device exactly as before.
func TestBalancingIsUnchangedWhenNoDeviceHasBeenVerified(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	env.addRenderDevice(t, "renderD129", "0x8086")
	first, second := env.devicePath("renderD128"), env.devicePath("renderD129")
	configured := first + "," + second

	selected := map[string]bool{}
	releases := make([]func(), 0, 2)
	t.Cleanup(func() {
		for _, release := range releases {
			release()
		}
	})
	for range 2 {
		device, _, release := acquireHWDevice(configured, transcodeHWQSV, "")
		releases = append(releases, release)
		selected[device] = true
	}
	if len(selected) != 2 {
		t.Fatalf("selected %v, want both devices used when no probe has ruled either out", selected)
	}
}

// The narrowing above is only safe because the whole configured list is probed.
// If the walk stopped at the first pass, every other card in the set would be
// unverified and the balancer would collapse a multi-GPU node onto one device —
// trading a correctness bug for a capacity one.
func TestEveryConfiguredDeviceThatPassesStaysInTheBalancer(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x8086")
	env.addRenderDevice(t, "renderD129", "0x8086")
	ffmpeg := writeFakeFFmpeg(t, successfulQSVProbe())

	first, second := env.devicePath("renderD128"), env.devicePath("renderD129")
	configured := first + "," + second

	if _, err := DetectHWAccelWithFFmpegContextResult(
		context.Background(), hwAccelAuto, ffmpeg.path, configured); err != nil {
		t.Fatalf("DetectHWAccelWithFFmpegContextResult: %v", err)
	}
	if got := VerifiedHWDevices(transcodeHWQSV); !slices.Equal(got, []string{first, second}) {
		t.Fatalf("verified devices = %v, want both cards in the configured set", got)
	}

	selected := map[string]bool{}
	releases := make([]func(), 0, 2)
	t.Cleanup(func() {
		for _, release := range releases {
			release()
		}
	})
	for range 2 {
		device, _, release := acquireHWDevice(configured, transcodeHWQSV, "")
		releases = append(releases, release)
		selected[device] = true
	}
	if len(selected) != 2 {
		t.Fatalf("selected %v, want the workload spread across both verified cards", selected)
	}
}

// An NVIDIA container is routinely given /dev/nvidia* and the toolkit with no
// /dev/dri at all: NVENC works and render_device_details is empty. Without a
// standalone uuid list the whole host contributes no hardware identity, so two
// such containers on one card look like two independent GPUs to the planner —
// which is the deployment where GPU sharing is most common and the placement
// mistake most expensive.
func TestCUDAOnlyHostPublishesItsGPUIdentities(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addNVIDIADevice(t, "nvidia0")
	previous := nvidiaSMIQuery
	nvidiaSMIQuery = func(context.Context) ([]byte, error) {
		return []byte("GPU-aaa, 00000000:03:00.0\nGPU-bbb, 00000000:04:00.0\n"), nil
	}
	t.Cleanup(func() {
		nvidiaSMIQuery = previous
		resetNVIDIAGPUUUIDs()
	})
	resetNVIDIAGPUUUIDs()
	ffmpeg := writeFakeFFmpeg(t, successfulNVENCProbe())

	info, err := DetectHWAccelWithFFmpegContextResult(context.Background(), hwAccelAuto, ffmpeg.path, "")
	if err != nil {
		t.Fatalf("DetectHWAccelWithFFmpegContextResult: %v", err)
	}
	if len(info.RenderDeviceDetails) != 0 {
		t.Fatalf("render devices = %+v, want none on a container with no /dev/dri", info.RenderDeviceDetails)
	}
	if !slices.Equal(info.NVIDIAGPUUUIDs, []string{"GPU-aaa", "GPU-bbb"}) {
		t.Fatalf("nvidia gpu uuids = %v, want both cards nvidia-smi reported", info.NVIDIAGPUUUIDs)
	}

	// A card appearing or disappearing has to move the hash, or a node that
	// gained or lost one is never refetched.
	withOne := info
	withOne.NVIDIAGPUUUIDs = []string{"GPU-aaa"}
	if ComputeCapabilityHash(info) == ComputeCapabilityHash(withOne) {
		t.Fatal("capability hash ignores the GPU identity list; a lost card would never trigger a refetch")
	}
}

// The walk's budget can also run out *inside* a probe rather than between two of
// them. Checking the context only at the top of the loop misses that entirely on
// the last candidate of the last backend: the probe returns a context error, the
// loop ends normally, and the report goes out claiming a verified regression —
// a new hash, a recorded drift note, and a node routed to software, for a GPU
// that is fine and merely slow to answer.
func TestDetectHWAccelReportsADeadlineInsideTheFinalProbe(t *testing.T) {
	env := setupHWAccelTest(t)
	// An AMD card so VAAPI is the only backend with candidates: with QSV also in
	// play, the walk's between-backends check would mask the gap.
	env.addRenderDevice(t, "renderD128", "0x1002")
	probe := successfulVAAPIProbe()
	probe.hang = true
	ffmpeg := writeFakeFFmpeg(t, probe)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	t.Cleanup(cancel)

	info, err := DetectHWAccelWithFFmpegContextResult(ctx, hwAccelAuto, ffmpeg.path, "")
	if !errors.Is(err, ErrHardwareDetectionIncomplete) {
		t.Fatalf("error = %v, want ErrHardwareDetectionIncomplete for a deadline inside the probe", err)
	}
	for _, backend := range info.DetectedBackends {
		if backend.Backend == transcodeHWVAAPI && backend.Verified {
			t.Fatalf("vaapi = %+v, want no verification claimed", backend)
		}
	}
}

// A probe outlives its caller by design, so a component that released its own
// claim on the GPU when its call returned can leave ffmpeg on the card with
// nothing accounting for it. The count is what lets the transcode node's
// re-probe gate see that.
func TestHWProbesInFlightCountsADetachedProbe(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x1002")
	ffmpeg := writeFakeFFmpeg(t, successfulVAAPIProbe())
	device := env.devicePath("renderD128")

	// Asserted as a floor rather than an exact count: the process-global counter
	// is shared with any probe an earlier test detached, and those drain on
	// their own schedule. What this test owns is that its own flight is counted
	// while it runs and released when it lands.
	//
	// Decided by channel receipts, not a sleep: the flight parks inside the
	// probe until this test has read the count.
	started := make(chan struct{})
	release := make(chan struct{})
	hwProbeFlightStarted = func() {
		close(started)
		<-release
	}
	t.Cleanup(func() { hwProbeFlightStarted = nil })

	var wg sync.WaitGroup
	wg.Go(func() {
		if ok, reason := ffmpegSupportsBackend(transcodeHWVAAPI, ffmpeg.path, device); !ok {
			t.Errorf("probe failed: %s", reason)
		}
	})

	<-started
	if got := HWProbesInFlight(); got < 1 {
		t.Fatalf("HWProbesInFlight() = %d while a smoke encode is running, want at least 1", got)
	}
	close(release)
	wg.Wait()
	awaitNoProbesInFlight(t)
}

// The claim has to be taken on the calling goroutine, not inside the function
// singleflight schedules. DoChan returns without waiting for that goroutine to
// run, so a caller whose context is already done returns immediately — and a
// component that released its own claim on the encoder when this call returned
// would hand a re-probe a card that is about to be busy.
//
// Checked the instant the call returns, which is the only instant that matters:
// that is when the caller's own claim goes away.
func TestHWProbeClaimIsHeldBeforeAnAbandonedCallerReturns(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x1002")
	ffmpeg := writeFakeFFmpeg(t, successfulVAAPIProbe())
	device := env.devicePath("renderD128")

	// The flight parks so it cannot finish and decrement before the assertion.
	release := make(chan struct{})
	var released sync.Once
	hwProbeFlightStarted = func() { <-release }
	t.Cleanup(func() {
		hwProbeFlightStarted = nil
		released.Do(func() { close(release) })
	})

	awaitNoProbesInFlight(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ok, _ := ffmpegSupportsBackendContext(ctx, transcodeHWVAAPI, ffmpeg.path, device); ok {
		t.Fatal("an already-canceled probe reported success")
	}

	if got := HWProbesInFlight(); got < 1 {
		t.Fatalf("HWProbesInFlight() = %d the moment the caller returned, want at least 1: "+
			"the detached flight was unaccounted for", got)
	}

	released.Do(func() { close(release) })
	// The claim comes back down once the flight lands, with no caller left to
	// receive it.
	awaitNoProbesInFlight(t)
}

// awaitNoProbesInFlight waits for every claim on the encoder to be released,
// including ones detached from a caller that has already returned. Waiting on
// the counter itself rather than on a fixed delay is what keeps these tests from
// depending on how loaded the machine is.
func awaitNoProbesInFlight(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got := HWProbesInFlight(); got == 0 {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("HWProbesInFlight() = %d, want every detached flight released", got)
		}
		runtime.Gosched()
	}
}

// Clearing the VideoToolbox cache does not supersede a probe already running:
// that call stays registered under its key, so a rebuild joins it rather than
// starting a cold one, and its completion repopulates the map — the re-probe
// then publishes the verdict it was asked to discard. The generation in the key
// is what moves the rebuild onto a fresh flight.
func TestInvalidateHWProbeCacheSupersedesAnInFlightVideoToolboxProbe(t *testing.T) {
	setupHWAccelTest(t)
	currentGOOS = darwinGOOS
	ffmpeg := writeFakeFFmpeg(t, successfulVideoToolboxProbe())

	// The first flight parks inside the probe and stays there while the second
	// call is made, so "did the second start its own flight" is decided by
	// channel receipts rather than by how the two happen to be scheduled.
	var starts atomic.Int32
	blocked := make(chan struct{})
	firstStarted := make(chan struct{})
	previous := videoToolboxProbeStarted
	videoToolboxProbeStarted = func() {
		if starts.Add(1) == 1 {
			close(firstStarted)
			<-blocked
		}
	}
	t.Cleanup(func() { videoToolboxProbeStarted = previous })

	var wg sync.WaitGroup
	wg.Go(func() {
		if result := cachedVideoToolboxProbe(ffmpeg.path); !result.available {
			t.Errorf("in-flight probe failed: %s", result.reason)
		}
	})
	<-firstStarted

	InvalidateHWProbeCache()

	second := make(chan hardwareProbeResult, 1)
	wg.Go(func() { second <- cachedVideoToolboxProbe(ffmpeg.path) })

	// The second call must reach its own flight while the first is still parked.
	deadline := time.Now().Add(5 * time.Second)
	for starts.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the re-probe joined the flight it was supposed to supersede")
		}
		runtime.Gosched()
	}

	close(blocked)
	if result := <-second; !result.available {
		t.Fatalf("post-invalidation probe failed: %s", result.reason)
	}
	wg.Wait()
}

// A capable Mac was publishing resolved:"none" with no detected backends: the
// snapshot builder only walked backends on Linux, so the VideoToolbox probe
// that resolution uses never ran here. The API stored that as the node's
// durable inventory — software-only, planned for software tone mapping, with an
// operator re-probe that could not verify otherwise.
func TestDetectHWAccelPublishesVideoToolboxOnDarwin(t *testing.T) {
	setupHWAccelTest(t)
	currentGOOS = darwinGOOS
	ffmpeg := writeFakeFFmpeg(t, successfulVideoToolboxProbe())

	info, err := DetectHWAccelWithFFmpegContextResult(context.Background(), hwAccelAuto, ffmpeg.path, "")
	if err != nil {
		t.Fatalf("DetectHWAccelWithFFmpegContextResult: %v", err)
	}
	if info.Resolved != transcodeHWVideoToolbox {
		t.Fatalf("Resolved = %q, want videotoolbox", info.Resolved)
	}
	if len(info.DetectedBackends) != 1 || info.DetectedBackends[0].Backend != transcodeHWVideoToolbox ||
		!info.DetectedBackends[0].Verified {
		t.Fatalf("DetectedBackends = %+v, want a verified videotoolbox entry", info.DetectedBackends)
	}
}

// A Mac whose probe fails publishes the failure rather than silence, so an
// operator can see why — but a probe cut short by the caller's deadline is not
// a verdict about the hardware and must not be hashed as one.
func TestDetectHWAccelReportsAFailedVideoToolboxProbe(t *testing.T) {
	setupHWAccelTest(t)
	currentGOOS = darwinGOOS
	ffmpeg := writeFakeFFmpeg(t, fakeFFmpegProbe{})

	info, err := DetectHWAccelWithFFmpegContextResult(context.Background(), hwAccelAuto, ffmpeg.path, "")
	if err != nil {
		t.Fatalf("DetectHWAccelWithFFmpegContextResult: %v", err)
	}
	if info.Resolved != HWAccelNone {
		t.Fatalf("Resolved = %q, want none", info.Resolved)
	}
	if len(info.DetectedBackends) != 1 || info.DetectedBackends[0].Verified ||
		info.DetectedBackends[0].Reason == "" {
		t.Fatalf("DetectedBackends = %+v, want an unverified entry carrying a reason", info.DetectedBackends)
	}

	hung := writeFakeFFmpeg(t, fakeFFmpegProbe{hang: true})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	t.Cleanup(cancel)
	if _, err := DetectHWAccelWithFFmpegContextResult(ctx, hwAccelAuto, hung.path, ""); !errors.Is(err, ErrHardwareDetectionIncomplete) {
		t.Fatalf("error = %v, want ErrHardwareDetectionIncomplete for a deadline inside the probe", err)
	}
}

// The VideoToolbox flight outlives its caller like the others, so it has to be
// counted like the others — otherwise a re-probe sees an idle encoder and a
// new-generation probe starts beside the one still running.
func TestHWProbesInFlightCountsADetachedVideoToolboxProbe(t *testing.T) {
	setupHWAccelTest(t)
	currentGOOS = darwinGOOS
	ffmpeg := writeFakeFFmpeg(t, successfulVideoToolboxProbe())
	awaitNoProbesInFlight(t)

	release := make(chan struct{})
	var released, announced sync.Once
	started := make(chan struct{})
	previous := videoToolboxProbeStarted
	videoToolboxProbeStarted = func() {
		announced.Do(func() { close(started) })
		<-release
	}
	t.Cleanup(func() {
		videoToolboxProbeStarted = previous
		released.Do(func() { close(release) })
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = cachedVideoToolboxProbeContext(ctx, ffmpeg.path)
	}()
	<-started
	cancel()
	<-done

	if got := HWProbesInFlight(); got < 1 {
		t.Fatalf("HWProbesInFlight() = %d with a detached VideoToolbox probe running, want at least 1", got)
	}
	released.Do(func() { close(release) })
	awaitNoProbesInFlight(t)
}

// A scheduled capability snapshot is how a long-running node notices its
// hardware changing, and on an NVIDIA-only node — /dev/nvidia* and the toolkit,
// no /dev/dri — a card's uuid is the only trace of it in the report. A listing
// cached past the walk that took it would republish a hot-removed card in every
// snapshot until someone re-probed by hand.
func TestCapabilityWalkRequeriesNVIDIAIdentities(t *testing.T) {
	previous := nvidiaSMIQuery
	answer := "GPU-aaa, 00000000:03:00.0\n"
	queries := 0
	nvidiaSMIQuery = func(context.Context) ([]byte, error) {
		queries++
		return []byte(answer), nil
	}
	t.Cleanup(func() {
		nvidiaSMIQuery = previous
		resetNVIDIAGPUUUIDs()
	})
	resetNVIDIAGPUUUIDs()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first, _ := DetectHWAccelWithFFmpegContextResult(ctx, hwAccelAuto, "", "")
	if !slices.Contains(first.NVIDIAGPUUUIDs, "GPU-aaa") {
		t.Fatalf("first walk reported %v, want the card nvidia-smi named", first.NVIDIAGPUUUIDs)
	}

	// The card is hot-removed. Nothing else in this node's report mentions it.
	answer = ""
	second, _ := DetectHWAccelWithFFmpegContextResult(ctx, hwAccelAuto, "", "")
	if slices.Contains(second.NVIDIAGPUUUIDs, "GPU-aaa") {
		t.Fatalf("second walk still reported %v for a card that is gone", second.NVIDIAGPUUUIDs)
	}
	if queries < 2 {
		t.Fatalf("nvidia-smi queried %d times across two walks, want one per walk", queries)
	}
}
