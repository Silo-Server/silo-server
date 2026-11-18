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
