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
	store          *PolicyStore
	eventBus       cache.EventBus
	logger         *slog.Logger
	decisionLogger *DecisionLogger

	pollInterval time.Duration
	evalTimeout  time.Duration

	mu       sync.RWMutex
	engine   *Engine
	pdp      *PDP
	cancel   context.CancelFunc
	reloadCh chan struct{}
	wg       sync.WaitGroup

	// bootDegradedReason is set when the initial engine could not load the
	// full stored policy (store unreachable or custom bundle failed) and is
	// cleared by the first successful reload from the store.
	bootDegradedReason string
}

// Degraded-state reasons reported by DegradedState.
const (
	DegradedReasonStoreUnavailable    = "store_unavailable"
	DegradedReasonCustomBundleFailed  = "custom_bundle_failed"
	DegradedReasonCustomSourceInvalid = "custom_source_invalid"
)

// DegradedState reports whether the live engine is serving with less than the
// full set of enabled custom policy sources. Because custom policy is
// tighten-only, a degraded engine is more permissive than administrator intent.
type DegradedState struct {
	Degraded bool
	Reason   string
	// Domains lists policy domains whose enabled custom source was dropped
	// from the loaded bundle (empty when the whole store was unavailable).
	Domains []string
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

// WithSystemDecisionLogger configures the async logger used by the PDP.
func WithSystemDecisionLogger(logger *DecisionLogger) SystemOption {
	return func(system *System) {
		system.decisionLogger = logger
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

	engine, bootDegradedReason, err := s.initialEngine(ctx)
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
	s.bootDegradedReason = bootDegradedReason
	s.pdp = NewPDP(engine, WithDecisionLogger(s.decisionLogger))
	s.cancel = cancel
	s.mu.Unlock()

	if s.decisionLogger != nil {
		s.decisionLogger.Start(runCtx)
	}

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
	if s.decisionLogger != nil {
		s.decisionLogger.Stop()
	}
	s.Wait()
}

// Wait blocks until background reload loops exit.
func (s *System) Wait() {
	if s != nil {
		s.wg.Wait()
		if s.decisionLogger != nil {
			s.decisionLogger.Wait()
		}
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

// DecisionLogger returns the async decision logger attached to the System.
func (s *System) DecisionLogger() *DecisionLogger {
	if s == nil {
		return nil
	}
	return s.decisionLogger
}

// Generation returns the policy generation currently loaded in the live engine.
func (s *System) Generation() int64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	engine := s.engine
	s.mu.RUnlock()
	if engine == nil {
		return 0
	}
	return engine.Revision()
}

// DegradedState reports whether the live engine dropped any enabled custom
// policy (boot-time store outage, custom bundle failure, or per-source compile
// skips). A degraded engine serves more permissive decisions than the stored
// policy, so operators must be able to see it.
func (s *System) DegradedState() DegradedState {
	if s == nil {
		return DegradedState{}
	}
	s.mu.RLock()
	engine := s.engine
	bootReason := s.bootDegradedReason
	s.mu.RUnlock()

	if bootReason != "" {
		return DegradedState{Degraded: true, Reason: bootReason}
	}
	if engine == nil {
		return DegradedState{}
	}
	skipped := engine.SkippedSources()
	if len(skipped) == 0 {
		return DegradedState{}
	}
	domains := make([]string, 0, len(skipped))
	for _, skip := range skipped {
		domains = append(domains, skip.Domain)
	}
	return DegradedState{
		Degraded: true,
		Reason:   DegradedReasonCustomSourceInvalid,
		Domains:  domains,
	}
}

// EvalTimeout returns the configured per-decision evaluation budget, used by
// the activation-time cost guard so it measures against the live budget.
func (s *System) EvalTimeout() time.Duration {
	if s == nil {
		return defaultEvalTimeout
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.evalTimeout
}

// EvalTimeouts returns how many evaluations exceeded the eval budget on the
// live engine since boot.
func (s *System) EvalTimeouts() int64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	engine := s.engine
	s.mu.RUnlock()
	if engine == nil {
		return 0
	}
	return engine.EvalTimeouts()
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

// ApplyStatus describes how a policy change was applied: whether this node's
// engine reloaded to the stored generation and whether peers were notified.
type ApplyStatus struct {
	LocalReloadErr error
	PublishErr     error
	// Generation is the generation loaded in the live engine after the apply
	// attempt — it matches the stored generation only when Applied.
	Generation int64
}

// Applied reports whether the local engine now serves the stored policy.
func (st ApplyStatus) Applied() bool { return st.LocalReloadErr == nil }

// FailedStep names the first failed apply step ("local_reload" or
// "event_publish"), or "" when both steps succeeded.
func (st ApplyStatus) FailedStep() string {
	switch {
	case st.LocalReloadErr != nil:
		return "local_reload"
	case st.PublishErr != nil:
		return "event_publish"
	}
	return ""
}

// Err joins the per-step errors, nil when the change fully applied.
func (st ApplyStatus) Err() error { return errors.Join(st.LocalReloadErr, st.PublishErr) }

// NotifyChanged reloads this node synchronously and publishes a cross-node
// invalidation event. The last known-good engine remains active on reload
// failure.
func (s *System) NotifyChanged(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.ApplyChanged(ctx).Err()
}

// ApplyChanged reloads this node synchronously, publishes a cross-node
// invalidation event, and reports per-step outcomes so callers can distinguish
// "persisted" from "live". The last known-good engine remains active on reload
// failure.
func (s *System) ApplyChanged(ctx context.Context) ApplyStatus {
	if s == nil {
		return ApplyStatus{}
	}

	var status ApplyStatus
	if err := s.reloadFromStore(ctx); err != nil {
		s.logger.ErrorContext(ctx, "policy reload after local change failed", "error", err)
		status.LocalReloadErr = err
	}
	if s.eventBus != nil {
		if err := s.eventBus.Publish(ctx, cache.ChannelAdmin, cache.Event{Type: cache.EventPolicyChanged}); err != nil {
			s.logger.ErrorContext(ctx, "policy change publish failed", "error", err)
			status.PublishErr = err
		}
	}
	status.Generation = s.Generation()
	return status
}

func (s *System) initialEngine(ctx context.Context) (*Engine, string, error) {
	sources, generation, err := s.loadSnapshot(ctx)
	if err != nil {
		s.logger.ErrorContext(ctx, "policy store unavailable; starting DEGRADED with vendor policy only", "error", err)
		engine, err := s.newVendorEngine(ctx)
		return engine, DegradedReasonStoreUnavailable, err
	}

	engine, err := NewEngineWithCustom(ctx, sources, s.engineOptions(WithRevision(generation))...)
	if err != nil {
		s.logger.ErrorContext(ctx, "policy custom bundle load failed; starting DEGRADED with vendor policy only", "error", err)
		engine, err := s.newVendorEngine(ctx)
		return engine, DegradedReasonCustomBundleFailed, err
	}
	return engine, "", nil
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
	s.mu.Lock()
	s.bootDegradedReason = ""
	s.mu.Unlock()
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
