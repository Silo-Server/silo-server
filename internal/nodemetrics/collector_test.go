package nodemetrics

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// A scrape must never wait on sampling. If Collect took a lock the disk prober
// holds, a wedged mount would turn into a scrape timeout that looks like the
// node being down — which is the exact failure this package exists to avoid.
func TestCollectorDoesNotBlockOnAWedgedMount(t *testing.T) {
	f := newDiskFixture(t, "/transcode", "/nfs")
	f.answer("/transcode", fsStats{UsedBytes: 1 << 30, TotalBytes: 10 << 30, FSID: "a:1"})
	f.wedge(t, "/nfs")

	f.sampler.sample(context.Background())
	f.awaitProbes(t, 1) // only /transcode can finish; /nfs is parked

	registry := prometheus.NewRegistry()
	if err := registry.Register(collector{sampler: f.sampler}); err != nil {
		t.Fatalf("register collector: %v", err)
	}
	type result struct {
		families []*dto.MetricFamily
		err      error
	}
	scraped := make(chan result, 1)
	go func() {
		families, err := registry.Gather()
		scraped <- result{families: families, err: err}
	}()
	select {
	case got := <-scraped:
		if got.err != nil {
			t.Fatalf("gather: %v", got.err)
		}
		if len(got.families) == 0 {
			t.Fatal("scrape returned no metrics while a mount was wedged")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scrape blocked behind a wedged mount")
	}
}
