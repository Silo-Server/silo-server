package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
)

var RequiredOnboardingReadsScenarios = []string{"flow.ok", "flow.meaning", "flow.surface_filter", "flow.web_surface", "flow.bad_surface", "flow.child_filter", "flow.shape", "flow.no_profile", "flow.other_account_profile", "flow.no_token", "state.ok", "state.fresh", "state.shape", "state.no_profile", "state.other_account_profile", "state.no_token", "state.error_shape"}

const onboardingReadsSpecs = `{"flow.ok":{"request":{"path":"/api/v1/onboarding/flow"},"translated":{"path":"/api/v2/onboarding/flow"},"principal":{"class":"profile"},"status":200,"v2_status":200,"fresh":false,"then":[],"v2_then":[]},"flow.meaning":{"request":{"path":"/api/v1/onboarding/flow"},"translated":{"path":"/api/v2/onboarding/flow"},"principal":{"class":"profile"},"status":200,"v2_status":200,"fresh":false,"then":[],"v2_then":[]},"flow.surface_filter":{"request":{"path":"/api/v1/onboarding/flow","query":{"surface":"TV"}},"translated":{"path":"/api/v2/onboarding/flow","query":{"surface":"tv"}},"principal":{"class":"profile"},"status":200,"v2_status":200,"fresh":false,"then":[],"v2_then":[]},"flow.web_surface":{"request":{"path":"/api/v1/onboarding/flow"},"translated":{"path":"/api/v2/onboarding/flow"},"principal":{"class":"profile"},"status":200,"v2_status":200,"fresh":false,"then":[],"v2_then":[]},"flow.bad_surface":{"request":{"path":"/api/v1/onboarding/flow","query":{"surface":"watch"}},"translated":{"path":"/api/v2/onboarding/flow","query":{"surface":"watch"}},"principal":{"class":"profile"},"status":400,"v2_status":422,"fresh":false,"then":[],"v2_then":[]},"flow.child_filter":{"request":{"path":"/api/v1/onboarding/flow"},"translated":{"path":"/api/v2/onboarding/flow"},"principal":{"class":"child_profile"},"status":200,"v2_status":200,"fresh":false,"then":[{"method":"GET","description":"a non-child profile's flow carries the requests step","principal":{"class":"profile"},"request":{"path":"/api/v1/onboarding/flow"},"expect":{"status":200,"body":[{"pointer":"/steps","op":"non_empty"},{"pointer":"/steps/4/id","op":"equals","value":"requests"}]}}],"v2_then":[{"method":"GET","description":"a non-child profile's flow carries the requests step","principal":{"class":"profile"},"request":{"path":"/api/v2/onboarding/flow"},"expect":{"status":200,"body":[{"pointer":"/steps","op":"non_empty"},{"pointer":"/steps/4/id","op":"equals","value":"requests"}]},"operation_id":"getOnboardingFlow"}]},"flow.shape":{"request":{"path":"/api/v1/onboarding/flow"},"translated":{"path":"/api/v2/onboarding/flow"},"principal":{"class":"profile"},"status":200,"v2_status":200,"fresh":false,"then":[],"v2_then":[]},"flow.no_profile":{"request":{"path":"/api/v1/onboarding/flow"},"translated":{"path":"/api/v2/onboarding/flow"},"principal":{"class":"authenticated"},"status":400,"v2_status":422,"fresh":false,"then":[],"v2_then":[]},"flow.other_account_profile":{"request":{"path":"/api/v1/onboarding/flow"},"translated":{"path":"/api/v2/onboarding/flow"},"principal":{"class":"profile","profile":"admin_primary"},"status":404,"v2_status":404,"fresh":false,"then":[],"v2_then":[]},"flow.no_token":{"request":{"path":"/api/v1/onboarding/flow"},"translated":{"path":"/api/v2/onboarding/flow"},"principal":{"class":"public"},"status":401,"v2_status":401,"fresh":false,"then":[],"v2_then":[]},"state.ok":{"request":{"path":"/api/v1/onboarding/state"},"translated":{"path":"/api/v2/onboarding/state"},"principal":{"class":"profile"},"status":200,"v2_status":200,"fresh":false,"then":[],"v2_then":[]},"state.fresh":{"request":{"path":"/api/v1/onboarding/state"},"translated":{"path":"/api/v2/onboarding/state"},"principal":{"class":"profile"},"status":200,"v2_status":200,"fresh":false,"then":[],"v2_then":[]},"state.shape":{"request":{"path":"/api/v1/onboarding/state"},"translated":{"path":"/api/v2/onboarding/state"},"principal":{"class":"profile"},"status":200,"v2_status":200,"fresh":false,"then":[],"v2_then":[]},"state.no_profile":{"request":{"path":"/api/v1/onboarding/state"},"translated":{"path":"/api/v2/onboarding/state"},"principal":{"class":"authenticated"},"status":400,"v2_status":422,"fresh":false,"then":[],"v2_then":[]},"state.other_account_profile":{"request":{"path":"/api/v1/onboarding/state"},"translated":{"path":"/api/v2/onboarding/state"},"principal":{"class":"profile","profile":"admin_primary"},"status":404,"v2_status":404,"fresh":false,"then":[],"v2_then":[]},"state.no_token":{"request":{"path":"/api/v1/onboarding/state"},"translated":{"path":"/api/v2/onboarding/state"},"principal":{"class":"public"},"status":401,"v2_status":401,"fresh":false,"then":[],"v2_then":[]},"state.error_shape":{"request":{"path":"/api/v1/onboarding/state"},"translated":{"path":"/api/v2/onboarding/state"},"principal":{"class":"authenticated"},"status":400,"v2_status":422,"fresh":false,"then":[],"v2_then":[]}}`

func OnboardingReadsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{"/api/v1/onboarding/flow", "/api/v1/onboarding/state"}, RequiredOnboardingReadsScenarios)
	if err != nil {
		return nil, err
	}
	var specs map[string]struct {
		Request    Request   `json:"request"`
		Translated Request   `json:"translated"`
		Fresh      bool      `json:"fresh"`
		Then       []Step    `json:"then"`
		V2Then     []V2Step  `json:"v2_then"`
		Principal  Principal `json:"principal"`
		Status     int       `json:"status"`
		V2Status   int       `json:"v2_status"`
	}
	if err := json.Unmarshal([]byte(onboardingReadsSpecs), &specs); err != nil {
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
				method, operation := http.MethodGet, "getOnboardingFlow"
				if s.ID[:6] == "state." {
					operation = "getOnboardingState"
				}
				if !reflect.DeepEqual(original, spec.Request) || !reflect.DeepEqual(translated, wantTranslated) || !reflect.DeepEqual(s.Principal, spec.Principal) || pair.Principal != nil || pair.Method != method || pair.OperationID != operation || s.Expect.Status != spec.Status || pair.Expect.Status != spec.V2Status || s.FreshState != spec.Fresh || len(s.Requires) != 0 || len(s.Settings) != 0 || !stepsMatch(s.Then, spec.Then) || !stepsMatch(pair.Then, spec.V2Then) {
					return nil, fmt.Errorf("%s: changed household exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}

// Empty optional sequences are equivalent; every supplied step stays exact.
func stepsMatch[T any](a, b []T) bool { return len(a) == 0 && len(b) == 0 || reflect.DeepEqual(a, b) }
