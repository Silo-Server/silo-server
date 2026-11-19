package jellycompat

const (
	compatRequestsMetric       = "silo_jellycompat_requests_total"
	compatDurationMetric       = "silo_jellycompat_request_duration_seconds"
	compatClientDurationMetric = "silo_jellycompat_client_request_duration_seconds"
)

// compatSample is one jellycompat request series: a counter's value or a
// histogram's sample count.
type compatSample struct {
	metric string
	labels map[string]string
	value  float64
}
