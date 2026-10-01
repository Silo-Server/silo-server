package scenariocatalog

import (
	"net/http"
)

const signupCodesLegacyRoute = "/api/v1/auth/signup"

// RequiredSignupCodesScenarios contains only the selected unpaired frozen cases.
var RequiredSignupCodesScenarios = []string{"signup.bad_code", "signup.exhausted_code", "signup.disabled_code"}

func SignupCodesAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{signupCodesLegacyRoute}, RequiredSignupCodesScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "signup", "signup code refusals"); err != nil {
		return nil, err
	}
	return selected, nil
}
