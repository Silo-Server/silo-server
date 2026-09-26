package scenariocatalog

import (
	"net/http"
)

// RequiredLoginSessionsScenarios contains only the selected unpaired frozen cases.
var RequiredLoginSessionsScenarios = []string{"sessions.ok", "sessions.meaning", "sessions.shape", "sessions.sorted", "sessions.no_token", "sessions.error_shape"}

func LoginSessionsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{"/api/v1/auth/sessions"}, RequiredLoginSessionsScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "listSessions", "login sessions"); err != nil {
		return nil, err
	}
	return selected, nil
}
