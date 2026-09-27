package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
)

var RequiredInviteCodeCreationScenarios = []string{"codes_create.ok", "codes_create.generated", "codes_create.explicit_code", "codes_create.shape"}

const inviteCodeCreationSpecs = `{"codes_create.ok":{"id":"codes_create.ok","category":"status_headers","description":"Creating a code answers 201 with the code.","principal":{"class":"acting_admin"},"request":{"path":"/api/v1/admin/invite-codes/","body":{"label":"fixture","max_uses":3}},"expect":{"status":201,"headers":[{"name":"Content-Type","op":"equals","value":"application/json"}],"body":[{"pointer":"","op":"keys_equal","value":["id","code","label","max_uses","use_count","created_by","enabled","created_at","updated_at"]}]},"fresh_state":true,"v2_expectation":{"kind":"intentional_difference","summary":"V1 omitted codes remain server-generated. V2 requires a caller-chosen code generated once before dispatch; explicit codes remain verbatim. String IDs/no-store and full issuance effects are checked.","recorded_in":"internal/scenariocatalog/executor/invite_code_creation_pairing_test.go","operation_id":"createAdminInviteCode","method":"POST","request":{"path":"/api/v2/admin/invite-codes","body":{"label":"fixture","max_uses":3,"code":"${caller_invite_code}"}},"expect":{"status":201,"headers":[{"name":"Content-Type","op":"equals","value":"application/json"},{"name":"Cache-Control","op":"equals","value":"no-store"}],"body":[{"pointer":"","op":"keys_equal","value":["id","code","label","max_uses","use_count","created_by","enabled","created_at","updated_at"]},{"pointer":"/id","op":"type","value":"string"},{"pointer":"/created_by","op":"type","value":"string"},{"pointer":"/code","op":"equals","value":"${caller_invite_code}"},{"pointer":"/label","op":"equals","value":"fixture"},{"pointer":"/max_uses","op":"equals","value":3},{"pointer":"/use_count","op":"equals","value":0},{"pointer":"/enabled","op":"equals","value":true},{"pointer":"/created_at","op":"rfc3339"},{"pointer":"/updated_at","op":"rfc3339"}]}}},"codes_create.generated":{"id":"codes_create.generated","category":"data_meaning","description":"An omitted code is generated (8 characters); use_count starts at 0 and enabled at true.","principal":{"class":"acting_admin"},"request":{"path":"/api/v1/admin/invite-codes/","body":{"label":"fixture","max_uses":3}},"expect":{"status":201,"body":[{"pointer":"/code","op":"matches","value":"^[A-Z0-9]{8}$"},{"pointer":"/use_count","op":"equals","value":0},{"pointer":"/enabled","op":"equals","value":true},{"pointer":"/max_uses","op":"equals","value":3}]},"fresh_state":true,"v2_expectation":{"kind":"intentional_difference","summary":"V1 omitted codes remain server-generated. V2 requires a caller-chosen code generated once before dispatch; explicit codes remain verbatim. String IDs/no-store and full issuance effects are checked.","recorded_in":"internal/scenariocatalog/executor/invite_code_creation_pairing_test.go","operation_id":"createAdminInviteCode","method":"POST","request":{"path":"/api/v2/admin/invite-codes","body":{"label":"fixture","max_uses":3,"code":"${caller_invite_code}"}},"expect":{"status":201,"headers":[{"name":"Content-Type","op":"equals","value":"application/json"},{"name":"Cache-Control","op":"equals","value":"no-store"}],"body":[{"pointer":"","op":"keys_equal","value":["id","code","label","max_uses","use_count","created_by","enabled","created_at","updated_at"]},{"pointer":"/id","op":"type","value":"string"},{"pointer":"/created_by","op":"type","value":"string"},{"pointer":"/code","op":"equals","value":"${caller_invite_code}"},{"pointer":"/label","op":"equals","value":"fixture"},{"pointer":"/max_uses","op":"equals","value":3},{"pointer":"/use_count","op":"equals","value":0},{"pointer":"/enabled","op":"equals","value":true},{"pointer":"/created_at","op":"rfc3339"},{"pointer":"/updated_at","op":"rfc3339"}]}}},"codes_create.explicit_code":{"id":"codes_create.explicit_code","category":"filtering","description":"A supplied code is stored verbatim.","principal":{"class":"acting_admin"},"request":{"path":"/api/v1/admin/invite-codes/","body":{"code":"FIXTURE7","label":"explicit","max_uses":1}},"expect":{"status":201,"body":[{"pointer":"/code","op":"equals","value":"FIXTURE7"}]},"fresh_state":true,"v2_expectation":{"kind":"intentional_difference","summary":"V1 omitted codes remain server-generated. V2 requires a caller-chosen code generated once before dispatch; explicit codes remain verbatim. String IDs/no-store and full issuance effects are checked.","recorded_in":"internal/scenariocatalog/executor/invite_code_creation_pairing_test.go","operation_id":"createAdminInviteCode","method":"POST","request":{"path":"/api/v2/admin/invite-codes","body":{"code":"FIXTURE7","label":"explicit","max_uses":1}},"expect":{"status":201,"headers":[{"name":"Content-Type","op":"equals","value":"application/json"},{"name":"Cache-Control","op":"equals","value":"no-store"}],"body":[{"pointer":"","op":"keys_equal","value":["id","code","label","max_uses","use_count","created_by","enabled","created_at","updated_at"]},{"pointer":"/id","op":"type","value":"string"},{"pointer":"/created_by","op":"type","value":"string"},{"pointer":"/code","op":"equals","value":"FIXTURE7"},{"pointer":"/label","op":"equals","value":"explicit"},{"pointer":"/max_uses","op":"equals","value":1},{"pointer":"/use_count","op":"equals","value":0},{"pointer":"/enabled","op":"equals","value":true},{"pointer":"/created_at","op":"rfc3339"},{"pointer":"/updated_at","op":"rfc3339"}]}}},"codes_create.shape":{"id":"codes_create.shape","category":"field_presence_nullability","description":"created_at/updated_at are RFC3339.","principal":{"class":"acting_admin"},"request":{"path":"/api/v1/admin/invite-codes/","body":{"label":"x","max_uses":1}},"expect":{"status":201,"body":[{"pointer":"/created_at","op":"rfc3339"},{"pointer":"/updated_at","op":"rfc3339"}]},"fresh_state":true,"v2_expectation":{"kind":"intentional_difference","summary":"V1 omitted codes remain server-generated. V2 requires a caller-chosen code generated once before dispatch; explicit codes remain verbatim. String IDs/no-store and full issuance effects are checked.","recorded_in":"internal/scenariocatalog/executor/invite_code_creation_pairing_test.go","operation_id":"createAdminInviteCode","method":"POST","request":{"path":"/api/v2/admin/invite-codes","body":{"label":"x","max_uses":1,"code":"${caller_invite_code}"}},"expect":{"status":201,"headers":[{"name":"Content-Type","op":"equals","value":"application/json"},{"name":"Cache-Control","op":"equals","value":"no-store"}],"body":[{"pointer":"","op":"keys_equal","value":["id","code","label","max_uses","use_count","created_by","enabled","created_at","updated_at"]},{"pointer":"/id","op":"type","value":"string"},{"pointer":"/created_by","op":"type","value":"string"},{"pointer":"/code","op":"equals","value":"${caller_invite_code}"},{"pointer":"/label","op":"equals","value":"x"},{"pointer":"/max_uses","op":"equals","value":1},{"pointer":"/use_count","op":"equals","value":0},{"pointer":"/enabled","op":"equals","value":true},{"pointer":"/created_at","op":"rfc3339"},{"pointer":"/updated_at","op":"rfc3339"}]}}}}`

func InviteCodeCreationAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/admin/invite-codes/"}, RequiredInviteCodeCreationScenarios)
	if err != nil {
		return nil, err
	}
	var specs map[string]any
	if err := json.Unmarshal([]byte(inviteCodeCreationSpecs), &specs); err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				raw, err := json.Marshal(s)
				if err != nil {
					return nil, err
				}
				var got any
				if err := json.Unmarshal(raw, &got); err != nil {
					return nil, err
				}
				if !reflect.DeepEqual(got, specs[s.ID]) {
					return nil, fmt.Errorf("%s: changed original or code creation exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
