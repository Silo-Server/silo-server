package scenariocatalog

import (
	"net/http"
)

const loginCredentialsLegacyRoute = "/api/v1/auth/login"

// RequiredLoginCredentialsScenarios contains only the selected unpaired frozen cases.
var RequiredLoginCredentialsScenarios = []string{"login.wrong_password", "login.unknown_user", "login.disabled"}

func LoginCredentialsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{loginCredentialsLegacyRoute}, RequiredLoginCredentialsScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "login", "login credentials"); err != nil {
		return nil, err
	}
	return selected, nil
}
