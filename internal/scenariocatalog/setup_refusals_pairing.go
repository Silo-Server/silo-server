package scenariocatalog

import (
	"net/http"
)

// RequiredSetupRefusalsScenarios contains only the selected unpaired frozen cases.
var RequiredSetupRefusalsScenarios = []string{"setup.already_complete", "setup.missing_fields", "setup.malformed_json"}

func SetupRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/auth/setup"}, RequiredSetupRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "setupServer", "setup refusals"); err != nil {
		return nil, err
	}
	return selected, nil
}
