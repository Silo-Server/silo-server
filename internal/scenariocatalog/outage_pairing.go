package scenariocatalog

import (
	"fmt"
	"net/http"
)

// frozenOutageRequirement is the requirement every outage case must carry:
// the executor runs it on its offline router, whose pool targets a closed
// loopback port so every query fails while the routes stay registered.
const frozenOutageRequirement = "database_unavailable"

const (
	outageSetupStatusPath = "/api/v1/auth/setup"
	outageSetupStatusOp   = "getSetupStatus"
)

// RequiredOutageScenarios fixes the three frozen public reads whose original
// oracle is recorded under an unreachable database. They were unpaired only
// because no acceptance runner exercised that state.
var RequiredOutageScenarios = []string{"setup_status.db_down", "signup_status.db_down", "capability.ok"}

// outageOperations names the one v2 operation each frozen outage case pairs with.
var outageOperations = map[string]string{
	"setup_status.db_down":  outageSetupStatusOp,
	"signup_status.db_down": "getSignupStatus",
	"capability.ok":         "getDeviceLoginCapability",
}

// OutageAcceptance selects the three frozen database-unavailable cases and
// refuses any that lost its outage requirement, gained a database
// requirement, or pairs with an unexpected operation or follow-up.
func OutageAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	paths := []string{outageSetupStatusPath, signupCodesLegacyRoute, "/api/v1/auth/device/capability"}
	selected, err := requiredAcceptance(catalogs, http.MethodGet, paths, RequiredOutageScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				if len(s.Requires) != 1 || s.Requires[0] != frozenOutageRequirement {
					return nil, fmt.Errorf("%s: outage case must require exactly %s", s.ID, frozenOutageRequirement)
				}
				if s.Principal.Class != lifecyclePublicPrincipal || len(s.Settings) != 0 || s.FreshState {
					return nil, fmt.Errorf("%s: outage case must be a plain public read", s.ID)
				}
				if s.V2Expectation.OperationID != outageOperations[s.ID] || len(s.Then) != 0 || len(s.V2Expectation.Then) != 0 || s.V2Expectation.Principal != nil {
					return nil, fmt.Errorf("%s: unsupported outage acceptance sequence", s.ID)
				}
			}
		}
	}
	return selected, nil
}
