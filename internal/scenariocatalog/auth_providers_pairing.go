package scenariocatalog

import (
	"net/http"
)

// RequiredAuthProvidersScenarios contains only the selected unpaired frozen cases.
var RequiredAuthProvidersScenarios = []string{"providers.ok", "providers.local_default", "providers.shape", "providers.sorted", "providers.oauth_hidden"}

const frozenDatabaseRequirement = "database"

func AuthProvidersAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{"/api/v1/auth/providers"}, RequiredAuthProvidersScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "listAuthProviders", "auth providers"); err != nil {
		return nil, err
	}
	return selected, nil
}
