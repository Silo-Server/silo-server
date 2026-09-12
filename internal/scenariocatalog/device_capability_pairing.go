package scenariocatalog

import (
	"net/http"
)

// RequiredDeviceCapabilityScenarios contains only the selected unpaired frozen cases.
var RequiredDeviceCapabilityScenarios = []string{"capability.meaning", "capability.shape"}

func DeviceCapabilityAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{"/api/v1/auth/device/capability"}, RequiredDeviceCapabilityScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "getDeviceLoginCapability", "device capability"); err != nil {
		return nil, err
	}
	return selected, nil
}
