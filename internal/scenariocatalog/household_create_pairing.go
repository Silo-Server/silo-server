package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
)

var RequiredHouseholdCreateScenarios = []string{"profiles_create.ok", "profiles_create.meaning", "profiles_create.admin_any_profile", "profiles_create.secondary_forbidden", "profiles_create.no_profile_forbidden", "profiles_create.locked_primary_unverified", "profiles_create.name_conflict", "profiles_create.limit", "profiles_create.missing_name", "profiles_create.bad_avatar", "profiles_create.bad_quality", "profiles_create.malformed", "profiles_create.shape", "profiles_create.unknown_library_ids", "profiles_create.other_account_profile", "profiles_create.no_token"}

const householdCreateRoute = "/api/v1/profiles/"
const householdCreateSpecs = `{"profiles_create.ok":{"request":{"path":"/api/v1/profiles/","body":{"name":"Fixture New"}},"translated":{"path":"/api/v2/profiles","body":{"name":"Fixture New"}},"principal":{"class":"primary_profile"},"status":201,"v2_status":201,"fresh":true},"profiles_create.meaning":{"request":{"path":"/api/v1/profiles/","body":{"name":"  Fixture New  ","pin":"1357","avatar":"avatar-2"}},"translated":{"path":"/api/v2/profiles","body":{"name":"  Fixture New  ","pin":"1357","avatar":"avatar-2"}},"principal":{"class":"primary_profile"},"status":201,"v2_status":201,"fresh":true},"profiles_create.admin_any_profile":{"request":{"path":"/api/v1/profiles/","body":{"name":"Fixture New"}},"translated":{"path":"/api/v2/profiles","body":{"name":"Fixture New"}},"principal":{"class":"acting_admin","profile":"admin_secondary"},"status":201,"v2_status":201,"fresh":true},"profiles_create.secondary_forbidden":{"request":{"path":"/api/v1/profiles/","body":{"name":"Fixture New"}},"translated":{"path":"/api/v2/profiles","body":{"name":"Fixture New"}},"principal":{"class":"profile"},"status":403,"v2_status":403,"fresh":false},"profiles_create.no_profile_forbidden":{"request":{"path":"/api/v1/profiles/","body":{"name":"Fixture New"}},"translated":{"path":"/api/v2/profiles","body":{"name":"Fixture New"}},"principal":{"class":"authenticated"},"status":403,"v2_status":403,"fresh":false},"profiles_create.locked_primary_unverified":{"request":{"path":"/api/v1/profiles/","body":{"name":"Fixture New"}},"translated":{"path":"/api/v2/profiles","body":{"name":"Fixture New"}},"principal":{"class":"profile","profile":"locked"},"status":403,"v2_status":403,"fresh":false},"profiles_create.name_conflict":{"request":{"path":"/api/v1/profiles/","body":{"name":" fixture teen "}},"translated":{"path":"/api/v2/profiles","body":{"name":" fixture teen "}},"principal":{"class":"primary_profile"},"status":409,"v2_status":409,"fresh":false},"profiles_create.limit":{"request":{"path":"/api/v1/profiles/","body":{"name":"Fixture New"},"repeat":2},"translated":{"path":"/api/v2/profiles","body":{"name":"Fixture New"},"repeat":2},"principal":{"class":"primary_profile"},"status":409,"v2_status":409,"fresh":true},"profiles_create.missing_name":{"request":{"path":"/api/v1/profiles/","body":{"name":"   "}},"translated":{"path":"/api/v2/profiles","body":{"name":"   "}},"principal":{"class":"primary_profile"},"status":400,"v2_status":422,"fresh":false},"profiles_create.bad_avatar":{"request":{"path":"/api/v1/profiles/","body":{"name":"X","avatar":"not-a-preset"}},"translated":{"path":"/api/v2/profiles","body":{"name":"X","avatar":"not-a-preset"}},"principal":{"class":"primary_profile"},"status":400,"v2_status":422,"fresh":false},"profiles_create.bad_quality":{"request":{"path":"/api/v1/profiles/","body":{"name":"X","max_playback_quality":"8K"}},"translated":{"path":"/api/v2/profiles","body":{"name":"X","max_playback_quality":"8K"}},"principal":{"class":"primary_profile"},"status":400,"v2_status":422,"fresh":false},"profiles_create.malformed":{"request":{"path":"/api/v1/profiles/","raw_body":"{"},"translated":{"path":"/api/v2/profiles","raw_body":"{"},"principal":{"class":"primary_profile"},"status":400,"v2_status":400,"fresh":false},"profiles_create.shape":{"request":{"path":"/api/v1/profiles/","body":{"name":"Fixture New"}},"translated":{"path":"/api/v2/profiles","body":{"name":"Fixture New"}},"principal":{"class":"primary_profile"},"status":201,"v2_status":201,"fresh":true},"profiles_create.unknown_library_ids":{"request":{"path":"/api/v1/profiles/","body":{"name":"Fixture Restricted","library_restrictions_enabled":true,"allowed_library_ids":[7,3]}},"translated":{"path":"/api/v2/profiles","body":{"name":"Fixture Restricted","library_restrictions_enabled":true,"allowed_library_ids":["7","3"]}},"principal":{"class":"primary_profile"},"status":500,"v2_status":422,"fresh":true},"profiles_create.other_account_profile":{"request":{"path":"/api/v1/profiles/","body":{"name":"Fixture New"}},"translated":{"path":"/api/v2/profiles","body":{"name":"Fixture New"}},"principal":{"class":"profile","profile":"admin_primary"},"status":404,"v2_status":404,"fresh":false},"profiles_create.no_token":{"request":{"path":"/api/v1/profiles/","body":{"name":"Fixture New"}},"translated":{"path":"/api/v2/profiles","body":{"name":"Fixture New"}},"principal":{"class":"public"},"status":401,"v2_status":401,"fresh":false}}`

func HouseholdCreateAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{householdCreateRoute}, RequiredHouseholdCreateScenarios)
	if err != nil {
		return nil, err
	}
	var specs map[string]struct {
		Request    Request   `json:"request"`
		Translated Request   `json:"translated"`
		Fresh      bool      `json:"fresh"`
		Principal  Principal `json:"principal"`
		Status     int       `json:"status"`
		V2Status   int       `json:"v2_status"`
	}
	if err := json.Unmarshal([]byte(householdCreateSpecs), &specs); err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				spec := specs[s.ID]
				pair := s.V2Expectation
				original, translated := s.Request, pair.Request
				var wantBody, wantV2Body, oldBody, newBody any
				for _, v := range []struct {
					raw    json.RawMessage
					target *any
				}{{spec.Request.Body, &wantBody}, {spec.Translated.Body, &wantV2Body}, {original.Body, &oldBody}, {translated.Body, &newBody}} {
					if len(v.raw) > 0 {
						if err := json.Unmarshal(v.raw, v.target); err != nil {
							return nil, fmt.Errorf("%s: invalid body", s.ID)
						}
					}
				}
				if !reflect.DeepEqual(wantBody, oldBody) || !reflect.DeepEqual(wantV2Body, newBody) {
					return nil, fmt.Errorf("%s: changed body", s.ID)
				}
				original.Body = nil
				translated.Body = nil
				spec.Request.Body = nil
				wantTranslated := spec.Translated
				wantTranslated.Body = nil
				method, operation := http.MethodPost, "createProfile"
				if !reflect.DeepEqual(original, spec.Request) || !reflect.DeepEqual(translated, wantTranslated) || !reflect.DeepEqual(s.Principal, spec.Principal) || pair.Principal != nil || pair.Method != method || pair.OperationID != operation || s.Expect.Status != spec.Status || pair.Expect.Status != spec.V2Status || s.FreshState != spec.Fresh || len(s.Requires) != 0 || len(s.Settings) != 0 || len(s.Then) != 0 || len(pair.Then) != 0 {
					return nil, fmt.Errorf("%s: changed household exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
