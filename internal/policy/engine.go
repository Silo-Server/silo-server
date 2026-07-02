package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
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
)

// Meta describes one policy evaluation.
type Meta struct {
	DecisionName DecisionName
	EvalTimeNS   int64
	Revision     int64
}

// Engine owns compiled Rego queries and evaluates named decisions.
type Engine struct {
	mu       sync.RWMutex
	queries  map[DecisionName]rego.PreparedEvalQuery
	timeout  time.Duration
	revision int64
	logger   *slog.Logger
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
	if err := engine.swap(ctx, modules, scopeQueries()); err != nil {
		return nil, err
	}
	return engine, nil
}

// NewEngineWithCustom compiles the embedded vendor policy bundle layered with
// active administrator-authored policy sources. Invalid custom sources are
// skipped with a warning so a bad row never takes down vendor policy decisions.
func NewEngineWithCustom(ctx context.Context, sources map[string]ActiveSource, opts ...EngineOption) (*Engine, error) {
	engine := newEngine(opts...)
	modules, err := vendorModules(false)
	if err != nil {
		return nil, err
	}
	for _, domain := range sortedActiveSourceDomains(sources) {
		source := sources[domain]
		if err := CompileCheck(ctx, domain, source.Source); err != nil {
			engine.warnSkippedCustomSource(ctx, domain, source, err)
			continue
		}
		modules = append(modules, ModuleSource{
			Path:   customModulePath(domain),
			Source: source.Source,
		})
	}
	if err := engine.swap(ctx, modules, scopeQueries()); err != nil {
		return nil, err
	}
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
		return Meta{}, fmt.Errorf("%w: %w", ErrPolicyEvalFailed, err)
	}
	if len(resultSet) == 0 || len(resultSet[0].Expressions) == 0 {
		return Meta{}, fmt.Errorf("%w: empty result for %s", ErrPolicyEvalFailed, name)
	}
	raw, err := json.Marshal(resultSet[0].Expressions[0].Value)
	if err != nil {
		return Meta{}, fmt.Errorf("%w: encoding result for %s: %w", ErrPolicyEvalFailed, name, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return Meta{}, fmt.Errorf("%w: decoding result for %s: %w", ErrPolicyEvalFailed, name, err)
	}
	return meta, nil
}

func (e *Engine) swap(ctx context.Context, modules []ModuleSource, decisions map[DecisionName]string) error {
	queries := make(map[DecisionName]rego.PreparedEvalQuery, len(decisions))
	for name, query := range decisions {
		options := []func(*rego.Rego){
			rego.Query(query),
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
	e.mu.Unlock()
	return nil
}

func scopeQueries() map[DecisionName]string {
	return map[DecisionName]string{
		DecisionScope: "data.silo.scope.decision",
	}
}

func newEngineFromModules(ctx context.Context, timeout time.Duration, modules []ModuleSource, decisions map[DecisionName]string) (*Engine, error) {
	engine := newEngine(WithEvalTimeout(timeout))
	if err := engine.swap(ctx, modules, decisions); err != nil {
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

func (e *Engine) warnSkippedCustomSource(ctx context.Context, domain string, source ActiveSource, err error) {
	fields := []any{
		"domain", domain,
		"error", err,
	}
	if source.DocumentID != 0 {
		fields = append(fields, "document_id", source.DocumentID)
	}
	e.logger.WarnContext(ctx, "skipping invalid custom policy source", fields...)
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
