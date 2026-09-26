package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
)

var RequiredAvatarScenarios = []string{"avatar_upload.typed_nil_panic", "avatar_upload.meaning", "avatar_upload.raw", "avatar_upload.missing_file", "avatar_upload.not_multipart", "avatar_upload.not_found", "avatar_upload.shape", "avatar_upload.any_profile", "avatar_upload.other_account_profile", "avatar_upload.other_account_path", "avatar_upload.no_token", "avatar_delete.ok", "avatar_delete.meaning", "avatar_delete.any_profile", "avatar_delete.not_found", "avatar_delete.shape", "avatar_delete.other_account_profile", "avatar_delete.other_account_path", "avatar_delete.no_token"}

func AvatarAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	var selected []*Catalog
	for _, g := range []struct {
		method string
		ids    []string
	}{{http.MethodPut, RequiredAvatarScenarios[:11]}, {http.MethodDelete, RequiredAvatarScenarios[11:]}} {
		rows, err := requiredAcceptance(catalogs, g.method, []string{"/api/v1/profiles/{id}/avatar"}, g.ids)
		if err != nil {
			return nil, err
		}
		selected = append(selected, rows...)
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				p := s.V2Expectation
				op := "uploadProfileAvatar"
				if strings.HasPrefix(s.ID, "avatar_delete.") {
					op = "deleteProfileAvatar"
				}
				principal := Principal{Class: passwordSessionsPrimaryPrincipal}
				switch strings.Split(s.ID, ".")[1] {
				case "any_profile":
					principal = Principal{Class: deviceRemovalProfilePrincipal}
				case "other_account_profile":
					principal = Principal{Class: deviceRemovalProfilePrincipal, Profile: "admin_primary"}
				case "no_token":
					principal = Principal{Class: lifecyclePublicPrincipal}
				}
				if p.OperationID != op || p.Method != r.Method || p.Principal != nil || !reflect.DeepEqual(s.Principal, principal) || s.Request.Repeat != 0 || len(s.Then) != 0 || len(p.Then) != 0 || len(s.Settings) != 0 || len(s.Requires) != 0 || !passwordSessionRequestSame(s.Request, p.Request) {
					return nil, fmt.Errorf("%s: changed avatar exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
