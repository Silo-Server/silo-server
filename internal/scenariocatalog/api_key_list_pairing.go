package scenariocatalog

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
)

// RequiredAPIKeyListScenarios contains only the selected unpaired frozen cases.
var RequiredAPIKeyListScenarios = []string{
	"keys_list.ok",
	"keys_list.sorted", "keys_list.empty", "keys_list.no_token",
	"keys_list.meaning", "keys_list.shape",
}

func APIKeyListAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	overlaid, err := apiKeyListAbsenceOverlay(catalogs)
	if err != nil {
		return nil, err
	}
	selected, err := requiredAcceptance(overlaid, http.MethodGet, []string{"/api/v1/api-keys/"}, RequiredAPIKeyListScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				if s.V2Expectation.OperationID != "listPersonalAPIKeys" || len(s.Then) != 0 || len(s.V2Expectation.Then) != 0 || len(s.Requires) != 0 {
					return nil, fmt.Errorf("%s: unsupported list acceptance sequence", s.ID)
				}
			}
		}
	}
	return selected, nil
}

// These digests pin the complete decoded original scenarios, including the v1
// full-secret oracle and nil v2 expectation. Only this selector overlays copies;
// Load and every other selector retain the frozen catalog's default behavior.
func apiKeyListAbsenceOverlay(catalogs []*Catalog) ([]*Catalog, error) {
	result := make([]*Catalog, 0, len(catalogs))
	for _, catalog := range catalogs {
		c := *catalog
		c.Rows = slices.Clone(catalog.Rows)
		for ri, row := range c.Rows {
			c.Rows[ri].Scenarios = slices.Clone(row.Scenarios)
			for si, scenario := range row.Scenarios {
				var digest string
				switch scenario.ID {
				case "keys_list.meaning":
					digest = "57d7196b16d8e986e10bf3ee5244091579e16a2dcce7f09123ca5cd4425eb338"
				case "keys_list.shape":
					digest = "1ad0a73af57a696b8b7099651019e7a54187dec04bccc136e8f0e5e48262ab09"
				default:
					continue
				}
				original := scenario
				original.V2Expectation = nil
				encoded, err := json.Marshal(original)
				if err != nil {
					return nil, fmt.Errorf("%s: encode original: %w", scenario.ID, err)
				}
				if row.Listener != listenerAPI || row.Method != http.MethodGet || row.Path != "/api/v1/api-keys/" || row.RegistrationIndex != 0 || fmt.Sprintf("%x", sha256.Sum256(encoded)) != digest {
					return nil, fmt.Errorf("%s: changed frozen API-key list original", scenario.ID)
				}
				pair := apiKeyListAbsenceExpectation()
				if scenario.V2Expectation != nil && !sameSequenceShape(scenario.V2Expectation, pair) {
					return nil, fmt.Errorf("%s: changed API-key list absence expectation", scenario.ID)
				}
				c.Rows[ri].Scenarios[si].V2Expectation = pair
			}
		}
		result = append(result, &c)
	}
	return result, nil
}

func apiKeyListAbsenceExpectation() *V2Expectation {
	return &V2Expectation{
		Kind:        "intentional_difference",
		Summary:     "V2 lists account-owned metadata with string IDs and key_prefix; the reusable key secret must be absent. The original v1 full-secret oracle is unchanged.",
		RecordedIn:  "internal/scenariocatalog/api_key_list_pairing.go",
		OperationID: "listPersonalAPIKeys", Method: http.MethodGet,
		Request: Request{Path: "/api/v2/api-keys"},
		Expect: Expect{Status: http.StatusOK,
			Headers: []HeaderAssertion{{Name: "Content-Type", Op: "equals", Value: "application/json"}},
			Body: []BodyAssertion{
				{Pointer: "/items", Op: "length", Value: json.RawMessage(`2`)},
				{Pointer: "/items", Op: "every", Value: json.RawMessage(`{"pointer":"/key","op":"absent"}`)},
				{Pointer: "/items", Op: "every", Value: json.RawMessage(`{"pointer":"","op":"keys_equal","value":["id","user_id","label","key_prefix","rate_tier","scopes","created_at"]}`)},
				{Pointer: "/items", Op: "every", Value: json.RawMessage(`{"pointer":"/user_id","op":"equals","value":"${admin_user_id}"}`)},
				{Pointer: "/items", Op: "every", Value: json.RawMessage(`{"pointer":"/id","op":"type","value":"string"}`)},
				{Pointer: "/items", Op: "every", Value: json.RawMessage(`{"pointer":"/key_prefix","op":"non_empty"}`)},
				{Pointer: "/items/0/scopes", Op: "equals", Value: json.RawMessage(`["admin:users"]`)},
				{Pointer: "/items/1/scopes", Op: "equals", Value: json.RawMessage(`[]`)},
				{Pointer: "/items/0/rate_tier", Op: "equals", Value: json.RawMessage(`"standard"`)},
			},
		},
	}
}
