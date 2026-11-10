package passwordhash

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// limit gives the test its own slot count, queue size, and wait limit.
func limit(t *testing.T, slots, waitersPerSlotN int, wait time.Duration) {
	t.Helper()
	oldLimit, oldWaiters, oldWait := slotLimit, waitersPerSlot, maxWait
	slotLimit = func() int { return slots }
	waitersPerSlot, maxWait = waitersPerSlotN, wait
	mu.Lock()
	lastAhead = false
	mu.Unlock()
	t.Cleanup(func() {
		slotLimit, waitersPerSlot, maxWait = oldLimit, oldWaiters, oldWait
		mu.Lock()
		defer mu.Unlock()
		if inFlight != 0 || len(queue) != 0 || len(ahead) != 0 {
			t.Errorf("after the test: %d in flight, %d queued, %d ahead; want none", inFlight, len(queue), len(ahead))
		}
	})
}

func state() (running, waiting int) {
	mu.Lock()
	defer mu.Unlock()
	return inFlight, len(queue) + len(ahead)
}

// waitForQueue waits until n operations are queued, ahead or not.
func waitForQueue(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, waiting := state(); waiting == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue never reached %d waiters", n)
		}
		runtime.Gosched()
	}
}

func TestAdmitBoundsRunningAndQueuedOperations(t *testing.T) {
	limit(t, 2, 1, time.Minute) // two run, two may wait in each queue

	first, err := Admit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Admit(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	admitted := make(chan int, 4)
	var done sync.WaitGroup
	queueUp := func(i int, admit func(context.Context) (*Permit, error)) {
		done.Go(func() {
			p, err := admit(context.Background())
			if err != nil {
				t.Errorf("queued waiter %d: %v", i, err)
				admitted <- -1
				return
			}
			admitted <- i
			p.Release()
		})
		waitForQueue(t, i+1) // queued in this order
	}
	queueUp(0, Admit)
	queueUp(1, Admit)
	// Both slots are held and the queue is full: refused without waiting.
	if _, err := Admit(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("Admit beyond the queue = %v, want ErrBusy", err)
	}
	// PIN checks have a queue of their own, bounded the same way.
	queueUp(2, AdmitAhead)
	queueUp(3, AdmitAhead)
	if _, err := AdmitAhead(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("AdmitAhead beyond its queue = %v, want ErrBusy", err)
	}
	if running, waiting := state(); running != 2 || waiting != 4 {
		t.Fatalf("%d running, %d queued; want 2 and 4", running, waiting)
	}

	// Freed slots go to a PIN check first, then alternate between the
	// queues, each in arrival order.
	first.Release()
	first.Release() // a second release does not free another slot
	for _, want := range []int{2, 0, 3, 1} {
		if got := <-admitted; got != want {
			t.Fatalf("turn went to waiter %d, want %d", got, want)
		}
	}
	done.Wait()
	if running, waiting := state(); running != 1 || waiting != 0 {
		t.Fatalf("%d running, %d queued; want 1 and 0", running, waiting)
	}
	second.Release()
}

func TestAdmitGivesUpWithoutHoldingASlot(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name  string
		wait  time.Duration
		ctx   func() (context.Context, context.CancelFunc)
		cause error
	}{
		{"request already ended", time.Minute, func() (context.Context, context.CancelFunc) {
			return canceled, func() {}
		}, context.Canceled},
		{"request ends while queued", time.Minute, func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 20*time.Millisecond)
		}, context.DeadlineExceeded},
		{"no turn within the wait limit", 20 * time.Millisecond, func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			limit(t, 1, 1, tc.wait)
			held, err := Admit(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := tc.ctx()
			defer cancel()

			err = Compare(ctx, []byte("hash"), []byte("password"))
			if !errors.Is(err, ErrBusy) || errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
				t.Fatalf("Compare = %v, want ErrBusy", err)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("Compare = %v, want it to wrap %v", err, tc.cause)
			}
			if _, err := AdmitAhead(ctx); !errors.Is(err, ErrBusy) {
				t.Fatalf("AdmitAhead = %v, want ErrBusy", err)
			}
			hashCtx, cancelHash := tc.ctx()
			defer cancelHash()
			if _, err := Generate(hashCtx, []byte("password"), bcrypt.MinCost); !errors.Is(err, ErrBusy) {
				t.Fatalf("Generate = %v, want ErrBusy", err)
			}
			if running, waiting := state(); running != 1 || waiting != 0 {
				t.Fatalf("%d running, %d queued after giving up; want 1 and 0", running, waiting)
			}

			held.Release()
			if _, err := Generate(canceled, []byte("password"), bcrypt.MinCost); !errors.Is(err, ErrBusy) {
				t.Fatalf("Generate for an ended request with a free slot = %v, want ErrBusy", err)
			}
		})
	}
}

// A waiter that gives up just as its turn comes passes the slot on.
func TestGivingUpAtTheHandoffLeavesNoSlotHeld(t *testing.T) {
	limit(t, 1, 1, time.Minute)
	for range 200 {
		held, err := Admit(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			if p, err := AdmitAhead(ctx); err == nil {
				p.Release()
			}
		}()
		waitForQueue(t, 1)
		go cancel()
		held.Release()
		<-done
		cancel()
		if running, waiting := state(); running != 0 || waiting != 0 {
			t.Fatalf("%d running, %d queued after the handoff; want none", running, waiting)
		}
	}
}
