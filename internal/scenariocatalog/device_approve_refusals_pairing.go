package scenariocatalog

import (
	"net/http"
)

// RequiredDeviceApproveRefusalsScenarios contains only the selected unpaired frozen cases.
var RequiredDeviceApproveRefusalsScenarios = []string{"approve.expired", "approve.purpose_mismatch", "approve.not_found", "approve.no_token"}

func DeviceApproveRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/auth/device/approve"}, RequiredDeviceApproveRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "approveDeviceLogin", "device approve refusals"); err != nil {
		return nil, err
	}
	return selected, nil
}
