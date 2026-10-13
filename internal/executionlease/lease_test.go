package executionlease

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestRetentionSurvivesCallerCancellationAndReleasesOnce(t *testing.T) {
	var held atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	ctx = WithRetainer(ctx, func() (func(), error) { held.Add(1); return func() { held.Add(-1) }, nil })
	release, err := Retain(context.WithoutCancel(ctx))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if held.Load() != 1 {
		t.Fatal("caller cancellation released shared work")
	}
	release()
	release()
	if held.Load() != 0 {
		t.Fatal("retained work did not release exactly once")
	}
}

func TestMissingAndExpiredRetentionPolicies(t *testing.T) {
	release, err := Retain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	release()
	expired := errors.New("expired execution")
	ctx := WithRetainer(t.Context(), func() (func(), error) { return nil, expired })
	if _, err := Retain(ctx); !errors.Is(err, expired) {
		t.Fatal("expired work retained")
	}
}
