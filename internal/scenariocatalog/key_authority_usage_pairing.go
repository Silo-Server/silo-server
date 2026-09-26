package scenariocatalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
)

//go:embed key_authority_usage_originals.json
var keyAuthorityOriginals []byte

var RequiredKeyAuthorityUsageScenarios = []string{
	"keys_list.api_key_forbidden", "keys_list.error_shape", "keys_create.api_key_forbidden",
	"scopes.api_key_allowed", "keys_delete.api_key_forbidden", "me.api_key", "me.scoped_api_key",
	"logout.api_key", "adm_inv_list.scoped_key",
}

type keyAuthorityOriginal struct {
	Method   string
	Path     string
	Scenario Scenario
}

// KeyAuthorityUsageAcceptance preserves every original field, including oracle and principal.
func KeyAuthorityUsageAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	var originals map[string]keyAuthorityOriginal
	if err := json.Unmarshal(keyAuthorityOriginals, &originals); err != nil {
		return nil, err
	}
	const keyPath = "/api/v2/api-keys"
	routes := map[string][3]string{
		"keys_list.api_key_forbidden":   {http.MethodGet, keyPath, "listPersonalAPIKeys"},
		"keys_list.error_shape":         {http.MethodGet, keyPath, "listPersonalAPIKeys"},
		"keys_create.api_key_forbidden": {http.MethodPost, keyPath, "createPersonalAPIKey"},
		"scopes.api_key_allowed":        {http.MethodGet, "/api/v2/api-keys/scopes", "getPersonalAPIKeyScopes"},
		"keys_delete.api_key_forbidden": {http.MethodDelete, "/api/v2/api-keys/1", "revokePersonalAPIKey"},
		"me.api_key":                    {http.MethodGet, "/api/v2/account/me", "getCurrentUser"},
		"me.scoped_api_key":             {http.MethodGet, "/api/v2/account/me", "getCurrentUser"},
		"logout.api_key":                {http.MethodPost, "/api/v2/auth/logout", "logout"},
		"adm_inv_list.scoped_key":       {http.MethodGet, "/api/v2/admin/invitations", "listAdminInvitations"},
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
				route := routes[s.ID]
				request := s.Request
				request.Path = route[1]
				requestJSON, _ := json.Marshal(request)
				pairRequestJSON, _ := json.Marshal(pair.Request)
				if !bytes.Equal(a, b) || row.Listener != listenerAPI || row.Method != original.Method || row.Path != original.Path || row.RegistrationIndex != 0 || pair.Method != route[0] || pair.OperationID != route[2] || pair.Principal != nil || len(pair.Then) != 0 || !bytes.Equal(pairRequestJSON, requestJSON) {
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
	for _, id := range RequiredKeyAuthorityUsageScenarios {
		if !seen[id] {
			return nil, fmt.Errorf("missing %s", id)
		}
	}
	return result, nil
}
