package scenariocatalog

import (
	"fmt"
	"net/http"
)

// RequiredAPIKeyCreateRefusalScenarios contains only the selected unpaired frozen cases.
var RequiredAPIKeyCreateRefusalScenarios = []string{
	"keys_create.bad_scope", "keys_create.missing_label", "keys_create.malformed",
	"keys_create.demo", "keys_create.no_token",
}

func APIKeyCreateRefusalAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/api-keys/"}, RequiredAPIKeyCreateRefusalScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				if s.V2Expectation.OperationID != "createPersonalAPIKey" || len(s.Then) != 0 || len(s.V2Expectation.Then) != 0 || len(s.Requires) != 0 {
					return nil, fmt.Errorf("%s: unsupported creation refusal acceptance sequence", s.ID)
				}
			}
		}
	}
	return selected, nil
}
