package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
)

var RequiredHouseholdDeletePINScenarios = []string{"profiles_delete.secondary_forbidden", "profiles_delete.not_found", "profiles_delete.shape", "profiles_delete.other_account_profile", "profiles_delete.other_account_path", "profiles_delete.no_token", "verify_pin.shape", "verify_pin.no_pin_profile", "verify_pin.other_account", "verify_pin.missing", "verify_pin.malformed", "verify_pin.other_account_profile", "verify_pin.other_account_path", "verify_pin.no_token"}

const householdDeleteRoute = "/api/v1/profiles/{id}"
const householdPINRoute = "/api/v1/profiles/{id}/verify-pin"
const householdDeletePINSpecs = `{"profiles_delete.secondary_forbidden":{"request":{"path":"/api/v1/profiles/${profile_missing}"},"principal":{"class":"profile"},"status":403,"v2_status":403},"profiles_delete.not_found":{"request":{"path":"/api/v1/profiles/${profile_missing}"},"principal":{"class":"primary_profile"},"status":404,"v2_status":404},"profiles_delete.shape":{"request":{"path":"/api/v1/profiles/${profile_child}"},"principal":{"class":"primary_profile"},"status":204,"v2_status":204},"profiles_delete.other_account_profile":{"request":{"path":"/api/v1/profiles/${profile_secondary}"},"principal":{"class":"profile","profile":"admin_primary"},"status":404,"v2_status":404},"profiles_delete.other_account_path":{"request":{"path":"/api/v1/profiles/${profile_admin_secondary}"},"principal":{"class":"primary_profile"},"status":404,"v2_status":404},"profiles_delete.no_token":{"request":{"path":"/api/v1/profiles/${profile_secondary}"},"principal":{"class":"public"},"status":401,"v2_status":401},"verify_pin.shape":{"request":{"path":"/api/v1/profiles/${profile_locked}/verify-pin","body":{"pin":"${locked_pin}"}},"principal":{"class":"authenticated"},"status":200,"v2_status":200},"verify_pin.no_pin_profile":{"request":{"path":"/api/v1/profiles/${profile_secondary}/verify-pin","body":{"pin":"1234"}},"principal":{"class":"authenticated"},"status":404,"v2_status":404},"verify_pin.other_account":{"request":{"path":"/api/v1/profiles/${profile_locked}/verify-pin","body":{"pin":"${locked_pin}"}},"principal":{"class":"admin"},"status":404,"v2_status":404},"verify_pin.missing":{"request":{"path":"/api/v1/profiles/${profile_locked}/verify-pin","body":{"pin":""}},"principal":{"class":"authenticated"},"status":400,"v2_status":422},"verify_pin.malformed":{"request":{"path":"/api/v1/profiles/${profile_locked}/verify-pin","raw_body":"x"},"principal":{"class":"authenticated"},"status":400,"v2_status":400},"verify_pin.other_account_profile":{"request":{"path":"/api/v1/profiles/${profile_locked}/verify-pin","body":{"pin":"1"}},"principal":{"class":"profile","profile":"admin_primary"},"status":404,"v2_status":404},"verify_pin.other_account_path":{"request":{"path":"/api/v1/profiles/${profile_admin_locked}/verify-pin","body":{"pin":"${admin_locked_pin}"}},"principal":{"class":"primary_profile"},"status":404,"v2_status":404},"verify_pin.no_token":{"request":{"path":"/api/v1/profiles/${profile_locked}/verify-pin","body":{"pin":"1"}},"principal":{"class":"public"},"status":401,"v2_status":401}}`

func HouseholdDeletePINAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodDelete, []string{householdDeleteRoute}, RequiredHouseholdDeletePINScenarios[:6])
	if err != nil {
		return nil, err
	}
	pins, err := requiredAcceptance(catalogs, http.MethodPost, []string{householdPINRoute}, RequiredHouseholdDeletePINScenarios[6:])
	if err != nil {
		return nil, err
	}
	selected = append(selected, pins...)
	var specs map[string]struct {
		Request   Request   `json:"request"`
		Principal Principal `json:"principal"`
		Status    int       `json:"status"`
		V2Status  int       `json:"v2_status"`
	}
	if err := json.Unmarshal([]byte(householdDeletePINSpecs), &specs); err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				spec := specs[s.ID]
				pair := s.V2Expectation
				original, translated := s.Request, pair.Request
				var wantBody, oldBody, newBody any
				for _, v := range []struct {
					raw    json.RawMessage
					target *any
				}{{spec.Request.Body, &wantBody}, {original.Body, &oldBody}, {translated.Body, &newBody}} {
					if len(v.raw) > 0 {
						if err := json.Unmarshal(v.raw, v.target); err != nil {
							return nil, fmt.Errorf("%s: invalid body", s.ID)
						}
					}
				}
				if !reflect.DeepEqual(wantBody, oldBody) || !reflect.DeepEqual(wantBody, newBody) {
					return nil, fmt.Errorf("%s: changed body", s.ID)
				}
				original.Body = nil
				translated.Body = nil
				spec.Request.Body = nil
				wantTranslated := spec.Request
				wantTranslated.Path = strings.Replace(wantTranslated.Path, "/api/v1/", "/api/v2/", 1)
				method, operation := http.MethodDelete, "deleteProfile"
				if strings.HasPrefix(s.ID, "verify_pin.") {
					method, operation = http.MethodPost, "verifyProfilePIN"
				}
				if !reflect.DeepEqual(original, spec.Request) || !reflect.DeepEqual(translated, wantTranslated) || !reflect.DeepEqual(s.Principal, spec.Principal) || pair.Principal != nil || pair.Method != method || pair.OperationID != operation || s.Expect.Status != spec.Status || pair.Expect.Status != spec.V2Status || len(s.Requires) != 0 || len(s.Settings) != 0 || len(s.Then) != 0 || len(pair.Then) != 0 {
					return nil, fmt.Errorf("%s: changed household exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
