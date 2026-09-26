package scenariocatalog

import (
	"net/http"
)

// RequiredDeviceDenyRefusalsScenarios contains only the selected unpaired frozen cases.
var RequiredDeviceDenyRefusalsScenarios = []string{"deny.expired", "deny.not_found", "deny.no_token"}

func DeviceDenyRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/auth/device/deny"}, RequiredDeviceDenyRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "denyDeviceLogin", "device deny refusals"); err != nil {
		return nil, err
	}
	return selected, nil
}
