package playback

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWarmHardwareEncoderWithRunnerBoundsEachAttempt(t *testing.T) {
	timeout := 20 * time.Millisecond
	started := time.Now()
	err := warmHardwareEncoderWithRunner(context.Background(), timeout, "ffmpeg", transcodeHWNVENC, nil,
		func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
			<-ctx.Done()
			return []byte("timed out"), ctx.Err()
		})
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed < timeout || elapsed > timeout+time.Second {
		t.Fatalf("elapsed = %v, want bounded near %v", elapsed, timeout)
	}
}

func TestWarmHardwareEncoderCachedSharesSuccessfulWarmupPastCallerCancellation(t *testing.T) {
	state := newHardwareEncoderWarmupState()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	runner := func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	resolve := func(context.Context, string, string, string) string { return transcodeHWNVENC }

	callerCtx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- warmHardwareEncoderCached(callerCtx, "/test/ffmpeg-cancel", "auto", "", state, resolve, runner)
	}()
	<-started
	cancel()
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("first warmup error = %v", err)
	}
	if err := warmHardwareEncoderCached(context.Background(), "/test/ffmpeg-cancel", "auto", "", state, resolve, runner); err != nil {
		t.Fatalf("cached warmup error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("runner calls = %d, want one shared cached warmup", got)
	}
}
