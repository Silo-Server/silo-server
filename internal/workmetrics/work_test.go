package workmetrics

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Silo-Server/silo-server/internal/database/queryattr"
)

func TestStartDetachesRequestAttribution(t *testing.T) {
	t.Setenv("SILO_QUERY_ATTRIBUTION_ENABLED", "true")
	if !queryattr.Initialize() {
		t.Fatal("query attribution is disabled")
	}
	ctx, request := queryattr.StartRequest(t.Context())
	workCtx, run := Start(ctx, "scan", time.Time{})
	defer run.Finish("success")
	op := queryattr.Begin(workCtx, queryattr.KindQuery, "SELECT 1", nil, 1)
	op.End(workCtx, nil)
	var record slog.Record
	record.Add(request.LogAttrs()...)
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "db_queries" {
			t.Errorf("request db_queries = %v, want field absent after detached work", attr.Value)
		}
		return true
	})
}

func TestAttemptLifecycleAndBoundedLabels(t *testing.T) {
	before := testutil.ToFloat64(completed.WithLabelValues("other", "canceled"))
	for i := 0; i < 100; i++ {
		ctx, run := Start(t.Context(), fmt.Sprintf("secret-path-%d", i), time.Now().Add(-time.Second))
		if got := testutil.ToFloat64(active.WithLabelValues("other")); got != 1 {
			t.Fatalf("active = %v", got)
		}
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() { FinishContext(ctx, legacyCanceled) })
		}
		wg.Wait()
		run.Finish("success")
	}
	if got := testutil.ToFloat64(active.WithLabelValues("other")); got != 0 {
		t.Fatalf("active leaked: %v", got)
	}
	if got := testutil.ToFloat64(completed.WithLabelValues("other", "canceled")); got-before != 100 {
		t.Fatalf("count %v -> %v", before, got)
	}
	if Category("plugin:private-name") != "plugin" || Outcome("private error") != "unknown" {
		t.Fatal("unbounded dimension")
	}
}

func TestQueueUnavailableAndStaleSamplesOmitValues(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	s := &QueueSampler{samples: map[string]queueSample{"scan": {at: old, available: true, states: map[string]queueState{"queued": {count: 7, oldest: &old}}}}}
	registry := prometheus.NewRegistry()
	registry.MustRegister(s)
	for _, available := range []bool{true, false} {
		sample := s.samples["scan"]
		sample.available = available
		s.samples["scan"] = sample
		families, err := registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, family := range families {
			if family.GetName() == "silo_queue_items" {
				t.Fatal("stale or unavailable queue appeared healthy")
			}
		}
	}
	sample := s.samples["scan"]
	sample.at = time.Now()
	sample.available = true
	s.samples["scan"] = sample
	if count := testutil.CollectAndCount(s, "silo_queue_items"); count != 1 {
		t.Fatalf("fresh sample missing: %d", count)
	}
}

func findQueueGauge(t *testing.T, registry *prometheus.Registry, metricName, queue, state string) (float64, bool) {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != metricName {
			continue
		}
		for _, m := range family.GetMetric() {
			var gotQueue, gotState string
			for _, lp := range m.GetLabel() {
				switch lp.GetName() {
				case labelQueue:
					gotQueue = lp.GetValue()
				case "state":
					gotState = lp.GetValue()
				}
			}
			if gotQueue == queue && gotState == state {
				return m.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

func TestQueueSamplerOmitsNilOldestTimestamp(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	runningAt := now.Add(-10 * time.Minute)
	s := &QueueSampler{
		samples: map[string]queueSample{
			workloadImages: {
				at:        now,
				available: true,
				states: map[string]queueState{
					stateQueued:  {count: 4, oldest: nil},
					stateRunning: {count: 1, oldest: &runningAt},
				},
			},
		},
	}
	registry := prometheus.NewRegistry()
	registry.MustRegister(s)
	if got, ok := findQueueGauge(t, registry, "silo_queue_items", workloadImages, stateQueued); !ok || got != 4 {
		t.Fatalf("queued depth = (%v, %v), want (4, true)", got, ok)
	}
	if got, ok := findQueueGauge(t, registry, "silo_queue_oldest_requested_timestamp_seconds", workloadImages, stateQueued); ok {
		t.Fatalf("queued oldest timestamp unexpectedly exported: %v", got)
	}
	if got, ok := findQueueGauge(t, registry, "silo_queue_oldest_requested_timestamp_seconds", workloadImages, stateRunning); !ok || got != float64(runningAt.Unix()) {
		t.Fatalf("running oldest timestamp = (%v, %v), want (%v, true)", got, ok, float64(runningAt.Unix()))
	}
}

func BenchmarkWorkAttempt(b *testing.B) {
	ctx := context.Background()
	for b.Loop() {
		_, run := Start(ctx, "scan", time.Time{})
		run.Finish("success")
	}
}

func TestProfileAppliesAndRestoresGoroutineLabels(t *testing.T) {
	pprof.Do(t.Context(), pprof.Labels("workload", "outer"), func(parent context.Context) {
		ctx, run := Start(parent, "scan", time.Time{})
		defer run.Finish("success")
		restore := Profile(ctx)
		var snapshot bytes.Buffer
		if err := pprof.Lookup("goroutine").WriteTo(&snapshot, 1); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(snapshot.String(), `"workload":"scan"`) {
			t.Fatal("workload labels absent from actual goroutine profile")
		}
		restore()
		snapshot.Reset()
		if err := pprof.Lookup("goroutine").WriteTo(&snapshot, 1); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(snapshot.String(), `"workload":"outer"`) {
			t.Fatal("initiating labels were not restored")
		}
	})
}
