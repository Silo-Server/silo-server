package triggers

import (
	"fmt"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// WeeklyTrigger fires at a specific time on a specific day of the week.
type WeeklyTrigger struct {
	cfg       taskmanager.TriggerConfig
	dayOfWeek time.Weekday
	hour      int
	minute    int
	ch        chan struct{}
	nextRun   time.Time
	timer     *time.Timer
	stopCh    chan struct{}
	mu        sync.Mutex
}

func NewWeeklyTrigger(cfg taskmanager.TriggerConfig) *WeeklyTrigger {
	var h, m int
	fmt.Sscanf(cfg.TimeOfDay, "%d:%d", &h, &m)
	return &WeeklyTrigger{
		cfg:       cfg,
		dayOfWeek: time.Weekday(cfg.DayOfWeek),
		hour:      h,
		minute:    m,
		ch:        make(chan struct{}, 1),
	}
}

func (w *WeeklyTrigger) calcNextRun(now time.Time) time.Time {
	daysAhead := (int(w.dayOfWeek) - int(now.Weekday()) + 7) % 7
	target := time.Date(now.Year(), now.Month(), now.Day()+daysAhead, w.hour, w.minute, 0, 0, now.Location())
	if daysAhead == 0 && !target.After(now) {
		target = target.Add(7 * 24 * time.Hour)
	}
	return target
}

func (w *WeeklyTrigger) Start(_ *taskmanager.ExecutionResult) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Drain any stale signal from a previous timer fire.
	select {
	case <-w.ch:
	default:
	}

	stopCh := make(chan struct{})
	w.nextRun = w.calcNextRun(time.Now())
	timer := time.NewTimer(time.Until(w.nextRun))
	w.stopCh = stopCh
	w.timer = timer

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
			case w.ch <- struct{}{}:
			default:
			}
		}
	}()
}

func (w *WeeklyTrigger) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopCh != nil {
		select {
		case <-w.stopCh:
		default:
			close(w.stopCh)
		}
	}
}

func (w *WeeklyTrigger) NextRunTime() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.nextRun
}

func (w *WeeklyTrigger) Config() taskmanager.TriggerConfig { return w.cfg }
func (w *WeeklyTrigger) C() <-chan struct{}                { return w.ch }
