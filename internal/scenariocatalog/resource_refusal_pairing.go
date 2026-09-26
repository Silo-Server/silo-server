package scenariocatalog

import (
	"fmt"
	"net/http"
)

// RequiredResourceRefusalScenarios excludes successful resource reads and their gates.
var RequiredResourceRefusalScenarios = []string{
	"resources.admin_secondary_profile", "resources.non_admin", "resources.no_token",
}

func ResourceRefusalAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{"/api/v1/admin/system/resources"}, RequiredResourceRefusalScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				if s.V2Expectation.OperationID != "getAdminSystemResources" || len(s.Then) != 0 || len(s.V2Expectation.Then) != 0 || len(s.Requires) != 0 || (s.Expect.Status != 401 && s.Expect.Status != 403) || s.V2Expectation.Expect.Status != s.Expect.Status {
					return nil, fmt.Errorf("%s: unsupported resource refusal acceptance sequence", s.ID)
				}
			}
		}
	}
	return selected, nil
}
