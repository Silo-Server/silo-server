package scenariocatalog

import (
	"fmt"
	"net/http"
)

// RequiredSectionReplaceScenarios fixes the frozen replacement acceptance inventory.
var RequiredSectionReplaceScenarios = []string{
	"overrides_put.ok",
	"overrides_put.roundtrip",
	"overrides_put.user_added_gate",
	"overrides_put.user_added_allowed",
	"overrides_put.admin_bypass",
	"overrides_put.unknown_recipe",
	"overrides_put.invalid_config",
	"overrides_put.malformed",
	"overrides_put.empty_section_id_is_user_added",
	"overrides_put.shape",
	"overrides_put.no_profile",
	"overrides_put.other_account_profile",
	"overrides_put.no_token",
}

func SectionReplaceAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPut, []string{"/api/v1/profile/sections/"}, RequiredSectionReplaceScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, row := range c.Rows {
			for _, scenario := range row.Scenarios {
				then := scenario.V2Expectation.Then
				if len(then) != 3 {
					return nil, fmt.Errorf("%s: three profile read-after steps required", scenario.ID)
				}
				for i, principal := range []string{"profile", "primary_profile", "acting_admin"} {
					step := then[i]
					if step.OperationID != "listProfileSectionOverrides" || step.Method != http.MethodGet || step.Request.Path != "/api/v2/profile/sections" || len(step.Request.Query) != 0 || step.Principal == nil || step.Principal.Class != principal || len(step.Expect.Body) == 0 {
						return nil, fmt.Errorf("%s: invalid profile read-after step %d", scenario.ID, i)
					}
				}
			}
		}
	}
	return selected, nil
}
