package policy

import "context"

// PDP exposes typed policy decisions over the generic Rego engine.
type PDP struct {
	engine *Engine
}

// NewPDP creates a policy decision point from a compiled engine.
func NewPDP(engine *Engine) *PDP {
	return &PDP{engine: engine}
}

// ResolveViewerScope resolves the effective viewer access scope.
func (p *PDP) ResolveViewerScope(ctx context.Context, input ScopeInput) (ScopeDecision, Meta, error) {
	var decision ScopeDecision
	meta, err := p.engine.Evaluate(ctx, DecisionScope, input, &decision)
	if err != nil {
		return ScopeDecision{}, Meta{}, err
	}
	return decision, meta, nil
}
