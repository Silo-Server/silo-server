package scenariocatalog

import (
	"net/http"
)

const meImpersonationLegacyRoute = "/api/v1/auth/me"

// RequiredMeImpersonationScenarios contains only the selected unpaired frozen cases.
var RequiredMeImpersonationScenarios = []string{"me.impersonation"}

func MeImpersonationAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{meImpersonationLegacyRoute}, RequiredMeImpersonationScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "getCurrentUser", "impersonated account read"); err != nil {
		return nil, err
	}
	return selected, nil
}
