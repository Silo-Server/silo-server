package playback

import (
	"math"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/Silo-Server/silo-server/internal/telemetry"
)

// maxFirstFrameMs bounds the first_frame_ms values the histogram accepts. A
// larger value is a stalled or backgrounded start, not a startup time, and
// would only distort the sum; the raw value stays in playback_route_events.
const maxFirstFrameMs = 600_000
const bufferingActionNone = "none"
const summaryDeliveryUnknown = "unknown"

// firstFrameSeconds is the press-play-to-first-frame time clients report on
// their first_frame route event. The client label is the fixed family from
// telemetry.ClientLabel, so the series count is bounded by the bucket count.
var firstFrameSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "silo_playback_first_frame_seconds",
	Help:    "Client-reported time from pressing play to the first rendered frame, by client family.",
	Buckets: []float64{0.1, 0.25, 0.5, 1, 1.5, 2, 3, 5, 8, 13, 20, 30, 60},
}, []string{telemetry.ClientLabelName})

// ObserveStoredRouteEvent feeds the metrics derived from a route event. Call
// it only for a row the store has just inserted: a v2 retry reuses its
// event_id, inserts nothing, and must not be counted a second time.
func ObserveStoredRouteEvent(record RouteEventRecordV3) {
	if record.Event == RouteEventPlanSummaryV3 {
		observePlanSummary(record)
		return
	}
	if record.Event != RouteEventFirstFrameV3 {
		return
	}
	value, ok := record.Diagnostics["first_frame_ms"]
	if !ok {
		return
	}
	ms, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(ms) || ms < 0 || ms > maxFirstFrameMs {
		return
	}
	firstFrameSeconds.WithLabelValues(telemetry.ClientLabel(record.ClientName)).Observe(ms / 1000)
}

var stallRatio = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name: "silo_playback_stall_ratio", Help: "Client-reported stall time divided by played time for completed plans.", Buckets: []float64{0, .001, .005, .01, .02, .05, .1, .25, .5, 1, 2},
}, []string{telemetry.ClientLabelName, "delivery"})
var bufferingActions = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_playback_buffering_actions_total", Help: "Last buffering action reported for a completed plan."}, []string{"mode", "action"})

func observePlanSummary(record RouteEventRecordV3) {
	mode, action := record.Diagnostics["buffering_mode"], record.Diagnostics["buffering_action"]
	switch mode {
	case "never", "ask", "always":
	default:
		return
	}
	switch action {
	case bufferingActionNone, "ignored", "lowered", "restored", "accepted", "kept", "timed_out", "cant_lower":
	default:
		return
	}
	bufferingActions.WithLabelValues(mode, action).Inc()
	played, e1 := strconv.ParseFloat(record.Diagnostics["played_ms"], 64)
	stalled, e2 := strconv.ParseFloat(record.Diagnostics["stall_total_ms"], 64)
	if e1 != nil || e2 != nil || math.IsNaN(played) || math.IsNaN(stalled) || played <= 0 || stalled < 0 || played > 7*24*60*60*1000 || stalled > 7*24*60*60*1000 {
		return
	}
	delivery := record.Delivery
	switch DeliveryV3(delivery) {
	case DeliveryOriginalHTTPV3, DeliveryRemuxProgressiveV3, DeliveryRemuxHLSV3, DeliveryTranscodeHLSV3:
	default:
		delivery = summaryDeliveryUnknown
	}
	stallRatio.WithLabelValues(telemetry.ClientLabel(record.ClientName), delivery).Observe(stalled / played)
}
