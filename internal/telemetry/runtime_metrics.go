package telemetry

import (
	"fmt"
	"regexp"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

var runtimeSetup struct {
	sync.Once
	err error
}

// ConfigureRuntimeMetrics replaces the default Go collector once before any
// listener starts. Existing go_memstats/process metrics keep their semantics;
// additional runtime families cover GC, memory classes, scheduler latency,
// CPU classes and mutex wait. This does not create an OTel MeterProvider.
func ConfigureRuntimeMetrics() error {
	runtimeSetup.Do(func() { runtimeSetup.err = configureRuntimeMetrics(prometheus.DefaultRegisterer) })
	return runtimeSetup.err
}

func configureRuntimeMetrics(registerer prometheus.Registerer) error {
	previous := collectors.NewGoCollector()
	if !registerer.Unregister(previous) {
		return fmt.Errorf("default Go metrics collector was not registered")
	}
	collector := collectors.NewGoCollector(collectors.WithGoCollectorRuntimeMetrics(collectors.GoRuntimeMetricsRule{
		Matcher: regexp.MustCompile(`^/(gc|memory/classes|sched|cpu/classes|sync)/`),
	}))
	if err := registerer.Register(collector); err != nil {
		// Preserve the baseline when a preexisting conflicting collector prevents
		// expansion. Startup can report this optional observability failure.
		_ = registerer.Register(previous)
		return fmt.Errorf("register expanded Go metrics: %w", err)
	}
	return nil
}
