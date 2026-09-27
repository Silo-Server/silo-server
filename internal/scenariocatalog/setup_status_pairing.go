package scenariocatalog

import (
	"net/http"
)

// RequiredSetupStatusScenarios contains only the selected unpaired frozen cases.
var RequiredSetupStatusScenarios = []string{"setup_status.ok", "setup_status.meaning", "setup_status.shape"}

func SetupStatusAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{"/api/v1/auth/setup"}, RequiredSetupStatusScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "getSetupStatus", "setup status"); err != nil {
		return nil, err
	}
	return selected, nil
}
