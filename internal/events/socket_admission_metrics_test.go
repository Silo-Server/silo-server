package events

import (
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func TestClientSocketAdmissionObservationMetrics(t *testing.T) {
	a := newTestClientSocketAdmission(t, 1, 2, ClientSocketObserve)
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(a)
	reservations := []*ClientSocketReservation{
		reserveTestClientSocket(t, a, 7),
		reserveTestClientSocket(t, a, 7), // account threshold only
		reserveTestClientSocket(t, a, 7), // both thresholds, both must be counted
		reserveTestClientSocket(t, a, 8), // process threshold only
	}
	if got := a.Occupancy(); got != 4 {
		t.Fatalf("observation occupancy = %d, want 4 (no quota enforcement)", got)
	}
	assertClientSocketRefusal(t, a, 0, false, ClientSocketUnavailable)
	assertClientSocketRefusal(t, a, 7, true, ClientSocketDraining)
	a.BeginDrain()
	assertClientSocketRefusal(t, a, 7, false, ClientSocketDraining)
	// Compare every family and label, so any account/profile/device/credential
	// labels, draining would-refuse series, or other extra series fail the test.
	want := `
# HELP silo_events_client_socket_occupancy Pending and active client socket reservations in this process.
# TYPE silo_events_client_socket_occupancy gauge
silo_events_client_socket_occupancy 4
# HELP silo_events_client_socket_high_water Highest pending and active client socket occupancy in this process since startup.
# TYPE silo_events_client_socket_high_water gauge
silo_events_client_socket_high_water 4
# HELP silo_events_client_socket_max_account_high_water Highest occupancy of any one account in this process since startup.
# TYPE silo_events_client_socket_max_account_high_water gauge
silo_events_client_socket_max_account_high_water 3
# HELP silo_events_client_socket_account_occupancy Account occupancy after each admitted client socket reservation, including pending reservations.
# TYPE silo_events_client_socket_account_occupancy histogram
silo_events_client_socket_account_occupancy_bucket{le="1"} 2
silo_events_client_socket_account_occupancy_bucket{le="2"} 3
silo_events_client_socket_account_occupancy_bucket{le="4"} 4
silo_events_client_socket_account_occupancy_bucket{le="8"} 4
silo_events_client_socket_account_occupancy_bucket{le="16"} 4
silo_events_client_socket_account_occupancy_bucket{le="32"} 4
silo_events_client_socket_account_occupancy_bucket{le="64"} 4
silo_events_client_socket_account_occupancy_bucket{le="128"} 4
silo_events_client_socket_account_occupancy_bucket{le="+Inf"} 4
silo_events_client_socket_account_occupancy_sum 7
silo_events_client_socket_account_occupancy_count 4
# HELP silo_events_client_socket_would_refuse_total Observation-mode reservations exceeding each account or process threshold; one reservation can increment both reasons.
# TYPE silo_events_client_socket_would_refuse_total counter
silo_events_client_socket_would_refuse_total{reason="account"} 2
silo_events_client_socket_would_refuse_total{reason="process"} 2
# HELP silo_events_client_socket_refusals_total Refused client socket reservations by bounded reason.
# TYPE silo_events_client_socket_refusals_total counter
silo_events_client_socket_refusals_total{reason="account"} 0
silo_events_client_socket_refusals_total{reason="process"} 0
silo_events_client_socket_refusals_total{reason="draining"} 2
silo_events_client_socket_refusals_total{reason="unavailable"} 1
`
	if err := testutil.GatherAndCompare(registry, strings.NewReader(want)); err != nil {
		t.Fatal(err)
	}
	for _, reservation := range reservations {
		reservation.Release()
		reservation.Release()
	}
	// Release changes only current occupancy, retaining every historical metric.
	want = strings.Replace(want, "silo_events_client_socket_occupancy 4\n", "silo_events_client_socket_occupancy 0\n", 1)
	if err := testutil.GatherAndCompare(registry, strings.NewReader(want)); err != nil {
		t.Fatal(err)
	}
	assertClientSocketDone(t, a, true)
}

func TestClientSocketAdmissionEnforcementRefusalMetrics(t *testing.T) {
	a := newTestClientSocketAdmission(t, 1, 2, ClientSocketEnforce)
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(a)
	reserveTestClientSocket(t, a, 7)
	assertClientSocketRefusal(t, a, 7, false, ClientSocketAccountLimit)
	reserveTestClientSocket(t, a, 8)
	assertClientSocketRefusal(t, a, 7, false, ClientSocketProcessLimit)
	assertClientSocketRefusal(t, a, 9, false, ClientSocketProcessLimit)
	assertClientSocketRefusal(t, a, 0, false, ClientSocketUnavailable)
	assertClientSocketRefusal(t, a, 7, true, ClientSocketDraining)
	if err := testutil.GatherAndCompare(registry, strings.NewReader(`
# HELP silo_events_client_socket_would_refuse_total Observation-mode reservations exceeding each account or process threshold; one reservation can increment both reasons.
# TYPE silo_events_client_socket_would_refuse_total counter
silo_events_client_socket_would_refuse_total{reason="account"} 0
silo_events_client_socket_would_refuse_total{reason="process"} 0
# HELP silo_events_client_socket_refusals_total Refused client socket reservations by bounded reason.
# TYPE silo_events_client_socket_refusals_total counter
silo_events_client_socket_refusals_total{reason="account"} 1
silo_events_client_socket_refusals_total{reason="process"} 2
silo_events_client_socket_refusals_total{reason="draining"} 1
silo_events_client_socket_refusals_total{reason="unavailable"} 1
`), "silo_events_client_socket_would_refuse_total", "silo_events_client_socket_refusals_total"); err != nil {
		t.Fatal(err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "silo_events_client_socket_account_occupancy" {
			histogram := family.GetMetric()[0].GetHistogram()
			if histogram.GetSampleCount() != 2 || histogram.GetSampleSum() != 2 {
				t.Fatal("refusals changed admitted occupancy observations")
			}
			return
		}
	}
	t.Fatal("account occupancy histogram missing")
}

func TestClientSocketMetricsAccountOccupancyOverflow(t *testing.T) {
	// Feed ten finite aggregate samples directly to the metric helper to cover
	// all bucket edges and overflow without allocating large reservations.
	var metrics clientSocketMetrics
	for _, occupancy := range []int{1, 2, 3, 4, 8, 16, 32, 64, 128, 129} {
		metrics.observeAdmission(1, occupancy)
	}
	if metrics.accountOccupancyBuckets != [8]uint64{1, 2, 4, 5, 6, 7, 8, 9} ||
		metrics.accountOccupancyCount != 10 || metrics.accountOccupancySum != 387 ||
		metrics.maxAccountHighWater != 129 {
		t.Fatal("account histogram lost a bucket boundary or overflow sample")
	}
}

func TestClientSocketAdmissionCollectorIsInstanceLocal(t *testing.T) {
	a := newTestClientSocketAdmission(t, 1, 1, ClientSocketEnforce)
	b := newTestClientSocketAdmission(t, 1, 1, ClientSocketEnforce)
	reserveTestClientSocket(t, a, 7)
	aRegistry := prometheus.NewPedanticRegistry()
	bRegistry := prometheus.NewPedanticRegistry()
	aRegistry.MustRegister(a)
	bRegistry.MustRegister(b)
	for _, tc := range []struct {
		registry *prometheus.Registry
		want     string
	}{
		{aRegistry, "1"},
		{bRegistry, "0"},
	} {
		if err := testutil.GatherAndCompare(tc.registry, strings.NewReader(`
# HELP silo_events_client_socket_occupancy Pending and active client socket reservations in this process.
# TYPE silo_events_client_socket_occupancy gauge
silo_events_client_socket_occupancy `+tc.want+`
# HELP silo_events_client_socket_high_water Highest pending and active client socket occupancy in this process since startup.
# TYPE silo_events_client_socket_high_water gauge
silo_events_client_socket_high_water `+tc.want+`
`), "silo_events_client_socket_occupancy", "silo_events_client_socket_high_water"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClientSocketAdmissionNilCollector(t *testing.T) {
	var a *ClientSocketAdmission
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(a)
	if err := testutil.GatherAndCompare(registry, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
}

func TestClientSocketAdmissionCollectorSnapshotsBeforeSending(t *testing.T) {
	a := newTestClientSocketAdmission(t, 2, 2, ClientSocketEnforce)
	first := reserveTestClientSocket(t, a, 7)
	metrics := make(chan prometheus.Metric)
	var wg sync.WaitGroup
	wg.Go(func() {
		a.Collect(metrics)
		close(metrics)
	})
	// Receiving the first sample observes snapshot completion. Leave Collect
	// blocked on its next send while changing admission; it must not hold mu.
	firstMetric := <-metrics
	reserveTestClientSocket(t, a, 8)
	first.Release()
	snapshot := []prometheus.Metric{firstMetric}
	for metric := range metrics {
		snapshot = append(snapshot, metric)
	}
	wg.Wait()
	for _, metric := range snapshot {
		var sample dto.Metric
		if err := metric.Write(&sample); err != nil {
			t.Fatal(err)
		}
		switch metric.Desc() {
		case clientSocketOccupancyDesc, clientSocketHighWaterDesc:
			if sample.GetGauge().GetValue() != 1 {
				t.Error("scrape mixed occupancy from after its snapshot")
			}
		case clientSocketAccountOccupancyDesc:
			if sample.GetHistogram().GetSampleCount() != 1 {
				t.Error("scrape mixed histogram from after its snapshot")
			}
		}
	}
}
