package scenariocatalog

import (
	"net/http"
)

// RequiredSessionDeleteRefusalsScenarios contains only the selected unpaired frozen cases.
var RequiredSessionDeleteRefusalsScenarios = []string{"session_delete.other_user", "session_delete.unknown", "session_delete.no_token"}

func SessionDeleteRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodDelete, []string{"/api/v1/auth/sessions/{id}"}, RequiredSessionDeleteRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "deleteSession", "session delete refusals"); err != nil {
		return nil, err
	}
	return selected, nil
}
