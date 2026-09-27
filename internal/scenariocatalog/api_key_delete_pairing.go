package scenariocatalog

import (
	"fmt"
	"net/http"
)

// RequiredAPIKeyDeleteScenarios contains only the selected unpaired frozen cases.
var RequiredAPIKeyDeleteScenarios = []string{
	"keys_delete.ok", "keys_delete.gone", "keys_delete.other_user", "keys_delete.bad_id",
	"keys_delete.demo", "keys_delete.shape", "keys_delete.no_token",
}

func APIKeyDeleteAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodDelete, []string{"/api/v1/api-keys/{id}"}, RequiredAPIKeyDeleteScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				if s.V2Expectation.OperationID != "revokePersonalAPIKey" || len(s.Then) != 0 || len(s.V2Expectation.Then) != 0 || len(s.Requires) != 0 {
					return nil, fmt.Errorf("%s: unsupported deletion acceptance sequence", s.ID)
				}
			}
		}
	}
	return selected, nil
}
