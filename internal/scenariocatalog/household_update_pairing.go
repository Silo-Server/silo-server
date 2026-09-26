package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
)

var RequiredHouseholdUpdateScenarios = []string{"profiles_update.self_service", "profiles_update.other_profile", "profiles_update.not_found", "profiles_update.name_conflict", "profiles_update.blank_name", "profiles_update.bad_quality", "profiles_update.shape", "profiles_update.other_account_profile", "profiles_update.other_account_path", "profiles_update.no_token"}

const householdUpdateRoute = "/api/v1/profiles/{id}"
const householdUpdateSpecs = `{"profiles_update.self_service":{"request":{"path":"/api/v1/profiles/${profile_secondary}","body":{"auto_skip_credits":true}},"principal":{"class":"profile"},"status":200,"v2_status":200},"profiles_update.other_profile":{"request":{"path":"/api/v1/profiles/${profile_child}","body":{"name":"X"}},"principal":{"class":"profile"},"status":403,"v2_status":403},"profiles_update.not_found":{"request":{"path":"/api/v1/profiles/${profile_missing}","body":{"name":"X"}},"principal":{"class":"primary_profile"},"status":404,"v2_status":404},"profiles_update.name_conflict":{"request":{"path":"/api/v1/profiles/${profile_secondary}","body":{"name":"Fixture Kid"}},"principal":{"class":"primary_profile"},"status":409,"v2_status":409},"profiles_update.blank_name":{"request":{"path":"/api/v1/profiles/${profile_secondary}","body":{"name":" "}},"principal":{"class":"primary_profile"},"status":400,"v2_status":422},"profiles_update.bad_quality":{"request":{"path":"/api/v1/profiles/${profile_secondary}","body":{"max_playback_quality":"potato"}},"principal":{"class":"primary_profile"},"status":400,"v2_status":422},"profiles_update.shape":{"request":{"path":"/api/v1/profiles/${profile_secondary}","body":{"auto_skip_recap":true}},"principal":{"class":"primary_profile"},"status":200,"v2_status":200},"profiles_update.other_account_profile":{"request":{"path":"/api/v1/profiles/${profile_secondary}","body":{"name":"X"}},"principal":{"class":"profile","profile":"admin_primary"},"status":404,"v2_status":404},"profiles_update.other_account_path":{"request":{"path":"/api/v1/profiles/${profile_admin_primary}","body":{"name":"Hijacked"}},"principal":{"class":"primary_profile"},"status":404,"v2_status":404},"profiles_update.no_token":{"request":{"path":"/api/v1/profiles/${profile_secondary}","body":{"name":"X"}},"principal":{"class":"public"},"status":401,"v2_status":401}}`

func HouseholdUpdateAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPut, []string{householdUpdateRoute}, RequiredHouseholdUpdateScenarios)
	if err != nil {
		return nil, err
	}
	var specs map[string]struct {
		Request   Request   `json:"request"`
		Principal Principal `json:"principal"`
		Status    int       `json:"status"`
		V2Status  int       `json:"v2_status"`
	}
	if err := json.Unmarshal([]byte(householdUpdateSpecs), &specs); err != nil {
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
				method, operation := http.MethodPatch, "updateProfile"
				if !reflect.DeepEqual(original, spec.Request) || !reflect.DeepEqual(translated, wantTranslated) || !reflect.DeepEqual(s.Principal, spec.Principal) || pair.Principal != nil || pair.Method != method || pair.OperationID != operation || s.Expect.Status != spec.Status || pair.Expect.Status != spec.V2Status || len(s.Requires) != 0 || len(s.Settings) != 0 || len(s.Then) != 0 || len(pair.Then) != 0 {
					return nil, fmt.Errorf("%s: changed household exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
