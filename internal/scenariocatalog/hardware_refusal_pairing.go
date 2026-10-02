package scenariocatalog

import (
	"fmt"
	"net/http"
)

// RequiredHardwareRefusalScenarios excludes successful hardware reads and their gates.
var RequiredHardwareRefusalScenarios = []string{
	"hwaccel.admin_secondary_profile", "hwaccel.non_admin", "hwaccel.no_token",
}

func HardwareRefusalAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{"/api/v1/admin/system/hw-accel"}, RequiredHardwareRefusalScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				if s.V2Expectation.OperationID != "getAdminHardwareAcceleration" || len(s.Then) != 0 || len(s.V2Expectation.Then) != 0 || len(s.Requires) != 0 || (s.Expect.Status != 401 && s.Expect.Status != 403) || s.V2Expectation.Expect.Status != s.Expect.Status {
					return nil, fmt.Errorf("%s: unsupported hardware refusal acceptance sequence", s.ID)
				}
			}
		}
	}
	return selected, nil
}
