package main

import (
	"context"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

func TestNodeDrainTimeout(t *testing.T) {
	for _, test := range []struct {
		value string
		want  time.Duration
	}{
		{"", transcodenode.DefaultDrainTimeout},
		{"0", 0},
		{"0s", 0},
		{"900", 15 * time.Minute},
		{"45m", 45 * time.Minute},
		{" 2h ", 2 * time.Hour},
		{"-5m", transcodenode.DefaultDrainTimeout},
		{"99999999999", transcodenode.DefaultDrainTimeout}, // more seconds than a Duration holds
		{"soon", transcodenode.DefaultDrainTimeout},
	} {
		if got := nodeDrainTimeout(test.value); got != test.want {
			t.Errorf("nodeDrainTimeout(%q) = %v, want %v", test.value, got, test.want)
		}
	}
}

// fakeDrain is a node whose work ends when done is closed.
type fakeDrain struct {
	began chan struct{}
	done  chan struct{}
}

func newFakeDrain(timeout time.Duration) (*nodeDrain, *fakeDrain) {
	f := &fakeDrain{began: make(chan struct{}), done: make(chan struct{})}
	return &nodeDrain{
		timeout: timeout,
		begin:   func() { close(f.began) },
		wait: func(ctx context.Context) error {
			select {
			case <-f.done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		activeJobs: func() int { return 1 },
	}, f
}

func runDrain(d *nodeDrain, sigCh chan os.Signal) <-chan struct{} {
	finished := make(chan struct{})
	go func() {
		d.run(sigCh)
		close(finished)
	}()
	return finished
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestNodeDrainRunsUntilTheWorkEnds(t *testing.T) {
	d, node := newFakeDrain(time.Hour)
	finished := runDrain(d, make(chan os.Signal, 1))
	waitClosed(t, node.began, "the drain to begin")
	select {
	case <-finished:
		t.Fatal("drain returned with work left")
	default:
	}
	close(node.done)
	waitClosed(t, finished, "the drain to return once the work ended")
}

func TestSecondSignalEndsTheDrain(t *testing.T) {
	d, node := newFakeDrain(time.Hour)
	sigCh := make(chan os.Signal, 1)
	finished := runDrain(d, sigCh)
	waitClosed(t, node.began, "the drain to begin")
	sigCh <- syscall.SIGTERM
	waitClosed(t, finished, "the drain to end on a second signal")
}

// A node whose key the main server refuses cannot be reached by any relay,
// so a SIGTERM drain does not wait for its sessions; with an accepted key it
// waits as before. The key state is read when the wait would start and again
// while it runs.
func TestNodeDrainDoesNotWaitWhileTheKeyIsRejected(t *testing.T) {
	run := func(rejected *atomic.Bool) (finished <-chan struct{}) {
		d, _ := newFakeDrain(time.Hour)
		d.keyRejected = rejected.Load
		return runDrain(d, make(chan os.Signal, 1))
	}
	t.Run("rejected when the drain begins", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var rejected atomic.Bool
			rejected.Store(true)
			finished := run(&rejected)
			synctest.Wait()
			select {
			case <-finished:
			default:
				t.Fatal("drain waited for sessions no relay can reach")
			}
		})
	})
	t.Run("rejected during the drain", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var rejected atomic.Bool
			finished := run(&rejected)
			time.Sleep(30 * time.Minute) //nolint:forbidigo // advances synctest's fake clock
			synctest.Wait()
			select {
			case <-finished:
				t.Fatal("drain with an accepted key returned with work left")
			default:
			}
			rejected.Store(true)
			time.Sleep(nodeDrainKeyCheckInterval) //nolint:forbidigo // advances synctest's fake clock
			synctest.Wait()
			select {
			case <-finished:
			default:
				t.Fatal("drain kept waiting after the key was rejected")
			}
		})
	})
	t.Run("accepted throughout", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var rejected atomic.Bool
			finished := run(&rejected)
			time.Sleep(time.Hour - time.Second) //nolint:forbidigo // advances synctest's fake clock
			synctest.Wait()
			select {
			case <-finished:
				t.Fatal("drain with an accepted key returned before its timeout")
			default:
			}
			time.Sleep(2 * time.Second) //nolint:forbidigo // advances synctest's fake clock
			synctest.Wait()
			select {
			case <-finished:
			default:
				t.Fatal("drain did not stop at its timeout")
			}
		})
	})
}

// Announcements that arrive while a node's check runs fold into one more check
// after it, so the last check starts after the last announcement.
func TestNodeRechecksCoalescePerNode(t *testing.T) {
	release := map[int]chan struct{}{1: make(chan struct{}), 2: make(chan struct{})}
	started := make(chan int, 8)
	r := newNodeRechecks(func(id int) {
		started <- id
		<-release[id]
	})
	r.request(1)
	if got := <-started; got != 1 {
		t.Fatalf("checked node %d", got)
	}
	r.request(1)
	r.request(1)
	r.request(2)
	if got := <-started; got != 2 {
		t.Fatalf("checked node %d, want node 2 to run alongside", got)
	}
	release[1] <- struct{}{} // node 1's first check ends
	if got := <-started; got != 1 {
		t.Fatalf("checked node %d, want node 1 again", got)
	}
	release[1] <- struct{}{}
	release[2] <- struct{}{}
	r.request(1) // node 1 is idle again, so this starts a fresh check
	if got := <-started; got != 1 {
		t.Fatalf("checked node %d", got)
	}
	release[1] <- struct{}{}
	if len(started) != 0 {
		t.Fatalf("%d extra checks", len(started))
	}
}
