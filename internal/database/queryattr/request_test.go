package queryattr

import (
	"context"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"
)

func setup(t *testing.T) {
	t.Helper()
	old := enabled.Load()
	enabled.Store(true)
	sites.Clear()
	pcs.Clear()
	shapeCache.Clear()
	shapeCacheSize.Store(0)
	t.Cleanup(func() { enabled.Store(old) })
}
func namedOp(ctx context.Context, siteName, shape string) Op {
	op := Begin(ctx, KindQuery, "SELECT 1", nil, 1)
	op.row = internSite(siteName).shapeRow(shape)
	return op
}
func attrsMap(attrs []any) map[string]any {
	m := make(map[string]any)
	for _, a := range attrs {
		attr := a.(slog.Attr)
		m[attr.Key] = attr.Value.Any()
	}
	return m
}
func requireAttr(t *testing.T, m map[string]any, key string, want any) {
	t.Helper()
	if got := m[key]; !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v (%T), want %v (%T)", key, got, got, want, want)
	}
}
func TestRequestTotals(t *testing.T) {
	for _, extra := range []int{0, 2} {
		t.Run(string(rune('0'+extra)), func(t *testing.T) {
			setup(t)
			ctx, r := StartRequest(context.Background())
			if r == nil {
				t.Fatal("StartRequest returned nil, want request")
			}
			a := namedOp(ctx, "a", "x")
			a.end(ctx, nil, 5, time.Time{})
			b := namedOp(ctx, "b", "y")
			b.end(ctx, nil, 7, time.Time{})
			batch := namedOp(ctx, "b", "batch")
			batch.kind = KindBatch
			batch.end(ctx, nil, 7, time.Time{})
			acquire := Begin(ctx, KindAcquire, "", nil, 0)
			acquire.end(ctx, nil, 3, time.Time{})
			for range extra {
				op := namedOp(ctx, "a", "x")
				op.end(ctx, nil, 1, time.Time{})
			}
			m := attrsMap(r.LogAttrs())
			requireAttr(t, m, "db_queries", uint64(3+extra))
			requireAttr(t, m, "db_query_ns", uint64(19+extra))
			requireAttr(t, m, "db_acquire_wait_ns", uint64(3))
			requireAttr(t, m, "db_slowest_site", "b")
			requireAttr(t, m, "db_slowest_shape", "batch")
			requireAttr(t, m, "db_slowest_ns", uint64(7))
			requireAttr(t, m, "db_top_site", "a")
			requireAttr(t, m, "db_top_shape", "x")
			requireAttr(t, m, "db_top_count", uint64(1+extra))
		})
	}
}
func TestRequestNoQueryAttrs(t *testing.T) {
	setup(t)
	_, r := StartRequest(context.Background())
	if attrs := r.LogAttrs(); attrs != nil {
		t.Errorf("LogAttrs = %v, want nil for idle request", attrs)
	}
	if attrs := r.LogAttrs(); attrs != nil {
		t.Errorf("second LogAttrs = %v, want nil for idle request", attrs)
	}
}
func TestRequestAcquireOnlyValues(t *testing.T) {
	setup(t)
	ctx, r := StartRequest(context.Background())
	acquire := Begin(ctx, KindAcquire, "", nil, 0)
	acquire.end(ctx, nil, 3, time.Time{})
	attrs := r.LogAttrs()
	keys := []string{"db_queries", "db_query_ns", "db_acquire_wait_ns", "db_slowest_site", "db_slowest_shape", "db_slowest_ns", "db_top_site", "db_top_shape", "db_top_count"}
	if len(attrs) != len(keys) {
		t.Fatalf("attrs = %d, want 9", len(attrs))
	}
	m := attrsMap(attrs)
	for i, k := range keys {
		if attrs[i].(slog.Attr).Key != k {
			t.Errorf("attr %d = %s, want %s", i, attrs[i].(slog.Attr).Key, k)
		}
		var want any = uint64(0)
		if k == "db_slowest_site" || k == "db_slowest_shape" || k == "db_top_site" || k == "db_top_shape" {
			want = "none"
		}
		if k == "db_acquire_wait_ns" {
			want = uint64(3)
		}
		requireAttr(t, m, k, want)
	}
}
func TestRequestEndAfterLogAttrs(t *testing.T) {
	setup(t)
	buf := capture(t)
	resetEvents()
	ctx, r := StartRequest(context.Background())
	first := r.LogAttrs()
	op := namedOp(ctx, "a", "x")
	op.end(ctx, nil, int64(time.Second), time.Unix(100, 0))
	if !reflect.DeepEqual(first, r.LogAttrs()) {
		t.Error("LogAttrs changed after close")
	}
	if op.row.calls.Load() != 1 {
		t.Errorf("row calls = %d, want 1", op.row.calls.Load())
	}
	lines := logLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("events = %d, want 1", len(lines))
	}
	requireAttr(t, lines[0], "request", false)
}
func TestStartRequestReuses(t *testing.T) {
	setup(t)
	ctx, r := StartRequest(context.Background())
	again, r2 := StartRequest(ctx)
	if r == nil || r2 != r || ctx != again {
		t.Fatal("StartRequest must reuse the existing context and non-nil request")
	}
}
func TestRequestParallelEnds(t *testing.T) {
	setup(t)
	ctx, r := StartRequest(context.Background())
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() { op := namedOp(ctx, "a", "x"); op.end(ctx, nil, 1, time.Time{}) })
	}
	wg.Wait()
	requireAttr(t, attrsMap(r.LogAttrs()), "db_queries", uint64(64))
}
