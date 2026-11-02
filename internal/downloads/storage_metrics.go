package downloads

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const labelLocation = "location"

// Storage metrics. Locations are "server" and "node:<id>", so the label set
// grows with the node count only.
var (
	storageFreedBytes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "silo_download_storage_freed_bytes_total",
		Help: "Prepared download bytes this process deleted, by location and reason; sum across instances.",
	}, []string{labelLocation, "reason"})
	storageBytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "silo_download_storage_bytes",
		Help: "Prepared download bytes by location: in_use and cached from Silo's records, on_disk and untracked from the files. Set by the replica that last ran storage maintenance; read with max and check silo_download_storage_sample_timestamp_seconds.",
	}, []string{labelLocation, "state"})
	storageSampleAt = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "silo_download_storage_sample_timestamp_seconds",
		Help: "When this process last set silo_download_storage_bytes for a location.",
	}, []string{labelLocation})
)

func recordStorageFreed(location, reason string, bytes int64) {
	if bytes > 0 {
		storageFreedBytes.WithLabelValues(location, reason).Add(float64(bytes))
	}
}

// recordStorageGauges publishes one location's totals from a maintenance pass.
func recordStorageGauges(loc storageLocation, totals locationTotals, untrackedBytes int64) {
	key := loc.key()
	storageBytes.WithLabelValues(key, "in_use").Set(float64(totals.inUseBytes))
	storageBytes.WithLabelValues(key, "cached").Set(float64(totals.cachedBytes))
	if loc.Usage != nil {
		storageBytes.WithLabelValues(key, "on_disk").Set(float64(loc.Usage.TotalBytes()))
	}
	storageBytes.WithLabelValues(key, "untracked").Set(float64(untrackedBytes))
	storageSampleAt.WithLabelValues(key).Set(float64(time.Now().Unix()))
}
