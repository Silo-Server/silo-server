package queryattr

import (
	"context"
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
