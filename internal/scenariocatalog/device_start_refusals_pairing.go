package scenariocatalog

import (
	"net/http"
)

// RequiredDeviceStartRefusalsScenarios contains only the selected unpaired frozen cases.
var RequiredDeviceStartRefusalsScenarios = []string{"device_start.bad_purpose", "device_start.malformed"}

func DeviceStartRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/auth/device/start"}, RequiredDeviceStartRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "startDeviceLogin", "device start refusals"); err != nil {
		return nil, err
	}
	return selected, nil
}
