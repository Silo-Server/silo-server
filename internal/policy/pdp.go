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

// CheckPermission evaluates a route-level permission gate.
func (p *PDP) CheckPermission(ctx context.Context, input PermissionInput) (PermissionDecision, Meta, error) {
	var decision PermissionDecision
	meta, err := p.engine.Evaluate(ctx, DecisionPermission, input, &decision)
	if err != nil {
		p.logPermissionDecision(ctx, input, nil, meta, err)
		return PermissionDecision{}, Meta{}, err
	}
	p.logPermissionDecision(ctx, input, decision, meta, nil)
	return decision, meta, nil
}

// CheckAction evaluates a download, download-transcode, or playback admission gate.
func (p *PDP) CheckAction(ctx context.Context, input ActionInput) (ActionDecision, Meta, error) {
	var decision ActionDecision
	meta, err := p.engine.Evaluate(ctx, DecisionAction, input, &decision)
	if err != nil {
		p.logActionDecision(ctx, input, nil, meta, err)
		return ActionDecision{}, Meta{}, err
	}
	p.logActionDecision(ctx, input, decision, meta, nil)
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

func (p *PDP) logPermissionDecision(ctx context.Context, input PermissionInput, result any, meta Meta, evalErr error) {
	if p == nil || p.decisionLogger == nil {
		return
	}

	entry := Entry{
		DecisionName:     DecisionPermission,
		PolicyGeneration: meta.Revision,
		UserID:           intPtr(input.UserID),
		ProfileID:        input.DeclaredProfileID,
		RequestID:        chimiddleware.GetReqID(ctx),
		EvalTimeNS:       meta.EvalTimeNS,
	}
	if decision, ok := result.(PermissionDecision); ok {
		entry.Allowed = boolPtr(decision.Allowed)
	}
	if evalErr != nil {
		entry.Error = evalErr.Error()
	}
	p.decisionLogger.LogDecision(entry, input, result)
}

func (p *PDP) logActionDecision(ctx context.Context, input ActionInput, result any, meta Meta, evalErr error) {
	if p == nil || p.decisionLogger == nil {
		return
	}

	entry := Entry{
		DecisionName:     DecisionAction,
		PolicyGeneration: meta.Revision,
		UserID:           intPtr(input.UserID),
		RequestID:        chimiddleware.GetReqID(ctx),
		EvalTimeNS:       meta.EvalTimeNS,
	}
	if decision, ok := result.(ActionDecision); ok {
		entry.Allowed = boolPtr(decision.Allowed)
	}
	if evalErr != nil {
		entry.Error = evalErr.Error()
	}
	p.decisionLogger.LogDecision(entry, input, result)
}

func intPtr(v int) *int {
	return &v
}

func boolPtr(v bool) *bool {
	return &v
}
