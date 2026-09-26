package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
)

const (
	resourcesLegacyPath       = "/api/v1/admin/system/resources"
	resourcesOperation        = "getAdminSystemResources"
	handoffLegacyPath         = "/api/v1/auth/device/approve-handoff"
	handoffOperation          = "approveDeviceHandoff"
	impersonationEndPath      = "/api/v1/auth/impersonation/end"
	impersonationEndOperation = "endImpersonation"
	resourcesAdminPrincipal   = "acting_admin"
)

// RequiredResourcesHandoffImpersonationScenarios fixes the selected unpaired frozen
// cases: the unsampled host resource read, successful remote-playback handoff
// approvals, and impersonation end. The hw-accel successes stay unpaired here
// because this checkout carries no v2 hardware operation.
var RequiredResourcesHandoffImpersonationScenarios = []string{
	"resources.ok", "resources.unsampled", "resources.shape", "resources.error_shape",
	"handoff.ok", "handoff.locked_verified", "handoff.meaning", "handoff.shape",
	"imp_end.ok", "imp_end.meaning", "imp_end.shape",
}

func ResourcesHandoffImpersonationAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	var selected []*Catalog
	for _, group := range []struct {
		method, path string
		ids          []string
	}{
		{http.MethodGet, resourcesLegacyPath, RequiredResourcesHandoffImpersonationScenarios[:4]},
		{http.MethodPost, handoffLegacyPath, RequiredResourcesHandoffImpersonationScenarios[4:8]},
		{http.MethodPost, impersonationEndPath, RequiredResourcesHandoffImpersonationScenarios[8:]},
	} {
		rows, err := requiredAcceptance(catalogs, group.method, []string{group.path}, group.ids)
		if err != nil {
			return nil, err
		}
		selected = append(selected, rows...)
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				p := s.V2Expectation
				op, principal, repeat, steps := resourcesOperation, Principal{Class: resourcesAdminPrincipal}, 0, 0
				switch strings.Split(s.ID, ".")[0] {
				case "resources":
					if s.ID == "resources.error_shape" {
						principal = Principal{Class: lifecyclePublicPrincipal}
					}
				case "handoff":
					op, principal = handoffOperation, Principal{Class: passwordSessionsPrimaryPrincipal}
					if s.ID == "handoff.locked_verified" {
						principal = Principal{Class: deviceRemovalProfilePrincipal, Profile: "locked", Verified: true}
					}
					if s.ID == "handoff.meaning" {
						steps = 1
					}
				case "imp_end":
					op, principal = impersonationEndOperation, Principal{Class: decisionAuthenticatedPrincipal}
					if s.ID == "imp_end.meaning" {
						repeat = 2
					}
				}
				if p.OperationID != op || p.Method != r.Method || p.Principal != nil || !reflect.DeepEqual(s.Principal, principal) || s.Request.Repeat != repeat || len(s.Settings) != 0 || len(s.Requires) != 0 || len(s.Then) != steps || len(p.Then) != steps || !passwordSessionRequestSame(s.Request, p.Request) {
					return nil, fmt.Errorf("%s: changed frozen exchange", s.ID)
				}
				for i, step := range s.Then {
					v := p.Then[i]
					if v.OperationID != decisionPollOperation || v.Method != step.Method || len(v.FromPrevious) != 0 || !reflect.DeepEqual(v.Principal, step.Principal) || !passwordSessionRequestSame(step.Request, v.Request) {
						return nil, fmt.Errorf("%s: changed follow-up", s.ID)
					}
				}
			}
		}
	}
	return selected, nil
}
