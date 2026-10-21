package queryattr

import (
	"context"
	"sync"
	"testing"
)

func TestDisabledIsInert(t *testing.T) {
	setup(t)
	enabled.Store(false)
	ctx := context.Background()
	next, r := StartRequest(ctx)
	if next != ctx || r != nil || r.LogAttrs() != nil {
		t.Error("disabled StartRequest must return original context, nil request and attrs")
	}
	op := Begin(ctx, KindQuery, "SELECT 1", nil, 1)
	op.End(ctx, nil)
	if op.Site() != "" || op.Shape() != "" || Detach(ctx) != ctx || Quiet(ctx) != ctx {
		t.Error("disabled attribution must be inert")
	}
	n := 0
	sites.Range(func(_, _ any) bool { n++; return true })
	if n != 0 {
		t.Errorf("disabled sites = %d, want 0", n)
	}
}
func TestInitializeDefaultsOff(t *testing.T) {
	setup(t)
	t.Setenv("SILO_QUERY_ATTRIBUTION_ENABLED", "")
	initializeOnce = sync.Once{}
	t.Cleanup(func() { initializeOnce = sync.Once{} })
	if Initialize() {
		t.Error("Initialize = true, want false with unset env")
	}
	t.Setenv("SILO_QUERY_ATTRIBUTION_ENABLED", "true")
	if Initialize() {
		t.Error("Initialize reread env after first call")
	}
}
