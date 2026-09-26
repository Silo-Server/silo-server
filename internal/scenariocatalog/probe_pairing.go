package scenariocatalog

import (
	"fmt"
	"net/http"
	"slices"
)

// RequiredRetainedProbeScenarios fixes the seven frozen liveness and
// readiness originals. Their rows are retained unversioned probes (owner
// decision 2026-09-07): there is no v2 operation to pair with, so acceptance
// runs the exact v1 oracle against the retained route only. Four of them
// require the unreachable-database state the outage runner reproduces.
var RequiredRetainedProbeScenarios = []string{
	"health.ok", "health.identity", "health.shape",
	"ready.ok", "ready.db_down", "ready.shape_on_failure", "ready.meaning",
}

// retainedProbeOutageScenarios are the members recorded under database_unavailable.
var retainedProbeOutageScenarios = []string{"health.ok", "ready.db_down", "ready.shape_on_failure", "ready.meaning"}

// RetainedProbeAcceptance selects the seven frozen probe cases from the
// health and readiness rows. Unlike the paired selectors it requires each case
// to be UNPAIRED: the retained probes gain no v2 operation, and a recorded
// v2_expectation would contradict the ledger. It refuses a case whose
// requirement set moved off its recorded state.
func RetainedProbeAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	want := make(map[string]bool, len(RequiredRetainedProbeScenarios))
	for _, id := range RequiredRetainedProbeScenarios {
		want[id] = false
	}
	var selected []*Catalog
	for _, c := range catalogs {
		copy := *c
		copy.Rows = nil
		for _, row := range c.Rows {
			if row.Listener != listenerAPI || row.Method != http.MethodGet || (row.Path != "/api/v1/health" && row.Path != "/api/v1/ready") || row.RegistrationIndex != 0 {
				continue
			}
			picked := row
			picked.Scenarios = nil
			for _, s := range row.Scenarios {
				if _, ok := want[s.ID]; !ok {
					continue
				}
				if want[s.ID] {
					return nil, fmt.Errorf("duplicate required scenario %s", s.ID)
				}
				if s.V2Expectation != nil {
					return nil, fmt.Errorf("%s: retained probe must stay unpaired; no v2 operation exists", s.ID)
				}
				if s.Principal.Class != lifecyclePublicPrincipal || len(s.Settings) != 0 || s.FreshState || len(s.Then) != 0 {
					return nil, fmt.Errorf("%s: probe case must be a plain public read", s.ID)
				}
				outage := slices.Contains(retainedProbeOutageScenarios, s.ID)
				switch {
				case outage && (len(s.Requires) != 1 || s.Requires[0] != frozenOutageRequirement):
					return nil, fmt.Errorf("%s: probe case must require exactly %s", s.ID, frozenOutageRequirement)
				case !outage && s.HasRequirement(frozenOutageRequirement):
					return nil, fmt.Errorf("%s: probe case must not require %s", s.ID, frozenOutageRequirement)
				case s.ID == "ready.ok" && (len(s.Requires) != 1 || s.Requires[0] != frozenDatabaseRequirement):
					return nil, fmt.Errorf("%s: readiness success must require exactly %s", s.ID, frozenDatabaseRequirement)
				}
				want[s.ID] = true
				picked.Scenarios = append(picked.Scenarios, s)
			}
			if len(picked.Scenarios) > 0 {
				copy.Rows = append(copy.Rows, picked)
			}
		}
		if len(copy.Rows) > 0 {
			selected = append(selected, &copy)
		}
	}
	for _, id := range RequiredRetainedProbeScenarios {
		if !want[id] {
			return nil, fmt.Errorf("required scenario %s is missing", id)
		}
	}
	return selected, nil
}

// RetainedProbeNeedsOutage reports whether the case runs on the offline
// (closed-port database) router.
func RetainedProbeNeedsOutage(id string) bool {
	return slices.Contains(retainedProbeOutageScenarios, id)
}
