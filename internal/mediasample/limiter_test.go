package mediasample

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// acquireAsync starts Acquire in the background and returns a channel that
// delivers its release once a slot is granted, or closes if Acquire fails.
// Call it inside a synctest bubble so assertWaiting and assertGranted can tell
// when the attempt has reached the limiter.
func acquireAsync(l *Limiter, ctx context.Context) <-chan func() {
	granted := make(chan func(), 1)
	go func() {
		release, err := l.Acquire(ctx)
		if err != nil {
			close(granted)
			return
		}
		granted <- release
	}()
	return granted
}

// assertWaiting checks that the acquisition started by acquireAsync is
// blocked inside the limiter. synctest.Wait returns only once that goroutine
// is durably blocked or has finished, so a slow scheduler cannot pass for a
// waiting acquisition.
func assertWaiting(t *testing.T, granted <-chan func(), why string) {
	t.Helper()
	synctest.Wait()
	select {
	case release, ok := <-granted:
		if !ok {
			t.Fatalf("acquire failed, want it to wait: %s", why)
		}
		release()
		t.Fatalf("acquire was granted, want it to wait: %s", why)
	default:
	}
}

// assertGranted checks that the acquisition started by acquireAsync has been
// granted and returns its release.
func assertGranted(t *testing.T, granted <-chan func(), why string) func() {
	t.Helper()
	synctest.Wait()
	select {
	case release, ok := <-granted:
		if !ok {
			t.Fatalf("acquire failed, want a grant: %s", why)
		}
		return release
	default:
		t.Fatalf("acquire still waiting: %s", why)
		return nil
	}
}

func mustAcquire(t *testing.T, l *Limiter, ctx context.Context) func() {
	t.Helper()
	release, err := l.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	return release
}

func TestLimiterBoundsSharedSlots(t *testing.T) {
	l := NewLimiter(2)
	a := mustAcquire(t, l, context.Background())
	b := mustAcquire(t, l, context.Background())
	busy, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := l.Acquire(busy); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third acquire = %v, want it to wait", err)
	}
	a()
	b()
}

func TestLimiterInteractiveWorkUsesTheReservedSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(1)
		held := mustAcquire(t, l, context.Background()) // background work holds every shared slot
		defer held()

		interactive := mustAcquire(t, l, WithInteractive(context.Background()))

		// The reserved slot holds one run; a second interactive caller waits.
		ctx, cancel := context.WithCancel(WithInteractive(context.Background()))
		defer cancel()
		granted := acquireAsync(l, ctx)
		assertWaiting(t, granted, "the reserved slot and the shared slot are both held")
		interactive()
		assertGranted(t, granted, "the reserved slot was released")()

		background, cancelBackground := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancelBackground()
		if _, err := l.Acquire(background); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("background acquire = %v, want it to wait for a shared slot", err)
		}
	})
}

func TestLimiterInteractiveWorkPrefersTheReservedSlot(t *testing.T) {
	l := NewLimiter(1)
	interactive := mustAcquire(t, l, WithInteractive(context.Background()))
	defer interactive()
	// The shared slot is still free for background work.
	mustAcquire(t, l, context.Background())()
}

func TestLimiterLoweringCapacityWaitsForHoldersToDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(3)
		held := []func(){
			mustAcquire(t, l, context.Background()),
			mustAcquire(t, l, context.Background()),
			mustAcquire(t, l, context.Background()),
		}

		l.Resize(1)
		held[0]()
		held[1]()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		granted := acquireAsync(l, ctx)
		assertWaiting(t, granted, "one run from the old limit still holds a slot and the new limit is 1")

		held[2]()
		assertGranted(t, granted, "every old holder released")()
	})
}

func TestLimiterRaisingCapacityWakesWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(1)
		release := mustAcquire(t, l, context.Background())
		defer release()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		granted := acquireAsync(l, ctx)
		assertWaiting(t, granted, "the only slot is held")

		l.Resize(2)
		assertGranted(t, granted, "the limit grew to 2")()
	})
}

func TestLimiterCapacityIsAtLeastOne(t *testing.T) {
	l := NewLimiter(0)
	mustAcquire(t, l, context.Background())()
	l.Resize(-3)
	mustAcquire(t, l, context.Background())()
}

func TestLimiterAcquireWithEndedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if release, err := NewLimiter(1).Acquire(ctx); release != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire on an ended context = (%v, %v), want (nil, context.Canceled)", release != nil, err)
	}
}
