package requests

import (
	"context"
	"errors"
	"testing"
)

func TestAdminQueueNeedsAnAdmin(t *testing.T) {
	svc := newTestService(newFakeStore())
	member := testViewer(1)
	if _, err := svc.CountAdminViews(context.Background(), member); !errors.Is(err, ErrForbidden) {
		t.Fatalf("counts: err = %v, want ErrForbidden", err)
	}
	if _, err := svc.ListRequestEvents(context.Background(), member, "r1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("events: err = %v, want ErrForbidden", err)
	}
}
