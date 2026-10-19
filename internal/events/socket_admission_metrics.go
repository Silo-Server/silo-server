package events

import "github.com/prometheus/client_golang/prometheus"

const (
	clientSocketAccountReason = iota
	clientSocketProcessReason
	clientSocketDrainingReason
	clientSocketUnavailableReason
)

var (
	clientSocketOccupancyDesc = prometheus.NewDesc(
		"silo_events_client_socket_occupancy", "Pending and active client socket reservations in this process.", nil, nil)
	clientSocketHighWaterDesc = prometheus.NewDesc(
		"silo_events_client_socket_high_water", "Highest pending and active client socket occupancy in this process since startup.", nil, nil)
	clientSocketAccountOccupancyDesc = prometheus.NewDesc(
		"silo_events_client_socket_account_occupancy", "Account occupancy after each admitted client socket reservation, including pending reservations.", nil, nil)
	clientSocketMaxAccountHighWaterDesc = prometheus.NewDesc(
		"silo_events_client_socket_max_account_high_water", "Highest occupancy of any one account in this process since startup.", nil, nil)
	clientSocketWouldRefuseDesc = prometheus.NewDesc(
		"silo_events_client_socket_would_refuse_total", "Observation-mode reservations exceeding each account or process threshold; one reservation can increment both reasons.", []string{"reason"}, nil)
	clientSocketRefusalsDesc = prometheus.NewDesc(
		"silo_events_client_socket_refusals_total", "Refused client socket reservations by bounded reason.", []string{"reason"}, nil)
)

// All fields use ClientSocketAdmission.mu. No identities are retained here.
type clientSocketMetrics struct {
	highWater               int
	maxAccountHighWater     int
	accountOccupancyCount   uint64
	accountOccupancySum     float64
	accountOccupancyBuckets [8]uint64 // cumulative: 1, 2, 4, 8, 16, 32, 64, 128
	wouldRefuse             [2]uint64 // account, process
	refusals                [4]uint64 // account, process, draining, unavailable
}

func (m *clientSocketMetrics) observeAdmission(processOccupancy, accountOccupancy int) {
	m.highWater = max(m.highWater, processOccupancy)
	m.maxAccountHighWater = max(m.maxAccountHighWater, accountOccupancy)
	m.accountOccupancyCount++
	m.accountOccupancySum += float64(accountOccupancy)
	for i := range m.accountOccupancyBuckets {
		if accountOccupancy <= 1<<i {
			m.accountOccupancyBuckets[i]++
		}
	}
}

var _ prometheus.Collector = (*ClientSocketAdmission)(nil)

// Describe implements prometheus.Collector without registering globally.
func (*ClientSocketAdmission) Describe(ch chan<- *prometheus.Desc) {
	ch <- clientSocketOccupancyDesc
	ch <- clientSocketHighWaterDesc
	ch <- clientSocketAccountOccupancyDesc
	ch <- clientSocketMaxAccountHighWaterDesc
	ch <- clientSocketWouldRefuseDesc
	ch <- clientSocketRefusalsDesc
}

// Collect exports one consistent aggregate snapshot. Sending to the collector
// channel happens after unlocking, so a stalled scrape cannot hold admission.
func (a *ClientSocketAdmission) Collect(ch chan<- prometheus.Metric) {
	if a == nil {
		return
	}
	a.mu.Lock()
	total, metrics := a.total, a.metrics
	a.mu.Unlock()

	ch <- prometheus.MustNewConstMetric(clientSocketOccupancyDesc, prometheus.GaugeValue, float64(total))
	ch <- prometheus.MustNewConstMetric(clientSocketHighWaterDesc, prometheus.GaugeValue, float64(metrics.highWater))
	ch <- prometheus.MustNewConstMetric(clientSocketMaxAccountHighWaterDesc, prometheus.GaugeValue, float64(metrics.maxAccountHighWater))
	buckets := make(map[float64]uint64, len(metrics.accountOccupancyBuckets))
	for i, count := range metrics.accountOccupancyBuckets {
		buckets[float64(int(1)<<i)] = count
	}
	// Prometheus supplies the +Inf bucket from accountOccupancyCount.
	ch <- prometheus.MustNewConstHistogram(clientSocketAccountOccupancyDesc, metrics.accountOccupancyCount, metrics.accountOccupancySum, buckets)
	for i, reason := range [...]ClientSocketRefusal{ClientSocketAccountLimit, ClientSocketProcessLimit} {
		ch <- prometheus.MustNewConstMetric(clientSocketWouldRefuseDesc, prometheus.CounterValue, float64(metrics.wouldRefuse[i]), string(reason))
	}
	for i, reason := range [...]ClientSocketRefusal{ClientSocketAccountLimit, ClientSocketProcessLimit, ClientSocketDraining, ClientSocketUnavailable} {
		ch <- prometheus.MustNewConstMetric(clientSocketRefusalsDesc, prometheus.CounterValue, float64(metrics.refusals[i]), string(reason))
	}
}
