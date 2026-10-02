package trickplay

import (
	"context"
	"testing"
)

func TestReconcileSchedulesContinuationAfterReleasingClusterLockDB(t *testing.T) {
	f := newFixture(t)
	q := &reconcileQueue{fakeQueue: newFakeQueue(), reconcile: func(_ context.Context, call int) (ReconcileStats, error) {
		if call <= reconcilePassBatches {
			return ReconcileStats{Added: reconcileBatch}, nil
		}
		return ReconcileStats{Added: 1}, nil
	}}
	s := newService(q, &fakeStore{}, nil, &fakeExtractor{}, "server")
	s.pool = f.pool
	stats, ran, err := s.Reconcile(t.Context())
	if err != nil || !ran || stats.Added != reconcileBatch*reconcilePassBatches {
		t.Fatalf("first pass=%+v ran=%v error=%v", stats, ran, err)
	}
	select {
	case <-s.reconcile:
	default:
		t.Fatal("full pass did not schedule an immediate continuation")
	}
	stats, ran, err = s.Reconcile(t.Context())
	if err != nil || !ran || stats.Added != 1 {
		t.Fatalf("continuation could not take lock: %+v ran=%v error=%v", stats, ran, err)
	}
	select {
	case <-s.reconcile:
		t.Fatal("partial batch scheduled another continuation")
	default:
	}
}
