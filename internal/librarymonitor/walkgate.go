package librarymonitor

import (
	"context"
	"sync"
	"time"
)

// walkGate runs root walks one at a time in the order their attempts were
// issued, which reconcile does in library sort order. That keeps it
// predictable which libraries get watches when the kernel limit is tight.
// A walk that stops recording directories for the stall duration (or an
// attempt stuck before its walk, for example on a hung statfs) no longer
// holds later walks back.
type walkGate struct {
	stall time.Duration
	now   func() time.Time

	mu      sync.Mutex
	seq     uint64
	active  map[uint64]*walkTicket
	changed chan struct{}
}

// walkTicket is one attempt's place in the walk order.
type walkTicket struct {
	seq        uint64
	waiting    bool
	progress   func() int
	lastCount  int
	lastChange time.Time
}

func newWalkGate(stall time.Duration, now func() time.Time) *walkGate {
	return &walkGate{
		stall:   stall,
		now:     now,
		active:  make(map[uint64]*walkTicket),
		changed: make(chan struct{}),
	}
}

func (g *walkGate) issue() *walkTicket {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.seq++
	t := &walkTicket{seq: g.seq, lastChange: g.now()}
	g.active[t.seq] = t
	return t
}

// setProgress tells the gate how to measure t's walk progress.
func (g *walkGate) setProgress(t *walkTicket, progress func() int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	t.progress = progress
	t.lastCount = -1
	t.lastChange = g.now()
}

// done releases t's place.
func (g *walkGate) done(t *walkTicket) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.active[t.seq]; !ok {
		return
	}
	delete(g.active, t.seq)
	close(g.changed)
	g.changed = make(chan struct{})
}

// wait blocks until every earlier ticket has finished or stalled.
func (g *walkGate) wait(ctx context.Context, t *walkTicket) error {
	check := max(g.stall/4, time.Millisecond)
	for {
		g.mu.Lock()
		t.waiting = true
		now := g.now()
		blocked := false
		// No early exit: stalledLocked samples the progress of every
		// earlier walk.
		for _, other := range g.active {
			if other.seq < t.seq && !g.stalledLocked(other, now) {
				blocked = true
			}
		}
		if !blocked {
			t.waiting = false
			t.lastChange = now
			g.mu.Unlock()
			return nil
		}
		changed := g.changed
		g.mu.Unlock()

		timer := time.NewTimer(check)
		select {
		case <-changed:
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			g.mu.Lock()
			t.waiting = false
			g.mu.Unlock()
			return ctx.Err()
		}
		timer.Stop()
	}
}

// stalledLocked reports whether t has made no progress for the stall
// duration. A ticket waiting its own turn is never stalled: it holds its
// place behind the walks ahead of it.
func (g *walkGate) stalledLocked(t *walkTicket, now time.Time) bool {
	if t.waiting {
		return false
	}
	if t.progress != nil {
		if count := t.progress(); count != t.lastCount {
			t.lastCount = count
			t.lastChange = now
			return false
		}
	}
	return now.Sub(t.lastChange) >= g.stall
}
