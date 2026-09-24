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

func TestMatchMediaTaskIsHidden(t *testing.T) {
	task := NewMatchMediaTask(&fakeBatchMatcher{})

	if !task.IsHidden() {
		t.Fatal("IsHidden() = false, want true")
	}
}
