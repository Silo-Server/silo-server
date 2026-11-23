package triggers

import (
	"fmt"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// DailyTrigger fires at a specific time each day in server-local time.
type DailyTrigger struct {
	cfg     taskmanager.TriggerConfig
	hour    int
	minute  int
	ch      chan struct{}
	spread  time.Duration
	nextRun time.Time
	timer   *time.Timer
	stopCh  chan struct{}
	mu      sync.Mutex
}

func NewDailyTrigger(cfg taskmanager.TriggerConfig) *DailyTrigger {
	var h, m int
	fmt.Sscanf(cfg.TimeOfDay, "%d:%d", &h, &m)
	return &DailyTrigger{
		cfg:    cfg,
		hour:   h,
		minute: m,
		ch:     make(chan struct{}, 1),
	}
}

// SetStartSpread implements taskmanager.SpreadableTrigger.
func (d *DailyTrigger) SetStartSpread(spread time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.spread = spread
}

// calcNextRun returns the first slot after now. Slots come from calendar
// dates rather than from adding 24 hours, so a day that a daylight-saving
// change makes 23 or 25 hours long keeps the configured wall-clock time.
func (d *DailyTrigger) calcNextRun(now time.Time) time.Time {
	for days := 0; ; days++ {
		if slot := wallClock(now, days, d.hour, d.minute); slot.After(now) {
			return spreadStart(slot, d.spread)
		}
	}
}

func (d *DailyTrigger) Start(_ *taskmanager.ExecutionResult) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Drain any stale signal from a previous timer fire.
	select {
	case <-d.ch:
	default:
	}

	stopCh := make(chan struct{})
	d.nextRun = d.calcNextRun(time.Now())
	timer := time.NewTimer(time.Until(d.nextRun))
	d.stopCh = stopCh
	d.timer = timer

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
			case d.ch <- struct{}{}:
			default:
			}
		}
	}()
}

func (d *DailyTrigger) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopCh != nil {
		select {
		case <-d.stopCh:
		default:
			close(d.stopCh)
		}
	}
}

func (d *DailyTrigger) NextRunTime() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.nextRun
}

func (d *DailyTrigger) Config() taskmanager.TriggerConfig { return d.cfg }
func (d *DailyTrigger) C() <-chan struct{}                { return d.ch }
