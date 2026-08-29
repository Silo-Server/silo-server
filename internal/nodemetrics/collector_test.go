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

func TestCollectorExposesSnapshot(t *testing.T) {
	f := newDiskFixture(t, "/transcode")
	f.answer("/transcode", fsStats{UsedBytes: 100 << 30, TotalBytes: 500 << 30, FSID: "a:1"})
	s := f.sampler
	s.runNVIDIASMI = func(context.Context) ([]byte, error) {
		return []byte("0, GPU-x, 00000000:03:00.0, 71, 63, 12, 812, 8192\n"), nil
	}
	s.sessions = func() map[string]int { return map[string]int{"cuda:0": 2} }

	f.sampleAndSettle(t, 1)
	f.sampleAndSettle(t, 1)

	values := gatherNames(t, s)
	for _, name := range []string{
		"streamapp_node_cpu_percent",
		"streamapp_node_load1",
		"streamapp_node_memory_used_bytes",
		"streamapp_node_memory_total_bytes",
		"streamapp_node_disk_used_bytes",
		"streamapp_node_disk_total_bytes",
		"streamapp_node_network_rx_bps",
		"streamapp_node_network_tx_bps",
		"streamapp_node_gpu_video_busy_percent",
		"streamapp_node_gpu_busy_percent",
		"streamapp_node_gpu_sessions",
		"streamapp_node_gpu_vram_used_bytes",
		"streamapp_node_gpu_vram_total_bytes",
	} {
		if _, ok := values[name]; !ok {
			t.Fatalf("%s missing from the scrape (got %v)", name, values)
		}
	}
	// nvidia-smi reports encoder and decoder but nothing for the render engine,
	// and this card has no DRM counters to supply one. Exporting the missing
	// column as 0 would draw an idle 3D engine for a GPU nobody measured.
	if _, ok := values["streamapp_node_gpu_render_busy_percent"]; ok {
		t.Fatalf("render busy exported for an nvidia-smi-only card: %v", values)
	}

	// Bytes on the wire, gibibytes in the JSON: the conversion has to survive.
	if got := values["streamapp_node_disk_used_bytes"]; got != float64(100<<30) {
		t.Fatalf("disk used = %v, want %v bytes", got, float64(100<<30))
	}
	if got := values["streamapp_node_gpu_sessions"]; got != 2 {
		t.Fatalf("gpu sessions = %v, want 2", got)
	}
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

// A host that cannot be sampled must publish nothing rather than a wall of
// zeros that alert rules would read as a healthy idle machine.
func TestCollectorExposesNothingWhenUnavailable(t *testing.T) {
	tree := newProcTree(t)
	s := newTestSampler(t, tree, newFakeClock(), Options{})
	s.goos = "darwin"
	s.sample(context.Background())

	if values := gatherNames(t, s); len(values) != 0 {
		t.Fatalf("scrape returned %v on an unsampled host, want nothing", values)
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

// Registration is guarded so a second sampler cannot panic the process on a
// duplicate collector.
func TestRegisterCollectorIsIdempotent(t *testing.T) {
	tree := newProcTree(t)
	first := newTestSampler(t, tree, newFakeClock(), Options{})
	second := newTestSampler(t, tree, newFakeClock(), Options{})
	registerCollector(first)
	registerCollector(second)
}

// The positional library label is a promise about which volume a series
// describes, and Prometheus has no way to signal that the promise moved: a
// mount going unavailable used to renumber every library after it, so an alert
// rule keyed on mount="library-1" silently followed a different disk with no
// gap in the series to show it happened.
func TestCollectorKeepsLibraryLabelsPositional(t *testing.T) {
	f := newDiskFixture(t, "/transcode", "/mnt/nas/movies", "/mnt/nas/shows")
	f.answer("/transcode", fsStats{UsedBytes: 1 << 30, TotalBytes: 10 << 30, FSID: "a:1"})
	// /mnt/nas/movies is never answered, so it stays unavailable — the media
	// root that lives on another node, or the export whose server went away.
	f.answer("/mnt/nas/shows", fsStats{UsedBytes: 3 << 30, TotalBytes: 30 << 30, FSID: "c:1"})

	f.sampleAndSettle(t, 3)
	f.sampleAndSettle(t, 3)

	used := diskSeriesByLabel(t, f.sampler)
	if _, reported := used["library-1"]; reported {
		t.Fatalf("unavailable mount emitted a series: %v", used)
	}
	// The measurable root keeps the index its position earns, rather than
	// sliding into the missing one's label.
	if got, ok := used["library-2"]; !ok || got != float64(3<<30) {
		t.Fatalf("library-2 = %v (present=%t), want the second library root's %v bytes",
			got, ok, float64(3<<30))
	}
}

// diskSeriesByLabel collects streamapp_node_disk_used_bytes keyed by its mount
// label.
func diskSeriesByLabel(t *testing.T, sampler *Sampler) map[string]float64 {
	t.Helper()
	registry := prometheus.NewRegistry()
	if err := registry.Register(collector{sampler: sampler}); err != nil {
		t.Fatalf("register collector: %v", err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	byLabel := map[string]float64{}
	for _, family := range families {
		if family.GetName() != "streamapp_node_disk_used_bytes" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == gpuDeviceLabel {
					continue
				}
				byLabel[label.GetValue()] = metric.GetGauge().GetValue()
			}
		}
	}
	return byLabel
}

// A GPU nothing could measure has zeros that mean "not taken", not "idle". The
// JSON surfaces carry `source` alongside and render the difference; a Prometheus
// sample cannot, so an exported 0 would show a busy-but-unobservable card as
// idle on every dashboard. The session count still ships: it comes from this
// process's own accounting rather than a driver.
func TestCollectorOmitsUnmeasuredGPUEngineGauges(t *testing.T) {
	sampler := NewFixedSamplerForTest(Snapshot{
		Available: true,
		System:    &SystemStats{},
		GPU: []GPUStats{{
			Device: "cuda:0", Vendor: vendorNVIDIA, Sessions: 2, Source: SourceUnavailable,
		}},
	})

	values := gatherNames(t, sampler)
	if _, present := values["streamapp_node_gpu_video_busy_percent"]; present {
		t.Fatal("an unmeasured GPU exported a video busy percentage")
	}
	if _, present := values["streamapp_node_gpu_render_busy_percent"]; present {
		t.Fatal("an unmeasured GPU exported a render busy percentage")
	}
	if _, present := values["streamapp_node_gpu_busy_percent"]; present {
		t.Fatal("an unmeasured GPU exported a whole-GPU utilization")
	}
	if got := values["streamapp_node_gpu_sessions"]; got != 2 {
		t.Fatalf("gpu sessions = %v, want the workload count exported regardless", got)
	}
}

// Whole-GPU utilization is what nvidia-smi can see and fdinfo cannot: the card's
// own busyness, other tenants included. Without it an operator watching only the
// engine gauges sees this node's idle transcoder and no sign that the card it is
// planned onto is saturated by someone else.
func TestCollectorExportsWholeGPUUtilization(t *testing.T) {
	sampler := NewFixedSamplerForTest(Snapshot{
		Available: true,
		System:    &SystemStats{},
		GPU: []GPUStats{{
			Device: "cuda:0", Vendor: vendorNVIDIA, Sessions: 0,
			VideoBusyPct: ptr(3), TotalBusyPct: ptr(94), Source: SourceNVIDIASMI,
		}},
	})

	values := gatherNames(t, sampler)
	if got := values["streamapp_node_gpu_busy_percent"]; got != 94 {
		t.Fatalf("whole-GPU busy = %v, want the card's 94 rather than this node's 3", got)
	}
}

// A measured source exports both engines, including a genuine zero — which does
// mean idle.
func TestCollectorExportsMeasuredGPUEngineGauges(t *testing.T) {
	sampler := NewFixedSamplerForTest(Snapshot{
		Available: true,
		System:    &SystemStats{},
		GPU: []GPUStats{{
			Device: "/dev/dri/renderD128", Sessions: 0,
			VideoBusyPct: ptr(0), RenderBusyPct: ptr(0), Source: SourceFdinfo,
		}},
	})

	values := gatherNames(t, sampler)
	if _, present := values["streamapp_node_gpu_video_busy_percent"]; !present {
		t.Fatal("a measured idle GPU omitted its video busy percentage")
	}
	if _, present := values["streamapp_node_gpu_render_busy_percent"]; !present {
		t.Fatal("a measured idle GPU omitted its render busy percentage")
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
