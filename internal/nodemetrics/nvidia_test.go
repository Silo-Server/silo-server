package nodemetrics

import (
	"context"
	"errors"
	"testing"
	"time"
)

const nvidiaSMIOutput = "0, GPU-1234abcd, 00000000:03:00.0, 71, 63, 12, 812, 8192\n" +
	"1, GPU-5678efgh, 00000000:04:00.0, 5, 0, 0, 100, 8192\n"

func TestParseNVIDIASMI(t *testing.T) {
	gpus := parseNVIDIASMI([]byte(nvidiaSMIOutput))
	if len(gpus) != 2 {
		t.Fatalf("gpus = %+v, want 2", gpus)
	}
	first := gpus[0]
	if first.Index != 0 || first.UUID != "GPU-1234abcd" {
		t.Fatalf("gpus[0] identity = %+v", first)
	}
	// The wide domain nvidia-smi prints has to normalize to the sysfs form or it
	// will never join with a DRM device.
	if first.PCIAddress != "0000:03:00.0" {
		t.Fatalf("PCIAddress = %q, want the normalized sysfs form", first.PCIAddress)
	}
	if *first.GPUUtil != 71 || *first.EncoderUtil != 63 || *first.DecoderUtil != 12 {
		t.Fatalf("gpus[0] utilization = %d/%d/%d", *first.GPUUtil, *first.EncoderUtil, *first.DecoderUtil)
	}
	if *first.MemUsedMB != 812 || *first.MemTotalMB != 8192 {
		t.Fatalf("gpus[0] memory = %d/%d", *first.MemUsedMB, *first.MemTotalMB)
	}
}

// Drivers print "[N/A]" for a column a card does not support. One unsupported
// column must not discard the whole row — nor be read as a measured zero, which
// would show an engine nobody can see as idle.
func TestParseNVIDIASMIToleratesPlaceholders(t *testing.T) {
	gpus := parseNVIDIASMI([]byte("0, GPU-x, 00000000:03:00.0, [N/A], [Not Supported], 4, 100, [N/A]\n"))
	if len(gpus) != 1 {
		t.Fatalf("gpus = %+v, want the row kept", gpus)
	}
	got := gpus[0]
	if got.GPUUtil != nil || got.EncoderUtil != nil || got.MemTotalMB != nil {
		t.Fatalf("gpus[0] = %+v, want the placeholder columns unset", got)
	}
	if got.DecoderUtil == nil || *got.DecoderUtil != 4 {
		t.Fatalf("DecoderUtil = %v, want the reported 4 preserved", got.DecoderUtil)
	}
	if got.MemUsedMB == nil || *got.MemUsedMB != 100 {
		t.Fatalf("MemUsedMB = %v, want the reported 100 preserved", got.MemUsedMB)
	}
	// One reported engine still gives a video reading; both missing gives none.
	if video := got.videoUtil(); video == nil || *video != 4 {
		t.Fatalf("videoUtil() = %v, want the one engine the driver did report", video)
	}
	if video := (nvidiaGPU{}).videoUtil(); video != nil {
		t.Fatalf("videoUtil() = %d with neither engine reported, want none", *video)
	}
}

// A GPU that has both DRM counters and an nvidia-smi row must be one entry
// crediting both sources, not two entries.
func TestSampleGPUMergesFdinfoAndNVIDIASMIOnOneDevice(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	tree.writeIdleHost()
	tree.write("4242/fdinfo/3", `drm-driver:	nvidia-drm
drm-pdev:	0000:03:00.0
drm-client-id:	1
drm-engine-video:	1000000000 ns
`)

	s := newTestSampler(t, tree, clock, Options{
		FFmpegChildren: func() []int { return []int{4242} },
		DeviceIdentities: func() []DeviceIdentity {
			return []DeviceIdentity{{Path: "/dev/dri/renderD128", PCIAddress: "0000:03:00.0", Vendor: "nvidia"}}
		},
	})
	s.runNVIDIASMI = func(context.Context) ([]byte, error) {
		return []byte("0, GPU-1234abcd, 00000000:03:00.0, 71, 63, 12, 812, 8192\n"), nil
	}
	s.sample(context.Background())

	gpu := s.Snapshot().GPU
	if len(gpu) != 1 {
		t.Fatalf("GPU = %+v, want the two views merged onto one device", gpu)
	}
	if gpu[0].Device != "/dev/dri/renderD128" {
		t.Fatalf("Device = %q, want the render node path kept", gpu[0].Device)
	}
	if gpu[0].Source != SourceFdinfoNVIDIASMI {
		t.Fatalf("Source = %q, want %q", gpu[0].Source, SourceFdinfoNVIDIASMI)
	}
	if gpu[0].TotalBusyPct == nil || *gpu[0].TotalBusyPct != 71 {
		t.Fatalf("TotalBusyPct = %v, want the whole-GPU reading", gpu[0].TotalBusyPct)
	}
}

// NVENC workloads are counted under a CUDA name or a GPU UUID, but a card whose
// DRM node this process can read is displayed by its render path. Looking
// sessions up by display name alone reports an idle GPU on an NVIDIA node that
// is transcoding.
func TestSampleGPUJoinsNVENCSessionsThroughDeviceAliases(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sessions map[string]int
		want     int
	}{
		{name: "counted by cuda index", sessions: map[string]int{"cuda:0": 3}, want: 3},
		{name: "counted by gpu uuid", sessions: map[string]int{"GPU-1234abcd": 2}, want: 2},
		{name: "counted by render path", sessions: map[string]int{"/dev/dri/renderD128": 1}, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree := newProcTree(t)
			clock := newFakeClock()
			tree.writeIdleHost()

			s := newTestSampler(t, tree, clock, Options{
				DeviceSessions: func() map[string]int { return tc.sessions },
				DeviceIdentities: func() []DeviceIdentity {
					// The proprietary driver with modeset does expose a render
					// node, so this is the ordinary bare-metal NVIDIA shape.
					return []DeviceIdentity{{Path: "/dev/dri/renderD128", PCIAddress: "0000:03:00.0", Vendor: "nvidia"}}
				},
			})
			s.runNVIDIASMI = func(context.Context) ([]byte, error) {
				return []byte("0, GPU-1234abcd, 00000000:03:00.0, 71, 63, 12, 812, 8192\n"), nil
			}
			s.sample(context.Background())

			gpu := s.Snapshot().GPU
			if len(gpu) != 1 {
				t.Fatalf("GPU = %+v, want one device", gpu)
			}
			if gpu[0].Device != "/dev/dri/renderD128" {
				t.Fatalf("Device = %q, want the render node path", gpu[0].Device)
			}
			if gpu[0].Sessions != tc.want {
				t.Fatalf("Sessions = %d, want %d for %v", gpu[0].Sessions, tc.want, tc.sessions)
			}
		})
	}
}

// Two cards must not both claim a count keyed by a name only one of them
// answers to.
func TestSampleGPUDoesNotDoubleCountSessionsAcrossDevices(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	tree.writeIdleHost()

	s := newTestSampler(t, tree, clock, Options{
		DeviceSessions: func() map[string]int { return map[string]int{"cuda:1": 2} },
	})
	s.runNVIDIASMI = func(context.Context) ([]byte, error) { return []byte(nvidiaSMIOutput), nil }
	s.sample(context.Background())

	total := 0
	for _, gpu := range s.Snapshot().GPU {
		total += gpu.Sessions
		if gpu.Device == "cuda:0" && gpu.Sessions != 0 {
			t.Fatalf("cuda:0 claimed %d sessions belonging to cuda:1", gpu.Sessions)
		}
	}
	if total != 2 {
		t.Fatalf("sessions across devices = %d, want the 2 counted exactly once", total)
	}
}

// Retiring a source until the process restarts confuses "this host has no
// toolkit" with "this driver is resetting" — they look identical for the five
// samples the limit counts. An NVENC node that hit a transient driver outage
// would then report no utilization and no VRAM for as long as it stayed up.
func TestNVIDIACircuitBreakerHalfOpensAfterItsRetryInterval(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	s := newTestSampler(t, tree, clock, Options{})
	calls := 0
	fail := true
	s.runNVIDIASMI = func(context.Context) ([]byte, error) {
		calls++
		if fail {
			return nil, errors.New("nvidia-smi: driver/library version mismatch")
		}
		return []byte(nvidiaSMIOutput), nil
	}

	for range sourceFailureLimit + 5 {
		s.queryNVIDIA(context.Background())
	}
	if calls != sourceFailureLimit {
		t.Fatalf("nvidia-smi invoked %d times, want it retired after %d failures", calls, sourceFailureLimit)
	}

	// Still retired just short of the interval: the point of the breaker is that
	// a toolkit-less host stops paying for a subprocess every few seconds.
	clock.advance(sourceRetryInterval - time.Second)
	s.queryNVIDIA(context.Background())
	if calls != sourceFailureLimit {
		t.Fatalf("nvidia-smi invoked %d times before the retry interval elapsed, want %d", calls, sourceFailureLimit)
	}

	// One probationary query per interval, and a failure buys another interval
	// rather than a burst.
	clock.advance(2 * time.Second)
	s.queryNVIDIA(context.Background())
	s.queryNVIDIA(context.Background())
	if calls != sourceFailureLimit+1 {
		t.Fatalf("nvidia-smi invoked %d times, want exactly one probationary query", calls)
	}

	// The driver comes back. The probationary query that succeeds closes the
	// breaker, and sampling resumes at its normal rate.
	fail = false
	clock.advance(sourceRetryInterval)
	if gpus := s.queryNVIDIA(context.Background()); len(gpus) != 2 {
		t.Fatalf("recovered query returned %d gpus, want 2", len(gpus))
	}
	if gpus := s.queryNVIDIA(context.Background()); len(gpus) != 2 {
		t.Fatalf("query after recovery returned %d gpus, want the breaker closed", len(gpus))
	}
	if calls != sourceFailureLimit+3 {
		t.Fatalf("nvidia-smi invoked %d times, want the breaker to stop gating once it answered", calls)
	}
}
