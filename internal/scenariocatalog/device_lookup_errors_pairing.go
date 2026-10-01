package scenariocatalog

import (
	"net/http"
)

// RequiredDeviceLookupErrorsScenarios contains only the selected unpaired frozen cases.
var RequiredDeviceLookupErrorsScenarios = []string{"device_lookup.expired", "device_lookup.not_found", "device_lookup.no_params"}

func DeviceLookupErrorsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{"/api/v1/auth/device"}, RequiredDeviceLookupErrorsScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "getDeviceLogin", "device lookup errors"); err != nil {
		return nil, err
	}
	return selected, nil
}
