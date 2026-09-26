package scenariocatalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed device_start_lookup_originals.json
var deviceStartLookupOriginals []byte

// deviceLookupRateLimitedScenario is the one 21-request burst the shared seam
// bounds by its frozen original instead of the default 16-exchange budget.
const deviceLookupRateLimitedScenario = "device_lookup.rate_limited.r1"

var RequiredDeviceStartLookupScenarios = []string{"device_start.ok", "device_start.meaning", "device_start.empty_body", "device_start.remote", "device_start.ok.r1", "device_start.meaning.r1", "device_start.empty_body.r1", "device_start.remote.r1", "device_start.bad_purpose.r1", "device_start.malformed.r1", "device_start.rate_limited.r1", "device_lookup.by_token.r1", "device_lookup.by_code.r1", "device_lookup.meaning.r1", "device_lookup.expired.r1", "device_lookup.shape.r1", "device_lookup.not_found.r1", "device_lookup.no_params.r1", deviceLookupRateLimitedScenario}

type deviceStartLookupOriginal struct {
	Registration int
	Method       string
	Path         string
	Scenario     Scenario
	Pair         V2Expectation
}

// DeviceStartLookupAcceptance preserves every original field, including oracle and principal.
func DeviceStartLookupAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	var originals map[string]deviceStartLookupOriginal
	if err := json.Unmarshal(deviceStartLookupOriginals, &originals); err != nil {
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
				// The shared seam keeps the 16-exchange budget for every scenario
				// except the frozen 21-request lookup burst, which it bounds by the
				// exact original scenario and row before the v2 translation.
				if err := ValidateScenarioPairing(row, s); err != nil {
					return nil, fmt.Errorf("%s: %w", s.ID, err)
				}
				frozen := s
				frozen.V2Expectation = nil
				a, _ := json.Marshal(frozen)
				b, _ := json.Marshal(original.Scenario)
				pairJSON, _ := json.Marshal(pair)
				expectedPairJSON, _ := json.Marshal(original.Pair)
				if !bytes.Equal(a, b) || row.Listener != listenerAPI || row.Method != original.Method || row.Path != original.Path || row.RegistrationIndex != original.Registration || !bytes.Equal(pairJSON, expectedPairJSON) {
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
	for _, id := range RequiredDeviceStartLookupScenarios {
		if !seen[id] {
			return nil, fmt.Errorf("missing %s", id)
		}
	}
	return result, nil
}
