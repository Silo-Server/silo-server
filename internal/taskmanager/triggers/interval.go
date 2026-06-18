package triggers

import (
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// IntervalTrigger fires every N milliseconds, measured from completion of the
// previous run (not from start). If no previous run exists, it fires
// interval-after-Start().
type IntervalTrigger struct {
	cfg      taskmanager.TriggerConfig
	interval time.Duration
	ch       chan struct{}
	nextRun  time.Time
	timer    *time.Timer
	stopCh   chan struct{}
	mu       sync.Mutex
}

func NewIntervalTrigger(cfg taskmanager.TriggerConfig) *IntervalTrigger {
	return &IntervalTrigger{
		cfg:      cfg,
		interval: time.Duration(cfg.IntervalMs) * time.Millisecond,
		ch:       make(chan struct{}, 1),
	}
}

func (t *IntervalTrigger) Start(lastResult *taskmanager.ExecutionResult) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Drain any stale signal from a previous timer fire.
	select {
	case <-t.ch:
	default:
	}

	var base time.Time
	if lastResult != nil && !lastResult.CompletedAt.IsZero() {
		base = lastResult.CompletedAt
	} else {
		base = time.Now()
	}

	t.nextRun = base.Add(t.interval)
	delay := time.Until(t.nextRun)
	if delay < 0 {
		delay = 0
		t.nextRun = time.Now()
	}

	stopCh := make(chan struct{})
	timer := time.NewTimer(delay)
	t.stopCh = stopCh
	t.timer = timer

	go func() {
		select {
		case <-stopCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
			select {
			case t.ch <- struct{}{}:
			default:
			}
		}
	}()
}

func (t *IntervalTrigger) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopCh != nil {
		select {
		case <-t.stopCh:
		default:
			close(t.stopCh)
		}
	}
}

func (t *IntervalTrigger) NextRunTime() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.nextRun
}

func (t *IntervalTrigger) Config() taskmanager.TriggerConfig { return t.cfg }
func (t *IntervalTrigger) C() <-chan struct{}                { return t.ch }
