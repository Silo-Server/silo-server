package scenariocatalog

import (
	"net/http"
)

// RequiredHandoffRefusalsScenarios contains only the selected unpaired frozen cases.
var RequiredHandoffRefusalsScenarios = []string{"handoff.no_profile", "handoff.locked_unverified", "handoff.purpose_mismatch", "handoff.other_account_profile", "handoff.no_token"}

func HandoffRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/auth/device/approve-handoff"}, RequiredHandoffRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptanceScenarios(selected, "approveDeviceHandoff", "handoff refusals"); err != nil {
		return nil, err
	}
	return selected, nil
}
