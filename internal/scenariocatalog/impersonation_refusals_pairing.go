package scenariocatalog

import (
	"net/http"
)

// RequiredImpersonationRefusalsScenarios contains only the selected unpaired frozen cases.
var RequiredImpersonationRefusalsScenarios = []string{"imp_end.not_impersonating", "imp_end.no_token"}

func ImpersonationRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/auth/impersonation/end"}, RequiredImpersonationRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "endImpersonation", "impersonation refusals"); err != nil {
		return nil, err
	}
	return selected, nil
}
