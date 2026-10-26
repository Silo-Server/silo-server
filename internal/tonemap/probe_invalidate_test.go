package tonemap

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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
		if len(args) > 0 && args[len(args)-1] == "-filters" {
			if runs.Add(1) == 1 {
				close(started)
				<-blocked
			}
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

	// Each probe lists filters once; counting all commands would let a single
	// probe's encoder listing and conversions masquerade as another probe.
	if got := runs.Load(); got != 2 {
		t.Fatalf("probe starts = %d, want a separate post-invalidation probe", got)
	}
}
