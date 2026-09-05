package apiv2

import (
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
)

// TestProfileHeaderRequiredMatchesGateChain: the documented X-Profile-Id
// requirement is the one the gate chain enforces. Only profile_scoped runs
// RequireProfile; acting_admin and permission_gated accept an absent header,
// as v1's RequireActingAdmin does, so they document it as optional.
func TestProfileHeaderRequiredMatchesGateChain(t *testing.T) {
	cases := map[Class]*bool{
		ClassPublic:          nil,
		ClassAuthenticated:   nil,
		ClassProfileScoped:   ptr(true),
		ClassActingAdmin:     ptr(false),
		ClassPermissionGated: ptr(false),
	}
	for class, want := range cases {
		t.Run(string(class), func(t *testing.T) {
			op := &Operation{Operation: huma.Operation{Method: http.MethodGet, Path: Prefix + "/x", OperationID: "getX"}, Class: class}
			documentDeclaration(op, nil)
			var param *huma.Param
			for _, p := range op.Parameters {
				if p.In == "header" && p.Name == profileHeader {
					param = p
				}
			}
			if want == nil {
				if param != nil {
					t.Fatalf("%s documents %s", class, profileHeader)
				}
				return
			}
			if param == nil {
				t.Fatalf("%s does not document %s", class, profileHeader)
			}
			if param.Required != *want {
				t.Fatalf("%s: Required = %v, want %v", class, param.Required, *want)
			}
			if param.Description == "" {
				t.Fatalf("%s: header has no description", class)
			}
		})
	}
}
