package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
)

const (
	hardwareLegacyPath = "/api/v1/admin/system/hw-accel"
	hardwareOperation  = "getAdminHardwareAcceleration"
)

// RequiredHardwareInventoryScenarios fixes the four remaining frozen hardware
// cases: three local inventory reads and the non-admin profile refusal shape.
// The three authorization refusals stay in RequiredHardwareRefusalScenarios.
var RequiredHardwareInventoryScenarios = []string{
	"hwaccel.ok", "hwaccel.meaning", "hwaccel.shape", "hwaccel.error_shape",
}

func HardwareInventoryAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{hardwareLegacyPath}, RequiredHardwareInventoryScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				p := s.V2Expectation
				principal, status := Principal{Class: resourcesAdminPrincipal}, http.StatusOK
				if s.ID == "hwaccel.error_shape" {
					principal, status = Principal{Class: passwordSessionsPrimaryPrincipal}, http.StatusForbidden
				}
				if p.OperationID != hardwareOperation || p.Method != r.Method || p.Principal != nil || !reflect.DeepEqual(s.Principal, principal) || s.Request.Repeat != 0 || len(s.Settings) != 0 || len(s.Requires) != 0 || len(s.Then) != 0 || len(p.Then) != 0 || s.Expect.Status != status || p.Expect.Status != status || !passwordSessionRequestSame(s.Request, p.Request) {
					return nil, fmt.Errorf("%s: changed hardware inventory exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
