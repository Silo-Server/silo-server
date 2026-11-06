package taskmanager

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestFiringSlot(t *testing.T) {
	at := func(day, hour, minute, second int) time.Time {
		// 2026-11-01 is a Sunday.
		return time.Date(2026, time.November, day, hour, minute, second, 0, time.UTC)
	}
	daily := TriggerConfig{Type: TriggerTypeDaily, TimeOfDay: "03:00"}
	sunday := TriggerConfig{Type: TriggerTypeWeekly, TimeOfDay: "23:30", DayOfWeek: int(time.Sunday)}
	hourly := TriggerConfig{Type: TriggerTypeInterval, IntervalMs: time.Hour.Milliseconds()}
	for _, tc := range []struct {
		name    string
		trigger TriggerConfig
		firedAt time.Time
		slot    time.Time
		window  time.Duration
		noSlot  bool
	}{
		{"daily on time", daily, at(6, 3, 0, 0), at(6, 3, 0, 0), 0, false},
		{"daily timer late", daily, at(6, 3, 4, 10), at(6, 3, 0, 0), 0, false},
		{"daily clock slightly behind the slot", daily, at(6, 2, 59, 58), at(6, 3, 0, 0), 0, false},
		{"weekly late into Monday", sunday, at(9, 0, 10, 0), at(8, 23, 30, 0), 0, false},
		{"weekly on the day", sunday, at(8, 23, 30, 5), at(8, 23, 30, 0), 0, false},
		{"interval", hourly, at(6, 10, 59, 59), at(6, 10, 0, 0), 59 * time.Minute, false},
		{"interval after the boundary", hourly, at(6, 11, 0, 2), at(6, 11, 0, 0), 59 * time.Minute, false},
		{"short interval", TriggerConfig{Type: TriggerTypeInterval, IntervalMs: 60_000}, at(6, 10, 0, 30), at(6, 10, 0, 0), 54 * time.Second, false},
		{"startup", TriggerConfig{Type: TriggerTypeStartup}, at(6, 10, 0, 30), at(6, 10, 0, 30), StartupClaimWindow, false},
		// Servers would each claim their own firing time: no slot.
		{"malformed time of day", TriggerConfig{Type: TriggerTypeDaily, TimeOfDay: "3am"}, at(6, 3, 0, 0), time.Time{}, 0, true},
		{"interval not positive", TriggerConfig{Type: TriggerTypeInterval}, at(6, 3, 0, 0), time.Time{}, 0, true},
		{"no trigger", TriggerConfig{}, at(6, 3, 0, 0), time.Time{}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slot, window, ok := FiringSlot(tc.trigger, tc.firedAt)
			if ok == tc.noSlot || !slot.Equal(tc.slot) || window != tc.window {
				t.Fatalf("FiringSlot = %s, %s, %v; want %s, %s, %v", slot, window, ok, tc.slot, tc.window, !tc.noSlot)
			}
		})
	}
}

type claimTestTriggers struct{}

func (claimTestTriggers) GetTriggers(context.Context, string) ([]TriggerConfig, error) {
	return []TriggerConfig{}, nil
}
func (claimTestTriggers) GetOrCreateTriggers(context.Context, string, []TriggerConfig) ([]TriggerConfig, error) {
	return []TriggerConfig{}, nil
}
func (claimTestTriggers) SetTriggers(context.Context, string, []TriggerConfig) error { return nil }

type claimTestExecutions struct{}

func (claimTestExecutions) Insert(context.Context, ExecutionResult) error { return nil }
func (claimTestExecutions) GetLatest(context.Context, string) (*ExecutionResult, error) {
	return nil, nil
}
func (claimTestExecutions) List(context.Context, string, int) ([]ExecutionResult, error) {
	return nil, nil
}

type recordingClaims struct {
	mu    sync.Mutex
	win   bool
	slots []time.Time
}

func (c *recordingClaims) ClaimFiring(_ context.Context, _ string, slot time.Time, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slots = append(c.slots, slot)
	return c.win, nil
}

// configuredTrigger is a test trigger that reports its config, which a
// firing claim reads to name the slot.
type configuredTrigger struct {
	*testTrigger
	cfg TriggerConfig
}

func (t configuredTrigger) Config() TriggerConfig { return t.cfg }

type claimingTask struct {
	testTask
	ran chan struct{}
}

func (claimingTask) ClaimsFirings() bool { return true }
func (t claimingTask) Execute(context.Context, ProgressReporter) error {
	t.ran <- struct{}{}
	return nil
}

// A firing another server claimed is skipped and the trigger rearmed; a won
// claim runs.
func TestTriggerLoopRunsOnlyClaimedFirings(t *testing.T) {
	claims := &recordingClaims{}
	m := New(claimTestTriggers{}, claimTestExecutions{}, newTestTrigger, nil)
	m.SetFiringClaims(claims)
	task := claimingTask{ran: make(chan struct{}, 1)}
	m.Register(task)
	w, err := m.getWorker(task.Key())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	configured := func(cfg TriggerConfig) Trigger {
		return configuredTrigger{testTrigger: newTestTrigger(cfg).(*testTrigger), cfg: cfg}
	}
	w.setTriggers([]TriggerConfig{{Type: TriggerTypeInterval, IntervalMs: 20}}, configured, nil, false)
	go m.triggerLoop(ctx, w)

	deadline := time.After(time.Second)
	for {
		claims.mu.Lock()
		asked := len(claims.slots)
		claims.mu.Unlock()
		if asked >= 2 {
			break
		}
		select {
		case <-task.ran:
			t.Fatal("a firing another server claimed ran here")
		case <-deadline:
			t.Fatal("lost claims did not rearm the trigger")
		case <-time.After(5 * time.Millisecond):
		}
	}
	claims.mu.Lock()
	claims.win = true
	claims.mu.Unlock()
	select {
	case <-task.ran:
	case <-time.After(time.Second):
		t.Fatal("a won claim did not run the task")
	}
}
