package ratelimit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// fakeWindows answers the window script in place of one Redis server, so
// limiters on separate clients share its counts as nodes share Redis.
type fakeWindows struct {
	mu     sync.Mutex
	counts map[string]int64
	checks int
}

func (f *fakeWindows) answer(cmd redis.Cmder) error {
	// EVALSHA sha numkeys secKey minKey secLimit minLimit
	args := cmd.Args()
	secKey, minKey := args[3].(string), args[4].(string)
	secLimit, minLimit := int64(args[5].(int)), int64(args[6].(int))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	if f.counts == nil {
		f.counts = map[string]int64{}
	}
	allowed := int64(0)
	if f.counts[secKey] < secLimit && f.counts[minKey] < minLimit {
		allowed = 1
		f.counts[secKey]++
		f.counts[minKey]++
	}
	cmd.(*redis.Cmd).SetVal([]any{allowed, f.counts[secKey], secLimit, f.counts[minKey], minLimit, int64(1)})
	return nil
}

// sharedTestWaiter returns a limiter on its own client, counting in windows,
// that reads its clock from now and waits through after.
func sharedTestWaiter(t *testing.T, windows *fakeWindows, perSecond int, now func() time.Time, after func(time.Duration) <-chan time.Time) *sharedWaiter {
	t.Helper()
	rl := hookedRedisLimiter(t, refusingRedisAddr(t), &commandHook{answer: windows.answer})
	w := newSharedWaiter(rl, "outbound:test", perSecond)
	w.now, w.after = now, after
	return w
}

// Two nodes on one Redis budget together make no more calls in any second
// than the budget allows, and still get the whole budget each second.
func TestOutboundLimiterNodesShareOneBudget(t *testing.T) {
	const perSecond = 10
	start := time.Unix(1_800_000_000, 250_000_000)
	clock := start
	now := func() time.Time { return clock }
	// Waiting moves the clock on instead of sleeping.
	after := func(d time.Duration) <-chan time.Time {
		clock = clock.Add(d)
		ready := make(chan time.Time, 1)
		ready <- clock
		return ready
	}
	windows := &fakeWindows{}
	nodes := []*sharedWaiter{
		sharedTestWaiter(t, windows, perSecond, now, after),
		sharedTestWaiter(t, windows, perSecond, now, after),
	}

	perWallSecond := map[int64]int{}
	for i := range 5 * perSecond {
		if err := nodes[i%len(nodes)].Wait(t.Context()); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		perWallSecond[clock.Unix()]++
	}
	for second, calls := range perWallSecond {
		if calls != perSecond {
			t.Errorf("second %d had %d calls across both nodes, want %d", second-start.Unix(), calls, perSecond)
		}
	}
	if len(perWallSecond) != 5 {
		t.Errorf("%d calls spread over %d seconds, want 5", 5*perSecond, len(perWallSecond))
	}
}

// A call waiting for a slot gives up when its context ends, with or without
// Redis. Without Redis the limiter is the per-process token bucket every
// client used to build: the burst passes at once, then calls wait.
func TestOutboundLimiterWaitEndsWithContext(t *testing.T) {
	const perSecond = 1
	waiting := make(chan struct{}, 1)
	for _, tt := range []struct {
		name    string
		limiter func(*testing.T) Waiter
	}{
		{name: "per process", limiter: func(*testing.T) Waiter { return NewOutboundLimiter(nil, "outbound:test", perSecond) }},
		{name: "shared", limiter: func(t *testing.T) Waiter {
			clock := time.Unix(1_800_000_000, 0)
			return sharedTestWaiter(t, &fakeWindows{}, perSecond,
				func() time.Time { return clock },
				// The next second never comes.
				func(time.Duration) <-chan time.Time { waiting <- struct{}{}; return nil })
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			limiter := tt.limiter(t)
			for i := range perSecond {
				if err := limiter.Wait(t.Context()); err != nil {
					t.Fatalf("call %d within the burst: %v", i, err)
				}
			}

			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- limiter.Wait(ctx) }()
			if tt.name == "shared" {
				<-waiting
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("call past the budget returned %v, want it to wait until its context ended", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("call kept waiting after its context ended")
			}
		})
	}
}

// A backlog of waiters on one node does not multiply Redis load: while the
// budget is spent, one waiter checks Redis each second and the rest wait
// on the node without calling it.
func TestOutboundLimiterBacklogChecksRedisOncePerNode(t *testing.T) {
	const perSecond, backlog = 2, 20
	clock := time.Unix(1_800_000_000, 0)
	waiting := make(chan struct{}, backlog)
	windows := &fakeWindows{}
	w := sharedTestWaiter(t, windows, perSecond,
		func() time.Time { return clock },
		// The next second never comes.
		func(time.Duration) <-chan time.Time { waiting <- struct{}{}; return nil })
	for i := range perSecond {
		if err := w.Wait(t.Context()); err != nil {
			t.Fatalf("call %d within the budget: %v", i, err)
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	for range backlog {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.Wait(ctx)
		}()
	}
	<-waiting
	// Negative observation: give the other waiters time to reach Redis if
	// they could; with the gate they never do.
	select {
	case <-waiting:
		t.Fatal("a second waiter checked Redis while the first was still waiting for the next second")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	wg.Wait()

	windows.mu.Lock()
	defer windows.mu.Unlock()
	if want := perSecond + 1; windows.checks != want {
		t.Fatalf("%d waiters made %d Redis checks after the budget ran out, want 1", backlog, windows.checks-perSecond)
	}
}
