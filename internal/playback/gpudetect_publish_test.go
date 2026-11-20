package playback

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
	stubNVIDIASMIQuery(t, func(context.Context) ([]byte, error) {
		queries++
		if answer == "" {
			return nil, errors.New("nvidia-smi not installed")
		}
		return []byte(answer), nil
	})

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
