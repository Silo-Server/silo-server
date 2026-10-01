package trickplay

import (
	"context"
	"errors"
	"testing"
)

type reconcileQueue struct {
	*fakeQueue
	calls     int
	reconcile func(context.Context, int) (ReconcileStats, error)
}

func (q *reconcileQueue) Reconcile(ctx context.Context, _ Recipe, _ string, batch int) (ReconcileStats, error) {
	q.calls++
	if batch != reconcileBatch {
		return ReconcileStats{}, errors.New("unexpected batch size")
	}
	return q.reconcile(ctx, q.calls)
}

func TestReconcileDrainsFullBatches(t *testing.T) {
	q := &reconcileQueue{fakeQueue: newFakeQueue(), reconcile: func(_ context.Context, call int) (ReconcileStats, error) {
		if call <= 2 {
			return ReconcileStats{Added: reconcileBatch}, nil
		}
		return ReconcileStats{Added: 501}, nil
	}}
	s := newService(q, &fakeStore{}, nil, &fakeExtractor{}, "server")
	stats, more, err := s.reconcileBatches(t.Context(), testRecipe, q.Reconcile)
	if err != nil || more || q.calls != 3 || stats.Added != 10501 {
		t.Fatalf("stats=%+v more=%v calls=%d error=%v", stats, more, q.calls, err)
	}
	select {
	case <-s.wake:
	default:
		t.Fatal("workers were not woken")
	}
}

func TestReconcileYieldsAndRequestsContinuation(t *testing.T) {
	q := &reconcileQueue{fakeQueue: newFakeQueue(), reconcile: func(context.Context, int) (ReconcileStats, error) {
		return ReconcileStats{Removed: reconcileBatch}, nil
	}}
	s := newService(q, &fakeStore{}, nil, &fakeExtractor{}, "server")
	stats, more, err := s.reconcileBatches(t.Context(), testRecipe, q.Reconcile)
	if err != nil || !more || q.calls != reconcilePassBatches || stats.Removed != reconcileBatch*reconcilePassBatches {
		t.Fatalf("stats=%+v more=%v calls=%d error=%v", stats, more, q.calls, err)
	}
}

func TestReconcileStopsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	q := &reconcileQueue{fakeQueue: newFakeQueue(), reconcile: func(context.Context, int) (ReconcileStats, error) {
		cancel()
		return ReconcileStats{Stale: reconcileBatch}, nil
	}}
	s := newService(q, &fakeStore{}, nil, &fakeExtractor{}, "server")
	stats, more, err := s.reconcileBatches(ctx, testRecipe, q.Reconcile)
	if !errors.Is(err, context.Canceled) || more || q.calls != 1 || stats.Stale != reconcileBatch {
		t.Fatalf("stats=%+v more=%v calls=%d error=%v", stats, more, q.calls, err)
	}
}
