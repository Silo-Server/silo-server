package scenariocatalog

import (
	"net/http"
)

// RequiredSignupRefusalsScenarios contains only the selected unpaired frozen cases.
var RequiredSignupRefusalsScenarios = []string{"signup.disabled_setting", "signup.missing_fields"}

func SignupRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/auth/signup"}, RequiredSignupRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "signup", "signup refusals"); err != nil {
		return nil, err
	}
	return selected, nil
}
