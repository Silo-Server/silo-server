package tasks

import (
	"context"
	"testing"
)

type fakeBatchMatcher struct {
	processed int
	err       error
}

func (f *fakeBatchMatcher) ProcessBatch(context.Context) (int, error) {
	return f.processed, f.err
}

func TestMatchMediaTaskIsVisible(t *testing.T) {
	task := NewMatchMediaTask(&fakeBatchMatcher{})

	if task.IsHidden() {
		t.Fatal("IsHidden() = true, want false")
	}
}
