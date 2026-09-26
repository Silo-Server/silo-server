package scenariocatalog

import (
	"net/http"
)

// RequiredDevicePollStatesScenarios contains only the selected unpaired frozen cases.
var RequiredDevicePollStatesScenarios = []string{"device_poll.pending", "device_poll.denied", "device_poll.expired"}

func DevicePollStatesAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/auth/device/poll"}, RequiredDevicePollStatesScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "pollDeviceLogin", "device poll states"); err != nil {
		return nil, err
	}
	return selected, nil
}
