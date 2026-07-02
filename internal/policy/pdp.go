package policy

import (
	"context"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

// PDP exposes typed policy decisions over the generic Rego engine.
type PDP struct {
	engine         *Engine
	decisionLogger *DecisionLogger
}

// PDPOption configures a PDP.
type PDPOption func(*PDP)

// WithDecisionLogger configures asynchronous policy decision logging.
func WithDecisionLogger(logger *DecisionLogger) PDPOption {
	return func(p *PDP) {
		p.decisionLogger = logger
	}
}

// NewPDP creates a policy decision point from a compiled engine.
func NewPDP(engine *Engine, opts ...PDPOption) *PDP {
	pdp := &PDP{engine: engine}
	for _, opt := range opts {
		opt(pdp)
	}
	return pdp
}

// ResolveViewerScope resolves the effective viewer access scope.
func (p *PDP) ResolveViewerScope(ctx context.Context, input ScopeInput) (ScopeDecision, Meta, error) {
	var decision ScopeDecision
	meta, err := p.engine.Evaluate(ctx, DecisionScope, input, &decision)
	if err != nil {
		p.logScopeDecision(ctx, input, nil, meta, err)
		return ScopeDecision{}, Meta{}, err
	}
	p.logScopeDecision(ctx, input, decision, meta, nil)
	return decision, meta, nil
}

func (p *PDP) logScopeDecision(ctx context.Context, input ScopeInput, result any, meta Meta, evalErr error) {
	if p == nil || p.decisionLogger == nil {
		return
	}

	entry := Entry{
		DecisionName:     DecisionScope,
		PolicyGeneration: meta.Revision,
		UserID:           intPtr(input.UserID),
		ProfileID:        input.ProfileID,
		SessionID:        input.SessionID,
		RequestID:        chimiddleware.GetReqID(ctx),
		EvalTimeNS:       meta.EvalTimeNS,
	}
	if evalErr != nil {
		entry.Error = evalErr.Error()
	}
	p.decisionLogger.LogDecision(entry, input, result)
}

func intPtr(v int) *int {
	return &v
}
