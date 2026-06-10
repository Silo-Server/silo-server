package presence

import (
	"context"
	"testing"
)

func TestMemoryRegistry_ConnectedRefcount(t *testing.T) {
	r := NewMemoryRegistry()
	ctx := context.Background()
	if r.Connected(ctx, 7) {
		t.Fatal("user should start absent")
	}
	rel1 := r.Add(ctx, 7)
	rel2 := r.Add(ctx, 7)
	if !r.Connected(ctx, 7) {
		t.Fatal("user should be present after Add")
	}
	rel1()
	if !r.Connected(ctx, 7) {
		t.Fatal("still present: one connection remains")
	}
	rel2()
	if r.Connected(ctx, 7) {
		t.Fatal("absent after all releases")
	}
}

func TestMemoryRegistry_ReleaseIdempotent(t *testing.T) {
	r := NewMemoryRegistry()
	ctx := context.Background()
	rel := r.Add(ctx, 1)
	rel()
	rel() // double release must not underflow / panic
	if r.Connected(ctx, 1) {
		t.Fatal("absent")
	}
	r.Add(ctx, 1)
	if !r.Connected(ctx, 1) {
		t.Fatal("present again")
	}
}
