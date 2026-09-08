package literaryworks

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type matchOriginKey struct{}

// WithMatchOrigin attributes matching work to a bounded set of callers. Unknown
// origins are folded into "other" so labels cannot grow with request data.
func WithMatchOrigin(ctx context.Context, origin string) context.Context {
	switch origin {
	case "scanner", "ebook_enrichment", "audiobook_enrichment":
	default:
		origin = "other"
	}
	return context.WithValue(ctx, matchOriginKey{}, origin)
}

func matchOrigin(ctx context.Context) string {
	if origin, ok := ctx.Value(matchOriginKey{}).(string); ok {
		return origin
	}
	return "other"
}

var (
	candidateDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "silo_literary_match_candidate_duration_seconds",
		Help:    "Time to select literary-work candidate IDs, including pool wait and row reads.",
		Buckets: []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"origin", "outcome"})
	candidateCount = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "silo_literary_match_candidates",
		Help:    "Candidate IDs returned by successful literary-work lookups.",
		Buckets: []float64{0, 1, 5, 20, 50, 100, 250, 500},
	}, []string{"origin"})
	autoMatchActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "silo_literary_match_active",
		Help: "Automatic literary-work matching calls currently running on this replica.",
	}, []string{"origin"})
	autoMatchTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "silo_literary_match_attempts_total",
		Help: "Completed automatic literary-work matching calls by caller and outcome.",
	}, []string{"origin", "outcome"})
)

func observeCandidateQuery(ctx context.Context, started time.Time, count int, err error) {
	origin := matchOrigin(ctx)
	outcome := "success"
	if err != nil {
		outcome = "error"
	} else {
		candidateCount.WithLabelValues(origin).Observe(float64(count))
	}
	candidateDuration.WithLabelValues(origin, outcome).Observe(time.Since(started).Seconds())
}
