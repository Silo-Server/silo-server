package scenariocatalog

import (
	"net/http"
)

// RequiredLoginInputScenarios contains only the selected unpaired frozen cases.
var RequiredLoginInputScenarios = []string{"login.unknown_provider", "login.missing_fields", "login.malformed_json"}

func LoginInputAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/auth/login"}, RequiredLoginInputScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "login", "login input"); err != nil {
		return nil, err
	}
	return selected, nil
}
