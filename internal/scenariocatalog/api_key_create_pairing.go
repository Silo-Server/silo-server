package scenariocatalog

import (
	"fmt"
	"net/http"
)

// RequiredAPIKeyCreateScenarios contains only the selected unpaired frozen cases.
var RequiredAPIKeyCreateScenarios = []string{
	"keys_create.ok", "keys_create.meaning", "keys_create.scoped", "keys_create.shape",
}

const frozenPersonalAPIKeysPath = "/api/v1/api-keys/"

func APIKeyCreateAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{frozenPersonalAPIKeysPath}, RequiredAPIKeyCreateScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				if s.V2Expectation.OperationID != "createPersonalAPIKey" || len(s.Then) != 0 || len(s.V2Expectation.Then) != 0 || len(s.Requires) != 0 {
					return nil, fmt.Errorf("%s: unsupported creation acceptance sequence", s.ID)
				}
			}
		}
	}
	return selected, nil
}
