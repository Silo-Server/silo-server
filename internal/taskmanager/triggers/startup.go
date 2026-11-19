package triggers

import (
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

const startupDelay = 5 * time.Second

// StartupTrigger fires once, shortly after Start() is called. Once it has
// fired, later calls to Start() (re-arming after execution) are no-ops. A
// Stop before it fires, such as the rearm after a run that was already going
// when this trigger was installed, leaves it unfired, so the next Start arms
// it again.
type StartupTrigger struct {
	cfg     taskmanager.TriggerConfig
	ch      chan struct{}
	delay   time.Duration
	nextRun time.Time
	stopCh  chan struct{}
	armed   bool
	fired   bool
	mu      sync.Mutex
}

func NewStartupTrigger(cfg taskmanager.TriggerConfig) *StartupTrigger {
	return &StartupTrigger{
		cfg:   cfg,
		ch:    make(chan struct{}, 1),
		delay: startupDelay,
	}
}

func (s *StartupTrigger) Start(_ *taskmanager.ExecutionResult) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Drain any stale signal from a previous timer fire.
	select {
	case <-s.ch:
	default:
	}

	if s.fired {
		s.nextRun = time.Time{}
		return
	}
	if s.armed {
		return
	}
	s.armed = true

	stopCh := make(chan struct{})
	s.nextRun = time.Now().Add(s.delay)
	timer := time.NewTimer(s.delay)
	s.stopCh = stopCh

	go func() {
		select {
		case <-stopCh:
			timer.Stop()
			return
		case <-timer.C:
			s.mu.Lock()
			select {
			case <-stopCh:
				// Stopped as the timer fired: not fired, so Start rearms it.
				s.mu.Unlock()
				return
			default:
			}
			s.armed = false
			s.fired = true
			s.nextRun = time.Time{}
			s.mu.Unlock()
			select {
			case s.ch <- struct{}{}:
			default:
			}
		}
	}()
}

func (s *StartupTrigger) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopCh != nil {
		select {
		case <-s.stopCh:
		default:
			close(s.stopCh)
		}
	}
	s.armed = false
	s.nextRun = time.Time{}
}

func (s *StartupTrigger) NextRunTime() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextRun
}

func (s *StartupTrigger) Config() taskmanager.TriggerConfig { return s.cfg }
func (s *StartupTrigger) C() <-chan struct{}                { return s.ch }
