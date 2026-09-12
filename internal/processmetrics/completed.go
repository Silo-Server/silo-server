// Package processmetrics accounts for completed child processes at their
// existing owner's Wait boundary. It never starts, waits for, or reaps a child.
package processmetrics

import (
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Workload is a finite resource ownership category; never use paths, media
// identities, installation IDs, command arguments, or binary names as labels.
type Workload uint8

const (
	Transcode Workload = iota
	Remux
	Probe
	Plugin
)

func (w Workload) label() string {
	switch w {
	case Transcode:
		return "transcode"
	case Remux:
		return "remux"
	case Probe:
		return "probe"
	case Plugin:
		return "plugin"
	default:
		return "other"
	}
}

const workloadLabel = "workload"

var (
	exits   = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_subprocess_exits_total", Help: "Observed child process completions or failures to start, by workload and bounded outcome."}, []string{workloadLabel, "outcome"})
	cpu     = promauto.NewCounterVec(prometheus.CounterOpts{Name: "silo_subprocess_cpu_seconds_total", Help: "CPU seconds reported by completed child processes at their owner's Wait boundary; excludes currently running processes."}, []string{workloadLabel, "mode"})
	peakRSS = promauto.NewHistogramVec(prometheus.HistogramOpts{Name: "silo_subprocess_peak_rss_bytes", Help: "Distribution of maximum resident bytes reported by individual completed child processes. Peaks are not current memory and must not be summed as concurrent usage.", Buckets: prometheus.ExponentialBuckets(1<<20, 4, 8)}, []string{workloadLabel})
)

// Record consumes a completed process state exactly once, after the existing
// owner has called Wait/Run/Output. A nil state records a failure to start and
// never invents CPU or memory measurements. The context error distinguishes
// owner cancellation from an unsuccessful exit.
func Record(workload Workload, state *os.ProcessState, waitErr, contextErr error) {
	label := workload.label()
	outcome := "success"
	switch {
	case state == nil:
		outcome = "start_error"
	case waitErr != nil || !state.Success():
		outcome = "error"
		if contextErr != nil {
			outcome = "canceled"
		}
	}
	exits.WithLabelValues(label, outcome).Inc()
	if state == nil {
		return
	}
	cpu.WithLabelValues(label, "user").Add(state.UserTime().Seconds())
	cpu.WithLabelValues(label, "system").Add(state.SystemTime().Seconds())
	if rss, ok := maximumRSS(state); ok {
		peakRSS.WithLabelValues(label).Observe(rss)
	}
}
