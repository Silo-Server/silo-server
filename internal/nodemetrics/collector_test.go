package nodemetrics

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// gatherNames collects one scrape's metric names and the first sample value per
// name, which is enough to assert the exposition without asserting on the
// registry's formatting.
func gatherNames(t *testing.T, sampler *Sampler) map[string]float64 {
	t.Helper()
	registry := prometheus.NewRegistry()
	if err := registry.Register(collector{sampler: sampler}); err != nil {
		t.Fatalf("register collector: %v", err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	values := make(map[string]float64, len(families))
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			if _, seen := values[family.GetName()]; seen {
				continue
			}
			values[family.GetName()] = metric.GetGauge().GetValue()
		}
	}
	return values
}

// /metrics is unauthenticated on the same listener that serves the API and the
// SPA. Labeling disk series by path would let anyone who can reach the server
// enumerate its media mounts — the layout the admin-authenticated resources
// endpoint exists to gate.
func TestCollectorDoesNotLabelDisksByPath(t *testing.T) {
	f := newDiskFixture(t, "/transcode", "/mnt/nas/movies", "/srv/private/kids-shows")
	f.answer("/transcode", fsStats{UsedBytes: 1 << 30, TotalBytes: 10 << 30, FSID: "a:1"})
	f.answer("/mnt/nas/movies", fsStats{UsedBytes: 2 << 30, TotalBytes: 20 << 30, FSID: "b:1"})
	f.answer("/srv/private/kids-shows", fsStats{UsedBytes: 3 << 30, TotalBytes: 30 << 30, FSID: "c:1"})

	f.sampleAndSettle(t, 3)
	f.sampleAndSettle(t, 3)

	registry := prometheus.NewRegistry()
	if err := registry.Register(collector{sampler: f.sampler}); err != nil {
		t.Fatalf("register collector: %v", err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	labels := map[string]bool{}
	for _, family := range families {
		if family.GetName() != "streamapp_node_disk_used_bytes" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if strings.Contains(label.GetValue(), "/") {
					t.Fatalf("%s carries a filesystem path in label %s=%q",
						family.GetName(), label.GetName(), label.GetValue())
				}
				labels[label.GetValue()] = true
			}
		}
	}
	for _, want := range []string{"scratch", "library-1", "library-2"} {
		if !labels[want] {
			t.Fatalf("mount label %q missing from %v", want, labels)
		}
	}
}

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

// gatherLabeled reads one metric family keyed by its single label value, so a
// test can assert per-mount or per-device rather than on whichever series the
// registry happened to order first.
func gatherLabeled(t *testing.T, sampler *Sampler, name string) map[string]float64 {
	t.Helper()
	registry := prometheus.NewRegistry()
	if err := registry.Register(collector{sampler: sampler}); err != nil {
		t.Fatalf("register collector: %v", err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	values := map[string]float64{}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				values[label.GetValue()] = metric.GetGauge().GetValue()
			}
		}
	}
	return values
}

// A mount whose probe has not come back keeps exporting its last real numbers —
// dropping them would blank a dashboard for a network mount that is merely slow,
// which sets Stale routinely — but a scrape carries no `stale` field the way the
// JSON surfaces do. Without a series saying so, a fill alert sits green forever
// on a volume that stopped answering at 40% and has been filling since.
func TestCollectorMarksStaleDiskReadings(t *testing.T) {
	sampler := NewFixedSamplerForTest(Snapshot{
		Available: true,
		System: &SystemStats{Disks: []DiskStats{
			{Role: ScratchDiskRole, UsedGB: 100, TotalGB: 500},
			{Role: "library-1", UsedGB: 400, TotalGB: 500, Stale: true},
			{Role: "library-2", Unavailable: true},
		}},
	})

	stale := gatherLabeled(t, sampler, "streamapp_node_disk_stale")
	if got, ok := stale[ScratchDiskRole]; !ok || got != 0 {
		t.Fatalf("scratch stale = %v (present=%v), want a measured 0", got, ok)
	}
	if got, ok := stale["library-1"]; !ok || got != 1 {
		t.Fatalf("library-1 stale = %v (present=%v), want 1", got, ok)
	}
	// An unavailable mount has no measurement at all, so it exports nothing —
	// including no staleness, which would imply there were numbers to qualify.
	if _, ok := stale["library-2"]; ok {
		t.Fatalf("unavailable mount exported a staleness series: %v", stale)
	}

	// The values themselves still ship for the stale mount: they are real, only
	// old, and an operator needs to see a volume that was nearly full.
	used := gatherLabeled(t, sampler, "streamapp_node_disk_used_bytes")
	if got, ok := used["library-1"]; !ok || got != 400*float64(1024*1024*1024) {
		t.Fatalf("library-1 used = %v (present=%v), want the carried-over reading kept", got, ok)
	}
	if _, ok := used["library-2"]; ok {
		t.Fatalf("unavailable mount exported a used-bytes series: %v", used)
	}
}
