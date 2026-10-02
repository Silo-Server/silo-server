package scenariocatalog

import (
	"net/http"
)

// RequiredLogoutSuccessScenarios contains only the selected unpaired frozen cases.
var RequiredLogoutSuccessScenarios = []string{"logout.ok", "logout.shape"}

func LogoutSuccessAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/auth/logout"}, RequiredLogoutSuccessScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "logout", "logout success"); err != nil {
		return nil, err
	}
	return selected, nil
}
