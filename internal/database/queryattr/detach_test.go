package queryattr

import (
	"context"
	"testing"
	"time"
)

func TestDetachHidesRequest(t *testing.T) {
	setup(t)
	type key struct{}
	base, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "kept"))
	defer cancel()
	ctx, r := StartRequest(base)
	detached := Detach(ctx)
	op := namedOp(detached, "a", "x")
	op.end(detached, nil, 1, time.Time{})
	if attrs := r.LogAttrs(); len(attrs) != 0 {
		t.Errorf("LogAttrs = %v, want no attributes after detached work", attrs)
	}
	if op.row.calls.Load() != 1 {
		t.Errorf("row calls = %d, want 1", op.row.calls.Load())
	}
	_, fresh := StartRequest(detached)
	if r == nil || fresh == nil || fresh == r {
		t.Error("detached StartRequest must create a new request")
	}
	if detached.Value(key{}) != "kept" {
		t.Error("Detach lost context value")
	}
	cancel()
	if detached.Err() != context.Canceled {
		t.Errorf("Detach Err = %v, want canceled", detached.Err())
	}
}
