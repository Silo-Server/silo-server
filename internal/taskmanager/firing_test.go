package taskmanager

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// testTask is the smallest task the trigger loop can run.
type testTask struct{}

func (testTask) Key() string                                     { return "test" }
func (testTask) Name() string                                    { return "test" }
func (testTask) Description() string                             { return "test" }
func (testTask) Category() TaskCategory                          { return TaskCategorySystem }
func (testTask) IsHidden() bool                                  { return false }
func (testTask) DefaultTriggers() []TriggerConfig                { return nil }
func (testTask) Execute(context.Context, ProgressReporter) error { return nil }

// testTrigger fires once each time it starts.
type testTrigger struct {
	ch chan struct{}
}

func newTestTrigger() *testTrigger {
	return &testTrigger{ch: make(chan struct{}, 1)}
}

func (t *testTrigger) Start(*ExecutionResult) {
	t.ch <- struct{}{}
}

func (t *testTrigger) Stop()                  {}
func (t *testTrigger) NextRunTime() time.Time { return time.Time{} }
func (t *testTrigger) Config() TriggerConfig  { return TriggerConfig{} }
func (t *testTrigger) C() <-chan struct{}     { return t.ch }

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

type claimTestExecutions struct{ latest *ExecutionResult }

func (claimTestExecutions) Insert(context.Context, ExecutionResult) error { return nil }
func (e claimTestExecutions) GetLatest(context.Context, string) (*ExecutionResult, error) {
	return e.latest, nil
}
func (claimTestExecutions) List(context.Context, string, int) ([]ExecutionResult, error) {
	return nil, nil
}

type recordingClaims struct {
	mu    sync.Mutex
	win   bool
	slots []time.Time
}

func (c *recordingClaims) ClaimFiring(_ context.Context, _ string, slot time.Time, _ time.Duration, _ time.Time) (bool, error) {
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
func (t claimingTask) Execute(ctx context.Context, _ ProgressReporter) error {
	select {
	case t.ran <- struct{}{}:
	case <-ctx.Done():
	}
	return nil
}

// A firing another server claimed is skipped, the trigger rearmed and the
// winner's run read from history as the last execution; a won claim runs.
func TestTriggerLoopRunsOnlyClaimedFirings(t *testing.T) {
	claims := &recordingClaims{}
	elsewhere := &ExecutionResult{TaskKey: "test", CompletedAt: time.Now()}
	m := New(claimTestTriggers{}, claimTestExecutions{latest: elsewhere}, func(TriggerConfig) Trigger { return newTestTrigger() }, nil)
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
		return configuredTrigger{testTrigger: newTestTrigger(), cfg: cfg}
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
	if got := w.info().LastExecution; got != elsewhere {
		t.Fatalf("last execution after lost claims = %v, want the run read from history", got)
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

// unregisteringTask is unregistered while its firing passes the preflight,
// as when a plugin task registry sync removes it at that moment.
type unregisteringTask struct {
	claimingTask
	m *TaskManager
}

func (t unregisteringTask) ShouldRun(context.Context) (bool, error) {
	t.m.Unregister(t.Key())
	return true, nil
}

// A worker retired after its trigger fired must not claim the firing: the
// claim would make every other server skip a firing that runs nowhere.
func TestRetiredWorkerDoesNotClaimFiring(t *testing.T) {
	claims := &recordingClaims{win: true}
	m := New(claimTestTriggers{}, claimTestExecutions{}, func(TriggerConfig) Trigger { return newTestTrigger() }, nil)
	m.SetFiringClaims(claims)
	task := unregisteringTask{claimingTask: claimingTask{ran: make(chan struct{}, 1)}, m: m}
	m.Register(task)
	w, err := m.getWorker(task.Key())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hourly := TriggerConfig{Type: TriggerTypeInterval, IntervalMs: 3_600_000}
	w.setTriggers([]TriggerConfig{hourly}, func(cfg TriggerConfig) Trigger {
		return configuredTrigger{testTrigger: newTestTrigger(), cfg: cfg}
	}, nil, false)
	loopDone := make(chan struct{})
	go func() {
		m.triggerLoop(ctx, w)
		close(loopDone)
	}() // the trigger fired when setTriggers started it

	select {
	case <-loopDone:
	case <-time.After(time.Second):
		t.Fatal("the retired worker's trigger loop did not end")
	}
	claims.mu.Lock()
	defer claims.mu.Unlock()
	if len(claims.slots) != 0 {
		t.Fatalf("retired worker claimed %d firings, want none", len(claims.slots))
	}
}

type gatedFiringClaim struct {
	entered chan struct{}
	release chan struct{}
}

func (c gatedFiringClaim) ClaimFiring(ctx context.Context, _ string, _ time.Time, _ time.Duration, _ time.Time) (bool, error) {
	close(c.entered)
	select {
	case <-c.release:
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

type oneShotTestTrigger struct{ configuredTrigger }

func (t oneShotTestTrigger) Start(*ExecutionResult) {}

func TestRetirementRecordsRunForClaimAlreadyInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		claim := gatedFiringClaim{entered: make(chan struct{}), release: make(chan struct{})}
		m := New(claimTestTriggers{}, claimTestExecutions{}, nil, nil)
		m.SetFiringClaims(claim)
		task := claimingTask{ran: make(chan struct{}, 1)}
		m.Register(task)
		w, err := m.getWorker(task.Key())
		if err != nil {
			t.Fatal(err)
		}
		trigger := oneShotTestTrigger{configuredTrigger{testTrigger: newTestTrigger(), cfg: TriggerConfig{Type: TriggerTypeStartup}}}
		w.setTriggers([]TriggerConfig{trigger.Config()}, func(TriggerConfig) Trigger { return trigger }, nil, false)
		go m.triggerLoop(ctx, w)
		trigger.ch <- struct{}{}
		<-claim.entered

		retired := make(chan struct{})
		go func() {
			m.Unregister(task.Key())
			close(retired)
		}()
		// A mutex waiter is not quiescent to synctest. If the claim left
		// admission open, let retirement finish before committing it.
		if w.scheduleMu.TryLock() {
			w.scheduleMu.Unlock()
			<-retired
		}
		close(claim.release)
		<-retired
		synctest.Wait()

		if w.info().LastExecution == nil {
			t.Fatal("claim committed without recording a scheduled execution")
		}
	})
}
