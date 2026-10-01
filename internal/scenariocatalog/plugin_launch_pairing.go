package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
)

const (
	pluginLaunchLegacyPath = "/api/v1/auth/plugin-launch"
	pluginLaunchOperation  = "createPluginLaunch"
)

// RequiredPluginLaunchScenarios fixes the eight frozen plugin launch cases:
// four successful issuances and four refusals. Every case asserts the launch
// response itself; none needs a served plugin.
var RequiredPluginLaunchScenarios = []string{
	"plugin_launch.ok", "plugin_launch.secure_flag", "plugin_launch.meaning", "plugin_launch.shape",
	"plugin_launch.profile_validated", "plugin_launch.locked_profile", "plugin_launch.api_key", "plugin_launch.no_token",
}

// pluginLaunchExpected pins each case's principal and status on both
// transports; a changed principal, status or request is refused.
// pluginLaunchMissingProfile names the profile the profile_validated original targets.
const pluginLaunchMissingProfile = "missing"

var pluginLaunchExpected = map[string]struct {
	principal        Principal
	v1Status, status int
}{
	"plugin_launch.ok":                {Principal{Class: decisionAuthenticatedPrincipal}, http.StatusOK, http.StatusOK},
	"plugin_launch.secure_flag":       {Principal{Class: decisionAuthenticatedPrincipal}, http.StatusOK, http.StatusOK},
	"plugin_launch.meaning":           {Principal{Class: decisionAuthenticatedPrincipal}, http.StatusOK, http.StatusOK},
	"plugin_launch.shape":             {Principal{Class: decisionAuthenticatedPrincipal}, http.StatusOK, http.StatusOK},
	"plugin_launch.profile_validated": {Principal{Class: decisionAuthenticatedPrincipal, Profile: pluginLaunchMissingProfile}, http.StatusNotFound, http.StatusNotFound},
	"plugin_launch.locked_profile":    {Principal{Class: decisionAuthenticatedPrincipal, Profile: "locked"}, http.StatusForbidden, http.StatusForbidden},
	"plugin_launch.api_key":           {Principal{Class: bindingAPIKeyPrincipal}, http.StatusUnauthorized, http.StatusForbidden},
	"plugin_launch.no_token":          {Principal{Class: accountMePublicPrincipal}, http.StatusUnauthorized, http.StatusUnauthorized},
}

func PluginLaunchAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{pluginLaunchLegacyPath}, RequiredPluginLaunchScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				p := s.V2Expectation
				want := pluginLaunchExpected[s.ID]
				if p.OperationID != pluginLaunchOperation || p.Method != r.Method || p.Principal != nil || !reflect.DeepEqual(s.Principal, want.principal) || s.Request.Repeat != 0 || len(s.Settings) != 0 || len(s.Requires) != 0 || len(s.Then) != 0 || len(p.Then) != 0 || s.Expect.Status != want.v1Status || p.Expect.Status != want.status || !passwordSessionRequestSame(s.Request, p.Request) {
					return nil, fmt.Errorf("%s: changed plugin launch exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
