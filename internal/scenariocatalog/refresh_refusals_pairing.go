package scenariocatalog

import (
	"net/http"
)

// RequiredRefreshRefusalsScenarios contains only the selected unpaired frozen cases.
var RequiredRefreshRefusalsScenarios = []string{"refresh.access_token_rejected", "refresh.revoked", "refresh.garbage", "refresh.missing"}

func RefreshRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/auth/refresh"}, RequiredRefreshRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "refreshSession", "refresh refusals"); err != nil {
		return nil, err
	}
	return selected, nil
}
