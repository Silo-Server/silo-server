package downloadstorage

import (
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newTestProber(clock *fakeClock, measure func(dir, scratch string, now time.Time) Usage) *Prober {
	p := NewProber(time.Minute)
	p.now = clock.Now
	p.measure = measure
	p.done = make(chan struct{}, 8)
	return p
}

func TestProberAnswersFromLastMeasurementAndRefreshesWhenDue(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	calls := 0
	p := newTestProber(clock, func(dir, _ string, now time.Time) Usage {
		calls++
		return Usage{Dir: dir, MeasuredAt: now, Files: calls}
	})
	if _, ok := p.Current("/a", ""); ok {
		t.Fatal("no measurement has finished yet")
	}
	<-p.done
	u, ok := p.Current("/a", "")
	if !ok || u.Files != 1 || u.Stale {
		t.Fatalf("first answer = %+v ok=%v", u, ok)
	}
	clock.Advance(30 * time.Second)
	if u, _ := p.Current("/a", ""); u.Files != 1 {
		t.Fatalf("answer inside the interval should not re-measure, got %+v", u)
	}
	clock.Advance(31 * time.Second)
	p.Current("/a", "")
	<-p.done
	if u, _ := p.Current("/a", ""); u.Files != 2 {
		t.Fatalf("due answer should re-measure, got %+v", u)
	}
}

func TestProberFlagsAHungMeasurementStaleAndRunsOnlyOne(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	var mu sync.Mutex
	started := 0
	p := newTestProber(clock, func(dir, _ string, now time.Time) Usage {
		mu.Lock()
		started++
		n := started
		mu.Unlock()
		if n > 1 {
			entered <- struct{}{}
			<-release
		}
		return Usage{Dir: dir, MeasuredAt: now, Files: n}
	})
	p.Current("/a", "")
	<-p.done
	clock.Advance(2 * time.Minute)
	p.Current("/a", "") // starts measurement 2, which hangs
	<-entered
	clock.Advance(probeTimeout + time.Second)
	u, ok := p.Current("/a", "")
	if !ok || !u.Stale || u.Files != 1 {
		t.Fatalf("hung measurement should leave the last answer flagged stale, got %+v", u)
	}
	p.Refresh("/a", "")
	p.Current("/a", "")
	mu.Lock()
	if started != 2 {
		t.Fatalf("a second goroutine started against a hung measurement: %d", started)
	}
	mu.Unlock()
	close(release)
	<-p.done
	select {
	case <-entered:
		t.Fatal("a third measurement ran while the second was outstanding")
	default:
	}
}

func TestProberKeepsLastGoodNumbersOnError(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	fail := false
	p := newTestProber(clock, func(dir, _ string, now time.Time) Usage {
		if fail {
			return Usage{Dir: dir, MeasuredAt: now, Error: "boom"}
		}
		return Usage{Dir: dir, MeasuredAt: now, Files: 7}
	})
	p.Current("/a", "")
	<-p.done
	fail = true
	p.Refresh("/a", "")
	<-p.done
	u, _ := p.Current("/a", "")
	if u.Files != 7 || u.Error != "boom" || !u.Stale {
		t.Fatalf("error should keep last numbers flagged stale, got %+v", u)
	}
}

func TestProberForgetsAnswerForAnotherDirectory(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	release := make(chan struct{})
	p := newTestProber(clock, func(dir, _ string, now time.Time) Usage {
		if dir == "/b" {
			<-release
		}
		return Usage{Dir: dir, MeasuredAt: now, Files: 1}
	})
	p.Current("/a", "")
	<-p.done
	if _, ok := p.Current("/b", ""); ok {
		t.Fatal("an answer for /a must not be reported for /b")
	}
	close(release)
	<-p.done
	if u, ok := p.Current("/b", ""); !ok || u.Dir != "/b" {
		t.Fatalf("answer for /b = %+v ok=%v", u, ok)
	}
}
