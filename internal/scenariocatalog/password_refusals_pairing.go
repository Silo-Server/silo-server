package scenariocatalog

import (
	"net/http"
)

// RequiredPasswordRefusalsScenarios contains only the selected unpaired frozen cases.
var RequiredPasswordRefusalsScenarios = []string{"password.wrong_current", "password.weak", "password.too_long", "password.missing_fields", "password.malformed_json"}

func PasswordRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/auth/account/password"}, RequiredPasswordRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "changePassword", "password refusals"); err != nil {
		return nil, err
	}
	return selected, nil
}
