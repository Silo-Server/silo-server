package buildinfo

import (
	"runtime"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// One build identity series per process; request metrics never carry revisions.
func init() {
	info := Current()
	revision := info.Revision
	if revision == "" {
		revision = unavailableDisplay
	}
	metric := prometheus.NewGauge(prometheus.GaugeOpts{Name: "silo_build_info", Help: "Running binary identity; revision is unavailable when build metadata was stripped.", ConstLabels: prometheus.Labels{"revision": revision, "dirty": strconv.FormatBool(info.Dirty), "go_version": runtime.Version()}})
	metric.Set(1)
	prometheus.MustRegister(metric)
}
