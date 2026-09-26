package scenariocatalog

import (
	"net/http"
)

// RequiredSignupStatusScenarios contains only the selected unpaired frozen cases.
var RequiredSignupStatusScenarios = []string{"signup_status.ok", "signup_status.enabled", "signup_status.disabled", "signup_status.shape"}

func SignupStatusAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{"/api/v1/auth/signup"}, RequiredSignupStatusScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "getSignupStatus", "signup status"); err != nil {
		return nil, err
	}
	return selected, nil
}
