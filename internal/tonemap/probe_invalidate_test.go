package tonemap

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A non-empty inventory never expires, which is the blind spot
// InvalidateProbeCache closes: a driver replaced underneath a running node
// changes the answer without changing the binary's identity key. The observable
// contract is that the probe commands run again.
func TestInvalidateProbeCacheForcesAnotherProbe(t *testing.T) {
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
	clock := func() time.Time { return now }

	probe := func(stage string) int {
		capabilities, err := probeCached(context.Background(), "/ffmpeg-invalidate", BackendSoftware, "", runner, clock)
		if err != nil {
			t.Fatalf("%s probe error = %v", stage, err)
		}
		if len(capabilities) != 1 {
			t.Fatalf("%s probe capabilities = %#v, want one software entry", stage, capabilities)
		}
		return calls
	}

	first := probe("first")
	if first == 0 {
		t.Fatal("first probe ran no commands")
	}
	if cached := probe("cached"); cached != first {
		t.Fatalf("cached probe ran %d commands, want the cached inventory reused", cached-first)
	}

	InvalidateProbeCache()

	if reprobed := probe("re-probed"); reprobed != first*2 {
		t.Fatalf("probe after invalidation ran %d commands total, want %d", reprobed, first*2)
	}
}

// Clearing the map is not enough: a probe already in flight completes and
// stores its inventory, so without a generation in the key the caller that
// invalidated would join that flight and be handed the very result it asked to
// discard — a re-probe reporting "nothing changed" about hardware it never
// re-examined. The generation moves the key instead, so the in-flight probe
// writes where nothing will read and the next caller runs a cold one.
func TestInvalidateProbeCacheSupersedesAnInFlightProbe(t *testing.T) {
	resetProbeCache(t)
	now := time.Unix(100, 0)
	clock := func() time.Time { return now }

	// started closes once the first probe is inside its runner; blocked holds it
	// there until the test has invalidated, so the race is decided rather than
	// slept on.
	started := make(chan struct{})
	blocked := make(chan struct{})
	var runs atomic.Int32
	runner := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if runs.Add(1) == 1 {
			close(started)
			<-blocked
		}
		if len(args) > 0 && args[len(args)-1] == "-filters" {
			return []byte(" .S. zscale V->V\n .S. tonemapx V->V\n .S. sidedata V->V\n"), nil
		}
		if len(args) > 0 && args[len(args)-1] == "-encoders" {
			return []byte("libx264"), nil
		}
		return nil, nil
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := probeCached(context.Background(), "/ffmpeg-inflight", BackendSoftware, "", runner, clock); err != nil {
			t.Errorf("in-flight probe error = %v", err)
		}
	})

	<-started
	InvalidateProbeCache()
	close(blocked)

	capabilities, err := probeCached(context.Background(), "/ffmpeg-inflight", BackendSoftware, "", runner, clock)
	if err != nil {
		t.Fatalf("post-invalidation probe error = %v", err)
	}
	if len(capabilities) != 1 {
		t.Fatalf("post-invalidation capabilities = %#v, want one software entry", capabilities)
	}
	wg.Wait()

	// One command from the blocked flight is enough to prove the second probe
	// did not simply wait on it: a joined caller would have run none of its own.
	if got := runs.Load(); got < 2 {
		t.Fatalf("runner invocations = %d, want the post-invalidation probe to run its own commands", got)
	}
}
