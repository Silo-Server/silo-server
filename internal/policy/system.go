package policy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
)

const defaultSystemPollInterval = 60 * time.Second

// System owns the live policy engine, reloads it when policy documents change,
// and exposes a stable PDP pointer for request-path adapters.
type System struct {
	store    *PolicyStore
	eventBus cache.EventBus
	logger   *slog.Logger

	pollInterval time.Duration
	evalTimeout  time.Duration

	mu       sync.RWMutex
	engine   *Engine
	pdp      *PDP
	cancel   context.CancelFunc
	reloadCh chan struct{}
	wg       sync.WaitGroup
}

// SystemOption configures a System.
type SystemOption func(*System)

// WithSystemPollInterval configures how often the poll fallback checks policy
// generation. Non-positive durations keep the default.
func WithSystemPollInterval(interval time.Duration) SystemOption {
	return func(system *System) {
		if interval > 0 {
			system.pollInterval = interval
		}
	}
}

// WithSystemEvalTimeout configures the per-decision policy evaluation timeout.
// Non-positive durations keep the default.
func WithSystemEvalTimeout(timeout time.Duration) SystemOption {
	return func(system *System) {
		if timeout > 0 {
			system.evalTimeout = timeout
		}
	}
}

// NewSystem constructs a policy System. Call Start before using PDP.
func NewSystem(store *PolicyStore, eventBus cache.EventBus, logger *slog.Logger, opts ...SystemOption) *System {
	if logger == nil {
		logger = slog.Default()
	}
	system := &System{
		store:        store,
		eventBus:     eventBus,
		logger:       logger.With("component", "policy.system"),
		pollInterval: defaultSystemPollInterval,
		evalTimeout:  defaultEvalTimeout,
		reloadCh:     make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(system)
	}
	return system
}

// Start loads the initial engine, subscribes to policy-change events, and
// starts the poll fallback loop.
func (s *System) Start(ctx context.Context) error {
	if s == nil {
		return nil
	}

	engine, err := s.initialEngine(ctx)
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.engine != nil {
		s.mu.Unlock()
		cancel()
		return errors.New("policy system already started")
	}
	s.engine = engine
	s.pdp = NewPDP(engine)
	s.cancel = cancel
	s.mu.Unlock()

	if s.eventBus != nil {
		if err := s.eventBus.Subscribe(runCtx, cache.ChannelAdmin, func(event cache.Event) {
			if event.Type == cache.EventPolicyChanged {
				s.requestReload()
			}
		}); err != nil {
			s.logger.WarnContext(ctx, "policy system: subscribe to admin channel failed, using poll-only mode", "error", err)
		}
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.poll(runCtx)
	}()
	return nil
}

// Stop cancels background work and waits for it to exit.
func (s *System) Stop() {
	if s == nil {
		return
	}
	s.mu.RLock()
	cancel := s.cancel
	s.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	s.Wait()
}

// Wait blocks until background reload loops exit.
func (s *System) Wait() {
	if s != nil {
		s.wg.Wait()
	}
}

// PDP returns the live policy decision point. The returned PDP remains valid
// across engine reloads because the System mutates one Engine in place.
func (s *System) PDP() *PDP {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pdp
}

// SetEvalTimeout hot-updates the per-decision policy evaluation timeout.
func (s *System) SetEvalTimeout(timeout time.Duration) {
	if s == nil || timeout <= 0 {
		return
	}
	s.mu.Lock()
	s.evalTimeout = timeout
	engine := s.engine
	s.mu.Unlock()
	if engine != nil {
		engine.SetEvalTimeout(timeout)
	}
}

// NotifyChanged reloads this node synchronously and publishes a cross-node
// invalidation event. The last known-good engine remains active on reload
// failure.
func (s *System) NotifyChanged(ctx context.Context) error {
	if s == nil {
		return nil
	}

	var errs []error
	if err := s.reloadFromStore(ctx); err != nil {
		s.logger.ErrorContext(ctx, "policy reload after local change failed", "error", err)
		errs = append(errs, err)
	}
	if s.eventBus != nil {
		if err := s.eventBus.Publish(ctx, cache.ChannelAdmin, cache.Event{Type: cache.EventPolicyChanged}); err != nil {
			s.logger.ErrorContext(ctx, "policy change publish failed", "error", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *System) initialEngine(ctx context.Context) (*Engine, error) {
	sources, generation, err := s.loadSnapshot(ctx)
	if err != nil {
		s.logger.ErrorContext(ctx, "policy store unavailable; starting with vendor policy only", "error", err)
		return s.newVendorEngine(ctx)
	}

	engine, err := NewEngineWithCustom(ctx, sources, s.engineOptions(WithRevision(generation))...)
	if err != nil {
		s.logger.ErrorContext(ctx, "policy custom bundle load failed; starting with vendor policy only", "error", err)
		return s.newVendorEngine(ctx)
	}
	return engine, nil
}

func (s *System) newVendorEngine(ctx context.Context) (*Engine, error) {
	engine, err := NewEngine(ctx, s.engineOptions()...)
	if err != nil {
		return nil, fmt.Errorf("compile vendor policy: %w", err)
	}
	return engine, nil
}

func (s *System) engineOptions(extra ...EngineOption) []EngineOption {
	opts := []EngineOption{
		WithLogger(s.logger),
		WithEvalTimeout(s.evalTimeout),
	}
	return append(opts, extra...)
}

func (s *System) reloadFromStore(ctx context.Context) error {
	sources, generation, err := s.loadSnapshot(ctx)
	if err != nil {
		return err
	}

	s.mu.RLock()
	engine := s.engine
	s.mu.RUnlock()
	if engine == nil {
		return errors.New("policy system is not started")
	}
	if err := engine.Reload(ctx, sources, generation); err != nil {
		return err
	}
	s.logger.InfoContext(ctx, "policy engine reloaded", "generation", generation)
	return nil
}

func (s *System) loadSnapshot(ctx context.Context) (map[string]ActiveSource, int64, error) {
	if s.store == nil {
		return nil, 0, errors.New("policy store is nil")
	}

	for {
		before, err := s.store.Generation(ctx)
		if err != nil {
			return nil, 0, err
		}
		sources, err := s.store.ActiveSources(ctx)
		if err != nil {
			return nil, 0, err
		}
		after, err := s.store.Generation(ctx)
		if err != nil {
			return nil, 0, err
		}
		if before == after {
			return sources, after, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
	}
}

func (s *System) requestReload() {
	select {
	case s.reloadCh <- struct{}{}:
	default:
	}
}

func (s *System) poll(ctx context.Context) {
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reloadIfGenerationChanged(ctx)
		case <-s.reloadCh:
			if err := s.reloadFromStore(ctx); err != nil {
				s.logger.ErrorContext(ctx, "policy event reload failed", "error", err)
			}
		}
	}
}

func (s *System) reloadIfGenerationChanged(ctx context.Context) {
	if s.store == nil {
		return
	}
	generation, err := s.store.Generation(ctx)
	if err != nil {
		s.logger.ErrorContext(ctx, "policy generation poll failed", "error", err)
		return
	}

	s.mu.RLock()
	engine := s.engine
	s.mu.RUnlock()
	if engine == nil || generation == engine.Revision() {
		return
	}
	if err := s.reloadFromStore(ctx); err != nil {
		s.logger.ErrorContext(ctx, "policy poll reload failed", "error", err)
	}
}
