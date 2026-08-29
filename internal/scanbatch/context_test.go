package scanbatch

import (
	"context"
	"testing"
)

func TestRunID(t *testing.T) {
	if got := RunID(context.Background()); got != "" {
		t.Fatalf("RunID(background) = %q, want empty", got)
	}
	ctx := WithRunID(context.Background(), "  run-1  ")
	if got := RunID(ctx); got != "run-1" {
		t.Fatalf("RunID(ctx) = %q, want run-1", got)
	}
}
