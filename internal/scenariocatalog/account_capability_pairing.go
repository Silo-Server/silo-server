package scenariocatalog

import (
	"net/http"
)

// RequiredAccountCapabilityScenarios contains only the selected unpaired frozen cases.
var RequiredAccountCapabilityScenarios = []string{
	"account_capability.ok", "account_capability.meaning", "account_capability.no_profile", "account_capability.secondary_profile", "account_capability.no_token", "account_capability.bad_token", "account_capability.error_shape",
}

func AccountCapabilityAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{"/api/v1/auth/account/capability"}, RequiredAccountCapabilityScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "getAccountPasswordCapability", "account capability"); err != nil {
		return nil, err
	}
	return selected, nil
}
