package scenariocatalog

import (
	"net/http"
)

// RequiredDeviceLookupScenarios contains only the selected unpaired frozen cases.
var RequiredDeviceLookupScenarios = []string{"device_lookup.by_token", "device_lookup.by_code", "device_lookup.meaning", "device_lookup.shape"}

func DeviceLookupAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{"/api/v1/auth/device"}, RequiredDeviceLookupScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "getDeviceLogin", "device lookup"); err != nil {
		return nil, err
	}
	return selected, nil
}
