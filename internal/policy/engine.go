package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/open-policy-agent/opa/v1/ast"
	"github.com/open-policy-agent/opa/v1/rego"
)

const defaultEvalTimeout = 25 * time.Millisecond

// DecisionName identifies a prepared policy decision query.
type DecisionName string

const (
	// DecisionScope resolves the effective viewer access scope.
	DecisionScope DecisionName = "silo.scope.decision"
	// DecisionPermission evaluates route-level permission gates.
	DecisionPermission DecisionName = "silo.permission.decision"
	// DecisionAction evaluates download and playback action gates.
	DecisionAction DecisionName = "silo.action.decision"
)

// Meta describes one policy evaluation.
type Meta struct {
	DecisionName DecisionName
	EvalTimeNS   int64
	Revision     int64
}

// SkippedSource records an enabled custom policy source that failed
// compilation and was left out of the compiled bundle. A bundle serving with
// skipped sources is strictly more permissive than the administrator intended.
type SkippedSource struct {
	Domain     string
	DocumentID int64
	Err        error
}

// Engine owns compiled Rego queries and evaluates named decisions.
type Engine struct {
	mu       sync.RWMutex
	queries  map[DecisionName]rego.PreparedEvalQuery
	timeout  time.Duration
	revision int64
	skipped  []SkippedSource
	logger   *slog.Logger

	evalTimeouts atomic.Int64
}

// EngineOption configures an Engine.
type EngineOption func(*Engine)

// WithEvalTimeout configures the per-decision evaluation timeout.
func WithEvalTimeout(timeout time.Duration) EngineOption {
	return func(engine *Engine) {
		if timeout > 0 {
			engine.timeout = timeout
		}
	}
}

// WithRevision records the policy generation loaded into the engine.
func WithRevision(revision int64) EngineOption {
	return func(engine *Engine) {
		engine.revision = revision
	}
}

// WithLogger configures the logger used for degraded policy reload warnings.
func WithLogger(logger *slog.Logger) EngineOption {
	return func(engine *Engine) {
		if logger != nil {
			engine.logger = logger
		}
	}
}

// NewEngine compiles the embedded vendor policy bundle.
func NewEngine(ctx context.Context, opts ...EngineOption) (*Engine, error) {
	engine := newEngine(opts...)
	modules, err := vendorModules(false)
	if err != nil {
		return nil, err
	}
	if err := engine.swap(ctx, modules, decisionQueries(), engine.revision); err != nil {
		return nil, err
	}
	return engine, nil
}

// NewEngineWithCustom compiles the embedded vendor policy bundle layered with
// active administrator-authored policy sources. Invalid custom sources are
// skipped so a bad row never takes down vendor policy decisions at boot, but
// every skip is recorded on the engine (see SkippedSources) and logged at
// Error level: the resulting bundle is more permissive than the stored policy.
func NewEngineWithCustom(ctx context.Context, sources map[string]ActiveSource, opts ...EngineOption) (*Engine, error) {
	engine := newEngine(opts...)
	modules, skipped, err := engine.modulesWithCustom(ctx, sources)
	if err != nil {
		return nil, err
	}
	if err := engine.swap(ctx, modules, decisionQueries(), engine.revision); err != nil {
		return nil, err
	}
	engine.setSkipped(skipped)
	return engine, nil
}

// NewEngineFromStore loads active custom policy sources and generation from the
// store, then compiles an engine from that snapshot.
func NewEngineFromStore(ctx context.Context, store *PolicyStore, opts ...EngineOption) (*Engine, error) {
	sources, err := store.ActiveSources(ctx)
	if err != nil {
		return nil, err
	}
	generation, err := store.Generation(ctx)
	if err != nil {
		return nil, err
	}
	opts = append(opts, WithRevision(generation))
	return NewEngineWithCustom(ctx, sources, opts...)
}

// Reload compiles a new bundle from vendor policy plus active custom sources,
// then atomically swaps prepared queries and revision. Unlike boot, Reload is
// strict: any enabled custom source that fails compilation fails the whole
// reload so the last known-good bundle keeps serving — a silent per-domain
// skip would widen decisions while the generation reports fully applied.
func (e *Engine) Reload(ctx context.Context, sources map[string]ActiveSource, generation int64) error {
	modules, skipped, err := e.modulesWithCustom(ctx, sources)
	if err != nil {
		return err
	}
	if len(skipped) > 0 {
		errs := make([]error, 0, len(skipped))
		for _, skip := range skipped {
			errs = append(errs, fmt.Errorf("custom policy source for domain %q (document %d) failed compilation: %w", skip.Domain, skip.DocumentID, skip.Err))
		}
		return errors.Join(errs...)
	}
	if err := e.swap(ctx, modules, decisionQueries(), generation); err != nil {
		return err
	}
	e.setSkipped(nil)
	return nil
}

// Revision returns the policy generation loaded into this engine.
func (e *Engine) Revision() int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.revision
}

// EvalTimeouts returns how many evaluations have exceeded the eval budget
// since this engine was constructed.
func (e *Engine) EvalTimeouts() int64 {
	return e.evalTimeouts.Load()
}

// SkippedSources returns the enabled custom sources that were dropped from the
// currently loaded bundle. Non-empty means the engine serves degraded (more
// permissive than stored policy); only boot-time loading can produce skips.
func (e *Engine) SkippedSources() []SkippedSource {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]SkippedSource(nil), e.skipped...)
}

func (e *Engine) setSkipped(skipped []SkippedSource) {
	e.mu.Lock()
	e.skipped = append([]SkippedSource(nil), skipped...)
	e.mu.Unlock()
}

// SetEvalTimeout updates the per-decision evaluation timeout. Non-positive
// durations are ignored.
func (e *Engine) SetEvalTimeout(timeout time.Duration) {
	if timeout <= 0 {
		return
	}
	e.mu.Lock()
	e.timeout = timeout
	e.mu.Unlock()
}

// Evaluate evaluates a prepared decision and decodes the result into out.
func (e *Engine) Evaluate(ctx context.Context, name DecisionName, input any, out any) (Meta, error) {
	e.mu.RLock()
	query, ok := e.queries[name]
	timeout := e.timeout
	revision := e.revision
	e.mu.RUnlock()
	if !ok {
		return Meta{}, fmt.Errorf("%w: %s", ErrUnknownDecision, name)
	}

	evalCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	resultSet, err := query.Eval(evalCtx, rego.EvalInput(input))
	meta := Meta{
		DecisionName: name,
		EvalTimeNS:   time.Since(start).Nanoseconds(),
		Revision:     revision,
	}
	if err != nil {
		// A timeout is still fail-closed (ErrPolicyEvalFailed matches), but it
		// gets its own sentinel, counter, and Error log: a slow policy denies
		// every request on the hot path and must be attributable.
		if errors.Is(evalCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			e.evalTimeouts.Add(1)
			e.logger.ErrorContext(ctx, "policy evaluation timed out",
				"decision", string(name), "timeout", timeout, "total_timeouts", e.evalTimeouts.Load())
			return meta, fmt.Errorf("%w: %w after %s: %w", ErrPolicyEvalFailed, ErrPolicyEvalTimeout, timeout, err)
		}
		return meta, fmt.Errorf("%w: %w", ErrPolicyEvalFailed, err)
	}
	if len(resultSet) == 0 || len(resultSet[0].Expressions) == 0 {
		// Vendor policies index required input fields directly, so a partial
		// input document (e.g. a hand-written simulate payload) yields an
		// undefined decision rather than an eval error.
		return meta, fmt.Errorf("%w: decision %s is undefined for this input (missing required input fields?)", ErrPolicyEvalFailed, name)
	}
	raw, err := json.Marshal(resultSet[0].Expressions[0].Value)
	if err != nil {
		return meta, fmt.Errorf("%w: encoding result for %s: %w", ErrPolicyEvalFailed, name, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return meta, fmt.Errorf("%w: decoding result for %s: %w", ErrPolicyEvalFailed, name, err)
	}
	return meta, nil
}

func (e *Engine) swap(ctx context.Context, modules []ModuleSource, decisions map[DecisionName]string, revision int64) error {
	queries := make(map[DecisionName]rego.PreparedEvalQuery, len(decisions))
	for name, query := range decisions {
		options := []func(*rego.Rego){
			rego.Query(query),
			// Same sandbox as CompileCheck: a stored source must not gain
			// builtins at runtime that save-time validation would reject.
			rego.Capabilities(LockedCapabilities()),
		}
		for _, module := range modules {
			options = append(options, rego.Module(module.Path, module.Source))
		}
		prepared, err := rego.New(options...).PrepareForEval(ctx)
		if err != nil {
			return compileErrorFromOPA(err)
		}
		queries[name] = prepared
	}

	e.mu.Lock()
	e.queries = queries
	e.revision = revision
	e.mu.Unlock()
	return nil
}

func decisionQueries() map[DecisionName]string {
	return map[DecisionName]string{
		DecisionScope:      "data.silo.scope.decision",
		DecisionPermission: "data.silo.permission.decision",
		DecisionAction:     "data.silo.action.decision",
	}
}

func newEngineFromModules(ctx context.Context, timeout time.Duration, modules []ModuleSource, decisions map[DecisionName]string) (*Engine, error) {
	engine := newEngine(WithEvalTimeout(timeout))
	if err := engine.swap(ctx, modules, decisions, engine.revision); err != nil {
		return nil, err
	}
	return engine, nil
}

func newEngine(opts ...EngineOption) *Engine {
	engine := &Engine{
		timeout: defaultEvalTimeout,
		logger:  slog.Default(),
	}
	for _, opt := range opts {
		opt(engine)
	}
	return engine
}

func sortedActiveSourceDomains(sources map[string]ActiveSource) []string {
	domains := make([]string, 0, len(sources))
	for domain := range sources {
		domains = append(domains, domain)
	}
	sort.Strings(domains)
	return domains
}

func (e *Engine) modulesWithCustom(ctx context.Context, sources map[string]ActiveSource) ([]ModuleSource, []SkippedSource, error) {
	modules, err := vendorModules(false)
	if err != nil {
		return nil, nil, err
	}
	var skipped []SkippedSource
	for _, domain := range sortedActiveSourceDomains(sources) {
		source := sources[domain]
		if err := CompileCheck(ctx, domain, source.Source); err != nil {
			e.logSkippedCustomSource(ctx, domain, source, err)
			skipped = append(skipped, SkippedSource{
				Domain:     domain,
				DocumentID: source.DocumentID,
				Err:        err,
			})
			continue
		}
		modules = append(modules, ModuleSource{
			Path:   customModulePath(domain),
			Source: source.Source,
		})
	}
	return modules, skipped, nil
}

func (e *Engine) logSkippedCustomSource(ctx context.Context, domain string, source ActiveSource, err error) {
	fields := []any{
		"domain", domain,
		"error", err,
	}
	if source.DocumentID != 0 {
		fields = append(fields, "document_id", source.DocumentID)
	}
	// Error, not Warn: a skipped source means requests are being decided by a
	// more permissive bundle than the administrator activated.
	e.logger.ErrorContext(ctx, "skipping invalid custom policy source", fields...)
}

func compileErrorFromOPA(err error) error {
	if err == nil {
		return nil
	}
	var astErrors ast.Errors
	if errors.As(err, &astErrors) {
		issues := make([]CompileIssue, 0, len(astErrors))
		for _, astErr := range astErrors {
			issue := CompileIssue{Message: astErr.Message}
			if astErr.Location != nil {
				issue.Row = astErr.Location.Row
				issue.Col = astErr.Location.Col
			}
			issues = append(issues, issue)
		}
		return &CompileError{Issues: issues}
	}
	return &CompileError{Issues: []CompileIssue{{Message: err.Error()}}}
}
