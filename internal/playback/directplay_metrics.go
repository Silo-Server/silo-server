package playback

import (
	"net/http"

	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	directStreamActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "silo_direct_stream_active",
		Help: "Number of original-file direct streams currently being served.",
	})
	directStreamEnds = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "silo_direct_stream_ends_total",
		Help: "Number of original-file direct streams by terminal outcome.",
	}, []string{"outcome"})
	directStreamRangeResumes = promauto.NewCounter(prometheus.CounterOpts{
		Name: "silo_direct_stream_range_resumes_total",
		Help: "Number of successful original-file byte-range resumes after byte zero.",
	})
	directStreamInvalidRanges = promauto.NewCounter(prometheus.CounterOpts{
		Name: "silo_direct_stream_invalid_range_total",
		Help: "Number of original-file direct stream requests rejected with HTTP 416.",
	})
	directStreamBytesSent = promauto.NewCounter(prometheus.CounterOpts{
		Name: "silo_direct_stream_bytes_sent_total",
		Help: "Number of original-file direct stream response body bytes sent.",
	})
)

func recordDirectStreamEnd(outcome httpstream.StreamOutcome, status int, bytesSent, rangeStart int64) {
	directStreamActive.Dec()
	directStreamEnds.WithLabelValues(string(outcome)).Inc()
	directStreamBytesSent.Add(float64(bytesSent))
	if status == http.StatusPartialContent && rangeStart > 0 {
		directStreamRangeResumes.Inc()
	}
	if status == http.StatusRequestedRangeNotSatisfiable {
		directStreamInvalidRanges.Inc()
	}
}
