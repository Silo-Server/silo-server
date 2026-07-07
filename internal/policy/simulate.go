package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrUnsupportedDomain is returned when a policy domain has no simulation
// decision registered.
var ErrUnsupportedDomain = errors.New("unsupported policy domain")

var domainDecisions = map[string]DecisionName{
	DomainScope:      DecisionScope,
	DomainPermission: DecisionPermission,
	DomainAction:     DecisionAction,
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
	// Lenient on stored sources: a broken source in another domain must not
	// block simulating a fix. The candidate source itself was CompileChecked.
	modules, _, err := engine.modulesWithCustom(ctx, sources)
	if err != nil {
		return SimulateResult{}, err
	}
	if err := engine.swap(ctx, modules, decisionQueries(), generation); err != nil {
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

// GuardEvalCost is the activation-time cost guard: it compiles the vendor
// bundle plus source into a throwaway engine and evaluates the domain's
// decision once against a canned representative input under budget. Every
// authenticated request evaluates policy under the same budget, so activating
// a source that cannot complete within it would convert to request-path
// failures for every viewer; rejecting at activation keeps that blast radius
// at the admin who authored the policy.
func GuardEvalCost(ctx context.Context, domain, source string, budget time.Duration) error {
	domain = strings.TrimSpace(domain)
	decisionName, ok := domainDecisions[domain]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnsupportedDomain, domain)
	}
	if strings.TrimSpace(source) == "" {
		return nil
	}
	if budget <= 0 {
		budget = defaultEvalTimeout
	}

	input, err := guardBenchmarkInput(domain)
	if err != nil {
		return err
	}
	modules, err := vendorModules(false)
	if err != nil {
		return err
	}
	modules = append(modules, ModuleSource{Path: customModulePath(domain), Source: source})
	engine := newEngine(WithEvalTimeout(budget))
	if err := engine.swap(ctx, modules, decisionQueries(), 0); err != nil {
		return err
	}

	var decision json.RawMessage
	if _, err := engine.Evaluate(ctx, decisionName, input, &decision); err != nil {
		if errors.Is(err, ErrPolicyEvalTimeout) {
			return fmt.Errorf("%w: %s decision did not complete within %s", ErrPolicySlowEval, domain, budget)
		}
		return err
	}
	return nil
}

// guardBenchmarkInput returns a fully-populated representative input for the
// domain's decision. It only needs to reach the custom override with realistic
// fact shapes — the decision outcome is irrelevant to the cost measurement.
func guardBenchmarkInput(domain string) (any, error) {
	const benchTime = "2026-01-01T00:00:00Z"
	var input any
	switch domain {
	case DomainScope:
		input = ScopeInput{
			SchemaVersion: 1, UserID: 1, SessionID: "eval-cost-guard",
			AccountLibraryIDs: []int{1, 2, 3}, AccountRestricted: true,
			AccountMaxQuality: "2160p", AccessPolicyRevision: 1,
			DisabledLibraryIDs: []int{4}, ProfilePresent: true,
			ProfileMaxRating: "PG-13", ProfileMaxQuality: "1080p",
			ProfileLibraryLimited: true, ProfileLibraryIDs: []int{1, 2},
			ProfileHasPIN: true, ProfileVerified: true, RequestTime: benchTime,
		}
	case DomainPermission:
		input = PermissionInput{
			SchemaVersion: 1, UserID: 1, Role: "user", UserEnabled: true,
			AssignedPermissions: []string{PermissionMarkerEdit},
			Permission:          PermissionMarkerEdit,
			TargetLibraryIDs:    []int{1}, UserLibraryIDs: []int{1, 2},
			UserLibrariesRestricted: true, RequestTime: benchTime,
		}
	case DomainAction:
		input = ActionInput{
			SchemaVersion: 1, Action: ActionDownload, UserID: 1,
			DownloadAllowed: true, DownloadTranscodeAllowed: true,
			MaxStreams: 5, MaxTranscodes: 2, DownloadsEnabled: true,
			TranscodeEnabled: true, ArtifactsAvailable: true,
			CurrentActiveStreams: 1, RequestedAction: RequestedActionDirectPlay,
			MaxPlaybackQuality: "1080p", RequestTime: benchTime,
		}
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedDomain, domain)
	}
	// Round-trip so the engine sees the same JSON document shape live inputs
	// arrive as.
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encoding eval-cost guard input: %w", err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("decoding eval-cost guard input: %w", err)
	}
	return decoded, nil
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
