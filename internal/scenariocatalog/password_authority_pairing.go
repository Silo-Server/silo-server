package scenariocatalog

import (
	"net/http"
)

// RequiredPasswordAuthorityScenarios contains only the selected unpaired frozen cases.
var RequiredPasswordAuthorityScenarios = []string{"password.no_profile", "password.secondary_profile", "password.no_token", "password.bad_token"}

func PasswordAuthorityAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/auth/account/password"}, RequiredPasswordAuthorityScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "changePassword", "password authority"); err != nil {
		return nil, err
	}
	return selected, nil
}
