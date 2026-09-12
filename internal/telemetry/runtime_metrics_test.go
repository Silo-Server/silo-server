package telemetry

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

func TestRuntimeMetricsReplaceDefaultWithoutDuplicates(t *testing.T) {
	r := prometheus.NewRegistry()
	r.MustRegister(collectors.NewGoCollector())
	r.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	if err := configureRuntimeMetrics(r); err != nil {
		t.Fatal(err)
	}
	families, err := r.Gather()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]int{}
	for _, f := range families {
		names[f.GetName()]++
	}
	for _, name := range []string{"go_goroutines", "go_memstats_heap_alloc_bytes", "go_sched_latencies_seconds", "go_cpu_classes_total_cpu_seconds_total", "go_sync_mutex_wait_total_seconds_total"} {
		if names[name] != 1 {
			t.Errorf("%s registered %d times", name, names[name])
		}
	}
	for name, n := range names {
		if n != 1 {
			t.Errorf("duplicate metric %s", name)
		}
	}
}
