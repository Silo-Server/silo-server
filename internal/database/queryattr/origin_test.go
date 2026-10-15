package queryattr

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

var originAllocationSink context.Context

func TestInternalMarkerAllocation(t *testing.T) {
	ctx := t.Context()
	allocations := testing.AllocsPerRun(100, func() {
		originAllocationSink = Internal(ctx, ConnectionSetup)
	})
	t.Cleanup(func() { originAllocationSink = nil })
	if allocations != 1 {
		t.Fatalf("internal marker allocations=%g, want 1", allocations)
	}
}

func TestBackgroundOwnershipAndInternalShadow(t *testing.T) {
	s := NewStore()
	tr := Tracer{Store: s}
	p := &scopeProbe{}
	parent := WithOwnership(t.Context(), RequestOrigin, p)
	job := Background(parent, "scan")
	for _, ctx := range []context.Context{job, Internal(job, TelemetryPersistence), parent, context.Background()} {
		c := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 'PRIVATE_VALUE'"})
		tr.TraceQueryEnd(c, nil, pgx.TraceQueryEndData{})
	}
	if p.starts != 1 || p.finishes != 1 {
		t.Fatalf("request starts/finishes=%d/%d, want 1/1", p.starts, p.finishes)
	}
	if got := s.Totals().OriginUntracked; got != 1 {
		t.Fatalf("untracked=%d, want only the one unmarked statement", got)
	}
	own := ownershipFrom(job)
	if own.category != "scan" || own.scope != nil || own.origin != BackgroundOrigin {
		t.Fatalf("job ownership=%+v, want background/scan without request scope", own)
	}
	internal := ownershipFrom(Internal(parent, TelemetryPersistence))
	if internal.scope != nil || internal.category != "" || internal.origin != InternalOrigin {
		t.Fatalf("internal ownership=%+v, want internal without scope/category", internal)
	}
}
