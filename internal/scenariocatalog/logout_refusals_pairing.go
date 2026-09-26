package scenariocatalog

import (
	"net/http"
)

// RequiredLogoutRefusalsScenarios contains only the selected unpaired frozen cases.
var RequiredLogoutRefusalsScenarios = []string{"logout.no_token", "logout.error_shape"}

func LogoutRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/auth/logout"}, RequiredLogoutRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "logout", "logout refusals"); err != nil {
		return nil, err
	}
	return selected, nil
}
