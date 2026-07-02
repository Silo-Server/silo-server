package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	mu      sync.RWMutex
	queries map[DecisionName]rego.PreparedEvalQuery
	timeout time.Duration
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

// NewEngine compiles the embedded vendor policy bundle.
func NewEngine(ctx context.Context, opts ...EngineOption) (*Engine, error) {
	engine := &Engine{timeout: defaultEvalTimeout}
	for _, opt := range opts {
		opt(engine)
	}
	modules, err := vendorModules(false)
	if err != nil {
		return nil, err
	}
	if err := engine.swap(ctx, modules, scopeQueries()); err != nil {
		return nil, err
	}
	return engine, nil
}

// Evaluate evaluates a prepared decision and decodes the result into out.
func (e *Engine) Evaluate(ctx context.Context, name DecisionName, input any, out any) (Meta, error) {
	e.mu.RLock()
	query, ok := e.queries[name]
	timeout := e.timeout
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
	engine := &Engine{timeout: timeout}
	if engine.timeout <= 0 {
		engine.timeout = defaultEvalTimeout
	}
	if err := engine.swap(ctx, modules, decisions); err != nil {
		return nil, err
	}
	return engine, nil
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
