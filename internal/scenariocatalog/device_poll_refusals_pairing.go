package scenariocatalog

import (
	"net/http"
)

// RequiredDevicePollRefusalsScenarios contains only the selected unpaired frozen cases.
var RequiredDevicePollRefusalsScenarios = []string{"device_poll.unknown", "device_poll.missing"}

func DevicePollRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/auth/device/poll"}, RequiredDevicePollRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "pollDeviceLogin", "device poll refusals"); err != nil {
		return nil, err
	}
	return selected, nil
}
