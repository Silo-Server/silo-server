package scenariocatalog

import (
	"fmt"
	"net/http"
)

// RequiredSectionResetScenarios fixes the frozen reset acceptance inventory.
var RequiredSectionResetScenarios = []string{
	"overrides_reset.ok",
	"overrides_reset.idempotent",
	"overrides_reset.scope",
	"overrides_reset.shape",
	"overrides_reset.no_profile",
	"overrides_reset.other_account_profile",
	"overrides_reset.no_token",
	"overrides_reset.error_shape",
}

func SectionResetAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodDelete, []string{"/api/v1/profile/sections/reset"}, RequiredSectionResetScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, row := range c.Rows {
			for _, scenario := range row.Scenarios {
				then := scenario.V2Expectation.Then
				if len(then) != 4 {
					return nil, fmt.Errorf("%s: four required scope/profile read-after steps missing", scenario.ID)
				}
				for i, step := range then {
					principal := "profile"
					if i >= 2 {
						principal = "primary_profile"
					}
					library := i%2 == 1
					if step.OperationID != "listProfileSectionOverrides" || step.Method != http.MethodGet || step.Request.Path != "/api/v2/profile/sections" || step.Principal == nil || step.Principal.Class != principal || len(step.Expect.Body) == 0 {
						return nil, fmt.Errorf("%s: invalid profile read-after step %d", scenario.ID, i)
					}
					if library && (step.Request.Query["scope"] != "library" || step.Request.Query["library_id"] != "7") || !library && len(step.Request.Query) != 0 {
						return nil, fmt.Errorf("%s: invalid scope read-after step %d", scenario.ID, i)
					}
				}
			}
		}
	}
	return selected, nil
}
