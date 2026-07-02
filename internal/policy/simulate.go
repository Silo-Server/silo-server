package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrUnsupportedDomain is returned when a policy domain has no simulation
// decision registered.
var ErrUnsupportedDomain = errors.New("unsupported policy domain")

var domainDecisions = map[string]DecisionName{
	DomainScope: DecisionScope,
}

// DecisionTypes returns policy domains with simulation support.
func DecisionTypes() []string {
	types := make([]string, 0, len(domainDecisions))
	for domain := range domainDecisions {
		types = append(types, domain)
	}
	sort.Strings(types)
	return types
}

// SimulateRequest describes a stateless policy simulation request.
type SimulateRequest struct {
	Domain string          `json:"domain"`
	Source string          `json:"source,omitempty"`
	Input  json.RawMessage `json:"input"`
}

// SimulateResult is the raw policy decision produced by a throwaway engine.
type SimulateResult struct {
	Decision   json.RawMessage `json:"decision"`
	EvalTimeNS int64           `json:"eval_time_ns"`
	Generation int64           `json:"generation"`
}

// Simulate evaluates a policy decision against a throwaway engine. It never
// mutates the live System engine and never writes a decision-log entry.
func Simulate(ctx context.Context, store *PolicyStore, req SimulateRequest) (SimulateResult, error) {
	domain := strings.TrimSpace(req.Domain)
	decisionName, ok := domainDecisions[domain]
	if !ok {
		return SimulateResult{}, fmt.Errorf("%w: %s", ErrUnsupportedDomain, domain)
	}

	input, err := decodeSimulateInput(req.Input)
	if err != nil {
		return SimulateResult{}, err
	}

	sources, generation, err := simulationSources(ctx, store)
	if err != nil {
		return SimulateResult{}, err
	}
	if strings.TrimSpace(req.Source) != "" {
		if err := CompileCheck(ctx, domain, req.Source); err != nil {
			return SimulateResult{}, err
		}
		sources[domain] = ActiveSource{Source: req.Source}
	}

	engine := newEngine(WithRevision(generation))
	modules, err := engine.modulesWithCustom(ctx, sources)
	if err != nil {
		return SimulateResult{}, err
	}
	if err := engine.swap(ctx, modules, scopeQueries(), generation); err != nil {
		return SimulateResult{}, err
	}

	var decision json.RawMessage
	meta, err := engine.Evaluate(ctx, decisionName, input, &decision)
	if err != nil {
		return SimulateResult{}, err
	}
	return SimulateResult{
		Decision:   decision,
		EvalTimeNS: meta.EvalTimeNS,
		Generation: meta.Revision,
	}, nil
}

func decodeSimulateInput(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, compileErrorMessage("policy simulation input is required")
	}
	var input any
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, compileErrorMessage("policy simulation input must be valid JSON")
	}
	return input, nil
}

func simulationSources(ctx context.Context, store *PolicyStore) (map[string]ActiveSource, int64, error) {
	if store == nil {
		return map[string]ActiveSource{}, 0, nil
	}
	for {
		before, err := store.Generation(ctx)
		if err != nil {
			return nil, 0, err
		}
		sources, err := store.ActiveSources(ctx)
		if err != nil {
			return nil, 0, err
		}
		after, err := store.Generation(ctx)
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
