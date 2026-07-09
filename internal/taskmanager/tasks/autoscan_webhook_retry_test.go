package tasks

import (
	"context"
	"errors"
	"testing"
)

type fakeAutoscanWebhookRetrier struct {
	limit     int
	processed int
	err       error
}

func (f *fakeAutoscanWebhookRetrier) RetryPendingWebhookDeliveries(_ context.Context, limit int) (int, error) {
	f.limit = limit
	return f.processed, f.err
}

func TestAutoscanWebhookRetryTask(t *testing.T) {
	retrier := &fakeAutoscanWebhookRetrier{processed: 3}
	task := NewAutoscanWebhookRetryTask(retrier)
	if err := task.Execute(context.Background(), noopProgressReporter{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if retrier.limit != autoscanWebhookRetryBatch {
		t.Fatalf("limit = %d, want %d", retrier.limit, autoscanWebhookRetryBatch)
	}
	if !task.IsHidden() || task.DefaultTriggers()[0].IntervalMs != autoscanWebhookRetryIntervalMs {
		t.Fatalf("unexpected task configuration: hidden=%v triggers=%+v", task.IsHidden(), task.DefaultTriggers())
	}
}

func TestAutoscanWebhookRetryTaskPropagatesClaimFailure(t *testing.T) {
	task := NewAutoscanWebhookRetryTask(&fakeAutoscanWebhookRetrier{err: errors.New("database unavailable")})
	if err := task.Execute(context.Background(), noopProgressReporter{}); err == nil {
		t.Fatal("Execute must report retry queue failures")
	}
}
