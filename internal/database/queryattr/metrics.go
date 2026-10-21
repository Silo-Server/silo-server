package queryattr

import "github.com/prometheus/client_golang/prometheus"

const siteLabel = "site"

var queriesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "silo_postgres_queries_total",
	Help: "PostgreSQL operations completed, by the calling Silo function and line.",
}, []string{siteLabel})
var querySecondsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "silo_postgres_query_seconds_total",
	Help: "Summed PostgreSQL operation time from start to end callback, by the calling Silo function and line; not wall time.",
}, []string{siteLabel})

// RegisterMetrics registers the two site counters. It panics on duplicate or invalid registration.
// Importing queryattr registers no collectors.
func RegisterMetrics(reg prometheus.Registerer) { reg.MustRegister(queriesTotal, querySecondsTotal) }
