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
