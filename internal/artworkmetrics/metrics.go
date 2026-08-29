package artworkmetrics

import (
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	labelOperation = "operation"
	labelOutcome   = "outcome"
	labelUnknown   = "unknown"
	labelBackend   = "backend"
	labelRoute     = "route"
	labelKind      = "kind"
)

var (
	materializationSources = labelSet("provider", "plugin", "library_sidecar", "embedded", "generated", "upload", "bundled", "seed", labelUnknown)
	materializationResults = labelSet("materialized", "adopted", "failed", labelUnknown)
	storeBackends          = labelSet("local", "s3", labelUnknown)
	storeOperations        = labelSet("write", "open", "stat", "matches", "delete", "probe", "list", "maintenance_delete", labelUnknown)
	deliveryRoutes         = labelSet("store", "source_fallback", "placeholder", "direct", "direct_library", labelUnknown)
	deliveryResults        = labelSet("served", "conditional_hit", "invalid_signature", "expired_signature", "miss", "emergency_cache_hit", "singleflight_join", labelUnknown)
	storeHealthStates      = labelSet("healthy", "degraded", "unavailable", "empty_rebuilding", "wrong_mount", labelUnknown)
	repairResults          = labelSet("missing", "queued", "repairing", "recovered", "protected_loss", "throttled", labelUnknown)
	purgeResults           = labelSet("completed", "failed", "canceled", labelUnknown)
	seedResults            = labelSet("imported", "adopted", "skipped", "retained_unverifiable", "expired", labelUnknown)
	variantResults         = labelSet("written", "matched", labelUnknown)
	manifestOperations     = labelSet("adoption_index", "adoption_manifest_digest", "adoption_manifest", "adoption_objects", labelUnknown)
)

func labelSet(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func boundedLabel(value string, allowed map[string]struct{}) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if _, ok := allowed[value]; ok {
		return value
	}
	return labelUnknown
}

var (
	materializations = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_artwork_materializations_total", Help: "Artwork materialization outcomes."}, []string{"source_class", labelOutcome})
	storeDuration    = promauto.NewHistogramVec(prometheus.HistogramOpts{Name: "silo_artwork_store_operation_duration_seconds", Help: "Artwork store operation latency.", Buckets: prometheus.DefBuckets}, []string{labelBackend, labelOperation})
	storeFailures    = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_artwork_store_operation_failures_total", Help: "Artwork store operation failures."}, []string{labelBackend, labelOperation})
	delivery         = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_artwork_delivery_requests_total", Help: "Artwork delivery requests by bounded outcome."}, []string{labelRoute, labelOutcome})
	purgeJobs        = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_artwork_purge_jobs_total", Help: "Artwork purge job outcomes."}, []string{"dry_run", labelOutcome})
	purgeBytes       = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_artwork_purge_bytes_total", Help: "Bytes reported by artwork purge plans and jobs."}, []string{labelKind})
	inventoryAge     = promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "silo_artwork_inventory_snapshot_age_seconds", Help: "Age of the latest artwork inventory snapshot."}, inventorySnapshotAgeSeconds)
	inventoryDrift   = promauto.NewGaugeVec(prometheus.GaugeOpts{Name: "silo_artwork_inventory_drift_objects", Help: "Artwork inventory drift counters."}, []string{"kind"})
	seedEvents       = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_artwork_seed_events_total", Help: "Portable artwork seed import, adoption, and expiry events."}, []string{labelOutcome})
	variantBytes     = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_artwork_variant_bytes_total", Help: "Artwork variant bytes written or matched."}, []string{labelOutcome})
	deliveryBytes    = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_artwork_delivery_bytes_total", Help: "Artwork bytes served by delivery route."}, []string{"route"})
	manifestErrors   = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_artwork_manifest_validation_failures_total", Help: "Portable manifest validation failures."}, []string{"operation"})
	tempCleaned      = promauto.NewCounter(prometheus.CounterOpts{Name: "silo_artwork_abandoned_temp_files_cleaned_total", Help: "Abandoned artwork temporary files removed."})
	seedExpired      = promauto.NewGauge(prometheus.GaugeOpts{Name: "silo_artwork_seed_expired_bytes", Help: "Expired unused portable seed bytes."})
	storeHealth      = promauto.NewGaugeVec(prometheus.GaugeOpts{Name: "silo_artwork_store_health", Help: "Current artwork store health state (one active state per backend)."}, []string{labelBackend, "state"})
	storeTransitions = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_artwork_store_health_transitions_total", Help: "Debounced artwork store health transitions."}, []string{labelBackend, "from", "to"})
	repairEvents     = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_artwork_repair_events_total", Help: "Artwork loss detection and repair coordinator events."}, []string{labelOutcome})
	repairPending    = promauto.NewGaugeVec(prometheus.GaugeOpts{Name: "silo_artwork_repair_pending", Help: "Artwork revisions awaiting repair by bounded class."}, []string{"kind"})
	deliveryLatency  = promauto.NewHistogramVec(prometheus.HistogramOpts{Name: "silo_artwork_delivery_time_to_first_verified_byte_seconds", Help: "Time from artwork request start to a verified stored, fallback, or placeholder response.", Buckets: prometheus.DefBuckets}, []string{"route"})
	healthStateTime  = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_artwork_store_health_state_seconds_total", Help: "Time accumulated in artwork store operational states."}, []string{"backend", "state"})
	wrongMounts      = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_artwork_store_wrong_mount_detections_total", Help: "Artwork shared-mount sentinel failures."}, []string{"backend"})
)

func Materialization(sourceClass, outcome string) {
	materializations.WithLabelValues(
		boundedLabel(sourceClass, materializationSources),
		boundedLabel(outcome, materializationResults),
	).Inc()
}

func ObserveStore(backend, operation string, started time.Time, err error) {
	if started.IsZero() {
		started = time.Now()
	}
	backend = boundedLabel(backend, storeBackends)
	operation = boundedLabel(operation, storeOperations)
	storeDuration.WithLabelValues(backend, operation).Observe(time.Since(started).Seconds())
	if err != nil {
		storeFailures.WithLabelValues(backend, operation).Inc()
	}
}

func Delivery(route, outcome string) {
	delivery.WithLabelValues(boundedLabel(route, deliveryRoutes), boundedLabel(outcome, deliveryResults)).Inc()
}

func Purge(dryRun bool, outcome string, pending, reclaimable int64) {
	purgeJobs.WithLabelValues(map[bool]string{true: "true", false: "false"}[dryRun], boundedLabel(outcome, purgeResults)).Inc()
	if pending > 0 {
		purgeBytes.WithLabelValues("pending").Add(float64(pending))
	}
	if reclaimable > 0 {
		purgeBytes.WithLabelValues("reclaimable").Add(float64(reclaimable))
	}
}

// inventorySnapshotUnixNano holds the wall-clock time of the most recent
// artwork inventory snapshot, or zero when none has been taken yet.
var inventorySnapshotUnixNano atomic.Int64

// inventorySnapshotAgeSeconds is evaluated at scrape time rather than at
// refresh time: the age of a snapshot grows while no refresh runs, which is
// exactly the condition staleness alerts have to fire on. Reporting it once per
// successful refresh would pin the gauge near zero forever.
func inventorySnapshotAgeSeconds() float64 {
	nanos := inventorySnapshotUnixNano.Load()
	if nanos == 0 {
		return 0
	}
	return time.Since(time.Unix(0, nanos)).Seconds()
}

func Inventory(snapshot time.Time, missingRevisions, missingObjects, orphans int64) {
	if !snapshot.IsZero() {
		inventorySnapshotUnixNano.Store(snapshot.UnixNano())
	}
	inventoryDrift.WithLabelValues("missing_revisions").Set(float64(missingRevisions))
	inventoryDrift.WithLabelValues("missing_objects").Set(float64(missingObjects))
	inventoryDrift.WithLabelValues("orphan_objects").Set(float64(orphans))
}

func Seed(outcome string, count int64) {
	if count > 0 {
		seedEvents.WithLabelValues(boundedLabel(outcome, seedResults)).Add(float64(count))
	}
}

func Variant(outcome string, bytes int64) {
	if bytes > 0 {
		variantBytes.WithLabelValues(boundedLabel(outcome, variantResults)).Add(float64(bytes))
	}
}

func DeliveryBytes(route string, bytes int64) {
	if bytes > 0 {
		deliveryBytes.WithLabelValues(boundedLabel(route, deliveryRoutes)).Add(float64(bytes))
	}
}

func ManifestFailure(operation string) {
	manifestErrors.WithLabelValues(boundedLabel(operation, manifestOperations)).Inc()
}
func TempFilesCleaned(count int) {
	if count > 0 {
		tempCleaned.Add(float64(count))
	}
}
func SeedExpiredBytes(bytes int64) { seedExpired.Set(float64(bytes)) }

func StoreHealth(backend, from, to string) {
	backend = boundedLabel(backend, storeBackends)
	from = boundedLabel(from, storeHealthStates)
	to = boundedLabel(to, storeHealthStates)
	for state := range storeHealthStates {
		if state != labelUnknown {
			storeHealth.WithLabelValues(backend, state).Set(0)
		}
	}
	storeHealth.WithLabelValues(backend, to).Set(1)
	if from != to {
		storeTransitions.WithLabelValues(backend, from, to).Inc()
		// Count the detection when the store enters wrong_mount. Counting on
		// exit would leave an ongoing mount-identity failure invisible for as
		// long as it lasts, which is when operators most need to see it.
		if to == "wrong_mount" {
			wrongMounts.WithLabelValues(backend).Inc()
		}
	}
}

func Repair(outcome string) {
	repairEvents.WithLabelValues(boundedLabel(outcome, repairResults)).Inc()
}

func RepairPending(repairing, protected int64) {
	repairPending.WithLabelValues("repairing").Set(float64(repairing))
	repairPending.WithLabelValues("protected_loss").Set(float64(protected))
}

func ObserveDeliveryLatency(route string, started time.Time) {
	if started.IsZero() {
		started = time.Now()
	}
	deliveryLatency.WithLabelValues(boundedLabel(route, deliveryRoutes)).Observe(time.Since(started).Seconds())
}

func StoreHealthDuration(backend, state string, duration time.Duration) {
	backend = boundedLabel(backend, storeBackends)
	state = boundedLabel(state, storeHealthStates)
	if duration > 0 {
		healthStateTime.WithLabelValues(backend, state).Add(duration.Seconds())
	}
}
