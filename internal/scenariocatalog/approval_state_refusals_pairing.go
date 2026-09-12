package scenariocatalog

import (
	"net/http"
)

// RequiredApprovalStateRefusalsScenarios contains only the selected unpaired frozen cases.
var RequiredApprovalStateRefusalsScenarios = []string{"approve.conflict", "approve.denied", "approve.disabled_user"}

func ApprovalStateRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/auth/device/approve"}, RequiredApprovalStateRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "approveDeviceLogin", "approval state refusals"); err != nil {
		return nil, err
	}
	return selected, nil
}
