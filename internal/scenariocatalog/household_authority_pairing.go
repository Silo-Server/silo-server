package scenariocatalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed household_authority_originals.json
var householdAuthorityOriginals []byte

var RequiredHouseholdAuthorityScenarios = []string{"profiles_update.quality_normalized", "household.ok", "household.empty", "household.shape", "household.secondary_forbidden", "household.admin", "household.other_account_profile", "household.no_token", "household.error_shape"}

type householdAuthorityOriginal struct {
	Method   string
	Path     string
	Scenario Scenario
	Pair     V2Expectation
}

// HouseholdAuthorityAcceptance preserves every original field, including oracle and principal.
func HouseholdAuthorityAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	var originals map[string]householdAuthorityOriginal
	if err := json.Unmarshal(householdAuthorityOriginals, &originals); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var result []*Catalog
	for _, catalog := range catalogs {
		c := *catalog
		c.Rows = nil
		for _, row := range catalog.Rows {
			r := row
			r.Scenarios = nil
			for _, s := range row.Scenarios {
				original, ok := originals[s.ID]
				if !ok {
					continue
				}
				if seen[s.ID] {
					return nil, fmt.Errorf("duplicate %s", s.ID)
				}
				seen[s.ID] = true
				pair := s.V2Expectation
				if err := ValidatePairing(pair); err != nil {
					return nil, fmt.Errorf("%s: %w", s.ID, err)
				}
				frozen := s
				frozen.V2Expectation = nil
				a, _ := json.Marshal(frozen)
				b, _ := json.Marshal(original.Scenario)
				pairJSON, _ := json.Marshal(pair)
				expectedPairJSON, _ := json.Marshal(original.Pair)
				if !bytes.Equal(a, b) || row.Listener != listenerAPI || row.Method != original.Method || row.Path != original.Path || row.RegistrationIndex != 0 || !bytes.Equal(pairJSON, expectedPairJSON) {
					return nil, fmt.Errorf("%s: changed frozen authority exchange", s.ID)
				}
				r.Scenarios = append(r.Scenarios, s)
			}
			if len(r.Scenarios) > 0 {
				c.Rows = append(c.Rows, r)
			}
		}
		if len(c.Rows) > 0 {
			result = append(result, &c)
		}
	}
	for _, id := range RequiredHouseholdAuthorityScenarios {
		if !seen[id] {
			return nil, fmt.Errorf("missing %s", id)
		}
	}
	return result, nil
}
