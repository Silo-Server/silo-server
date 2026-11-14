package playback

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Subtitle serve-path metrics (measurement only).
//
// A subtitle response can be produced by four materially different paths with
// very different costs: a committed full-track artifact (a disk read), the
// in-memory last-good window (a copy), an in-progress ASS warm's retained
// prefix (a render), and a source extract (an ffmpeg demux, possibly of a slow
// relay). These counters are incremented at the exact return points of the
// existing paths and change nothing about which path a request takes. They
// exist so the multi-second subtitle cost can be attributed per class before
// any behavior changes; the admission and probe counters attribute the
// remaining time to contention and validation.
//
// Every counter here is a pure observation: no request reads one back, no
// response shape depends on one, and no scheduling decision uses one.

const (
	// subtitleServeArtifact is a whole-track response served directly from the
	// committed cache artifact.
	subtitleServeArtifact = "artifact"
	// subtitleServePGSSupArtifact is the PGS variant of subtitleServeArtifact.
	subtitleServePGSSupArtifact = "pgs_artifact"
	// subtitleServeMemoryWindow is a bounded window answered from the last-good
	// in-memory window cache (including a window first rendered by a warm
	// prefix and then stored).
	subtitleServeMemoryWindow = "memory_window"
	// subtitleServeWarmPrefix is a bounded ASS window rendered from an
	// in-progress full-track warm's retained prefix.
	subtitleServeWarmPrefix = "warm_prefix"
	// subtitleServeSourceExtract is a response or extraction attempt produced
	// by demuxing the source (or a windowed extract over the committed
	// artifact), including a fill watched to completion.
	subtitleServeSourceExtract = "source_extract"
	// subtitleServePGSSupSource is the PGS variant of subtitleServeSourceExtract.
	subtitleServePGSSupSource = "pgs_source"
	// subtitleServePGSProgressive is a bounded PGS window answered from the
	// running full-track fill's artifact, behind its coverage watermark.
	subtitleServePGSProgressive = "pgs_progressive"
)

const (
	// subtitleProbeRan is an ffprobe that actually spawned.
	subtitleProbeRan = "ran"
	// subtitleProbeReused is a layout verdict served from the request-path memo
	// instead of spawning ffprobe again.
	subtitleProbeReused = "reused"
)

const (
	subtitleMetricLabelSource = "source"
	subtitleMetricLabelResult = "result"
	subtitleMetricLabelFormat = "format"
	subtitleMetricLabelClass  = "class"
)

var subtitleServeSource = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "vio_subtitle_serve_source_total",
	Help: "Subtitle responses (or extraction attempts) by the serve path that produced them. Measurement only.",
}, []string{subtitleMetricLabelSource})

var subtitleLayoutProbes = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "vio_subtitle_layout_probe_total",
	Help: "Subtitle layout validation outcomes: ffprobe runs and memoized verdict reuses. Measurement only.",
}, []string{subtitleMetricLabelResult})

var subtitleRelayAdmissions = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "vio_subtitle_relay_admission_total",
	Help: "Relay subtitle demux admission outcomes by class and relay source. Includes layout-probe admissions, which draw from the same per-source budgets. Measurement only.",
}, []string{subtitleMetricLabelClass, subtitleMetricLabelResult, subtitleMetricLabelSource})

var subtitleRelayAdmissionWait = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "vio_subtitle_relay_admission_wait_seconds",
	Help:    "Time a request-path or request-triggered font subtitle read waited for a relay demux slot. Measurement only.",
	Buckets: []float64{0, 0.001, 0.01, 0.05, 0.1, 0.25, 0.5, 0.75, 1, 2, 5},
}, []string{subtitleMetricLabelClass})

var subtitleWarmPageOverlaps = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "vio_subtitle_warm_page_overlap_total",
	Help: "Page subtitle extracts that ran while a background warm for the same identity was in flight. Measurement only.",
}, []string{subtitleMetricLabelFormat})

var subtitleWarmPageOverlapSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name: "vio_subtitle_warm_page_overlap_seconds",
	Help: "Duration of a page subtitle extract that overlapped an in-flight background warm for the same identity. Measurement only.",
	// A window extract on a warm relay is the multi-second case under
	// investigation; the long buckets keep that tail visible.
	Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 4, 8, 15, 30, 60},
}, []string{subtitleMetricLabelFormat})

var subtitlePageCostRejections = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "vio_subtitle_page_read_budget_total",
	Help: "Windowed bitmap page extracts killed for reading more of the source container than a page may cost, by output format. Measurement only.",
}, []string{subtitleMetricLabelFormat})

// recordSubtitleServeSource attributes one response or extraction attempt to
// the path that produced it. Call it at the point the path is chosen, so a
// later write error still counts the cost that was already paid.
func recordSubtitleServeSource(source string) {
	subtitleServeSource.WithLabelValues(source).Inc()
}

// recordSubtitleServeSourceFor is recordSubtitleServeSource with the PGS
// variants selected by the source codec.
func recordSubtitleServeSourceFor(source string, pgs bool) {
	if pgs {
		switch source {
		case subtitleServeArtifact:
			source = subtitleServePGSSupArtifact
		case subtitleServeSourceExtract:
			source = subtitleServePGSSupSource
		}
	}
	recordSubtitleServeSource(source)
}

// recordSubtitleLayoutProbeRan counts one ffprobe spawn.
func recordSubtitleLayoutProbeRan() {
	subtitleLayoutProbes.WithLabelValues(subtitleProbeRan).Inc()
}

// ObserveSubtitleLayoutProbeReused counts a layout verdict served from the
// request-path memo instead of a fresh ffprobe. The memo lives in the API
// handler; this hook keeps the metric next to the probe it describes.
func ObserveSubtitleLayoutProbeReused() {
	subtitleLayoutProbes.WithLabelValues(subtitleProbeReused).Inc()
}

// recordSubtitleRelayAdmission counts one admission decision for a remote
// relay read. Local reads are not admitted and are not counted.
func recordSubtitleRelayAdmission(class, source string, admitted bool) {
	result := "shed"
	if admitted {
		result = "admitted"
	}
	subtitleRelayAdmissions.WithLabelValues(class, result, source).Inc()
}

// observeSubtitleRelayAdmissionWait records how long a waiting class spent
// before its slot was granted or shed.
func observeSubtitleRelayAdmissionWait(class string, waited time.Duration) {
	subtitleRelayAdmissionWait.WithLabelValues(class).Observe(waited.Seconds())
}

// recordSubtitleWarmPageOverlap counts and times a page extract that ran while
// a background warm for the same identity was filling. The overlap is the cost
// signal: the two demuxes share one relay pipe.
func recordSubtitleWarmPageOverlap(format string, elapsed time.Duration) {
	subtitleWarmPageOverlaps.WithLabelValues(format).Inc()
	subtitleWarmPageOverlapSeconds.WithLabelValues(format).Observe(elapsed.Seconds())
}

// recordSubtitlePageTooCostly counts one windowed bitmap page extract killed for
// reading more of the source container than a page may cost.
func recordSubtitlePageTooCostly() {
	subtitlePageCostRejections.WithLabelValues(subtitleFormatSUP).Inc()
}

// recordSubtitlePageCostRefused counts one windowed bitmap page refused before
// its extract because the container prefix it would read was priced beyond the
// page budget. It shares the rejection counter with the runtime read cap: both
// answer the same way, and the split that matters is pages refused versus pages
// served from the artifact.
func recordSubtitlePageCostRefused() {
	subtitlePageCostRejections.WithLabelValues(subtitleFormatSUP).Inc()
}

// recordSubtitlePageTimeout counts one windowed bitmap page that ran out the
// window extract deadline before emitting a byte. It shares the rejection
// counter with the read-budget kills: the page was not served, and the reason a
// reader needs is that a page was refused rather than which guard fired.
func recordSubtitlePageTimeout() {
	subtitlePageCostRejections.WithLabelValues(subtitleFormatSUP).Inc()
}
