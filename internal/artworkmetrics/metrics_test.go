package artworkmetrics

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func gaugeValue(t testing.TB, gauge prometheus.Metric) float64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := gauge.Write(metric); err != nil {
		t.Fatalf("read gauge: %v", err)
	}
	return metric.GetGauge().GetValue()
}

func counterValue(t testing.TB, counter prometheus.Metric) float64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := counter.Write(metric); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return metric.GetCounter().GetValue()
}

func TestArtworkMetricLabelsAreBounded(t *testing.T) {
	tests := []struct {
		name    string
		allowed map[string]struct{}
		known   string
	}{
		{"materialization source", materializationSources, "provider"},
		{"materialization result", materializationResults, "adopted"},
		{"store backend", storeBackends, "s3"},
		{"store operation", storeOperations, "maintenance_delete"},
		{"delivery route", deliveryRoutes, "direct_library"},
		{"delivery result", deliveryResults, "conditional_hit"},
		{"store health", storeHealthStates, "wrong_mount"},
		{"repair result", repairResults, "protected_loss"},
		{"purge result", purgeResults, "completed"},
		{"seed result", seedResults, "retained_unverifiable"},
		{"variant result", variantResults, "matched"},
		{"manifest operation", manifestOperations, "adoption_objects"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := boundedLabel(test.known, test.allowed); got != test.known {
				t.Fatalf("known label = %q", got)
			}
			if got := boundedLabel("catalog-id-or-path", test.allowed); got != labelUnknown {
				t.Fatalf("unbounded label = %q, want %q", got, labelUnknown)
			}
		})
	}
}

func TestArtworkMetricRecordingAcceptsZeroAndNilInputs(t *testing.T) {
	Materialization("", "")
	ObserveStore("", "", time.Time{}, nil)
	ObserveStore("local", "stat", time.Now(), errors.New("unavailable"))
	Delivery("", "")
	DeliveryBytes("", 0)
	Purge(false, "", 0, 0)
	Inventory(time.Time{}, 0, 0, 0)
	Seed("", 0)
	Variant("", 0)
	ManifestFailure("")
	TempFilesCleaned(0)
	SeedExpiredBytes(0)
	StoreHealth("", "", "")
	Repair("")
	RepairPending(0, 0)
	ObserveDeliveryLatency("", time.Time{})
	StoreHealthDuration("", "", 0)
}

func TestInventorySnapshotAgeIsMeasuredAtScrapeTime(t *testing.T) {
	previous := inventorySnapshotUnixNano.Load()
	t.Cleanup(func() { inventorySnapshotUnixNano.Store(previous) })

	inventorySnapshotUnixNano.Store(0)
	if got := gaugeValue(t, inventoryAge); got != 0 {
		t.Fatalf("age before any snapshot = %v, want 0", got)
	}

	Inventory(time.Now().Add(-90*time.Minute), 0, 0, 0)
	if got := gaugeValue(t, inventoryAge); got < (90 * time.Minute).Seconds() {
		t.Fatalf("age after a 90m-old snapshot = %v, want >= %v", got, (90 * time.Minute).Seconds())
	}

	// A refresh that produced no snapshot must not reset the recorded time.
	Inventory(time.Time{}, 0, 0, 0)
	if got := gaugeValue(t, inventoryAge); got < (90 * time.Minute).Seconds() {
		t.Fatalf("age after a snapshotless refresh = %v, want >= %v", got, (90 * time.Minute).Seconds())
	}
}

func TestWrongMountDetectionCountsOnEntry(t *testing.T) {
	before := counterValue(t, wrongMounts.WithLabelValues("local"))

	StoreHealth("local", "healthy", "wrong_mount")
	if got := counterValue(t, wrongMounts.WithLabelValues("local")); got != before+1 {
		t.Fatalf("detections after entering wrong_mount = %v, want %v", got, before+1)
	}

	// Staying in the state, accounting its duration, and leaving it must not
	// add further detections.
	StoreHealth("local", "wrong_mount", "wrong_mount")
	StoreHealthDuration("local", "wrong_mount", time.Second)
	StoreHealth("local", "wrong_mount", "healthy")
	if got := counterValue(t, wrongMounts.WithLabelValues("local")); got != before+1 {
		t.Fatalf("detections after staying in and leaving wrong_mount = %v, want %v", got, before+1)
	}
}
