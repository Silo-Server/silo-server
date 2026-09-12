package scenariocatalog

import (
	"net/http"
)

// RequiredAccountReadsScenarios contains only the selected unpaired frozen cases.
var RequiredAccountReadsScenarios = []string{"me.ok", "me.meaning"}

func AccountReadsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{meImpersonationLegacyRoute}, RequiredAccountReadsScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "getCurrentUser", "ordinary account reads"); err != nil {
		return nil, err
	}
	return selected, nil
}
