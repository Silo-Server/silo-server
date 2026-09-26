package scenariocatalog

import (
	"fmt"
	"net/http"
)

// RequiredAPIKeyScopesScenarios contains only the selected unpaired frozen cases.
var RequiredAPIKeyScopesScenarios = []string{
	"scopes.ok", "scopes.meaning", "scopes.shape",
	"scopes.sorted", "scopes.no_token", "scopes.error_shape",
}

func APIKeyScopesAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{"/api/v1/api-keys/scopes"}, RequiredAPIKeyScopesScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				if s.V2Expectation.OperationID != "getPersonalAPIKeyScopes" || len(s.Then) != 0 || len(s.V2Expectation.Then) != 0 || len(s.Requires) != 0 {
					return nil, fmt.Errorf("%s: unsupported scope discovery acceptance sequence", s.ID)
				}
			}
		}
	}
	return selected, nil
}
