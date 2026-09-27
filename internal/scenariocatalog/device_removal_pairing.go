package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
)

const deviceRemovalProfilePrincipal = "profile"

var RequiredDeviceRemovalScenarios = []string{
	"device_forget.other_profile", "device_forget.named_profile", "device_forget.named_profile_missing", "device_forget.shape", "device_forget.no_profile", "device_forget.other_account_profile", "device_forget.no_token",
	"device_clear.other_profile", "device_clear.named_profile", "device_clear.shape", "device_clear.no_profile", "device_clear.other_account_profile", "device_clear.no_token",
}

func DeviceRemovalAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	var selected []*Catalog
	for _, g := range []struct {
		path string
		ids  []string
	}{{"/api/v1/devices/{device_id}", RequiredDeviceRemovalScenarios[:7]}, {"/api/v1/devices/{device_id}/settings", RequiredDeviceRemovalScenarios[7:]}} {
		rows, err := requiredAcceptance(catalogs, http.MethodDelete, []string{g.path}, g.ids)
		if err != nil {
			return nil, err
		}
		selected = append(selected, rows...)
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				p := s.V2Expectation
				op := "forgetDevice"
				if strings.HasPrefix(s.ID, "device_clear.") {
					op = "clearDeviceSettings"
				}
				principal := Principal{Class: passwordSessionsPrimaryPrincipal}
				switch strings.Split(s.ID, ".")[1] {
				case "other_profile":
					principal = Principal{Class: deviceRemovalProfilePrincipal}
				case "other_account_profile":
					principal = Principal{Class: deviceRemovalProfilePrincipal, Profile: "admin_primary"}
				case "no_profile":
					principal = Principal{Class: decisionAuthenticatedPrincipal}
				case "no_token":
					principal = Principal{Class: lifecyclePublicPrincipal}
				}
				if p.OperationID != op || p.Method != http.MethodDelete || p.Principal != nil || !reflect.DeepEqual(s.Principal, principal) || s.Request.Repeat != 0 || len(s.Then) != 0 || len(p.Then) != 0 || len(s.Settings) != 0 || len(s.Requires) != 0 || !passwordSessionRequestSame(s.Request, p.Request) {
					return nil, fmt.Errorf("%s: changed device removal exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
