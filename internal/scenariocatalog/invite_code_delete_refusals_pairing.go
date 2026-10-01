package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
)

// RequiredInviteCodeDeleteRefusalsScenarios is the complete reserved frozen cohort.
var RequiredInviteCodeDeleteRefusalsScenarios = []string{"codes_delete.admin_secondary_profile", "codes_delete.non_admin", "codes_delete.no_token"}

const inviteCodeDeleteSecondaryProfile = "admin_secondary"

func InviteCodeDeleteRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodDelete, []string{"/api/v1/admin/invite-codes/{id}"}, RequiredInviteCodeDeleteRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: inviteCodeLegacyPath + "${invite_code_id}"}
				principal := Principal{Class: inviteCodePublicPrincipal}
				status := http.StatusUnauthorized
				switch s.ID {
				case "codes_delete.no_token":
					original.Path = inviteCodeLegacyPath + "1"
				case "codes_delete.admin_secondary_profile":
					principal = Principal{Class: inviteCodeAdminPrincipal, Profile: inviteCodeDeleteSecondaryProfile}
					status = http.StatusForbidden
				case "codes_delete.non_admin":
					principal = Principal{Class: inviteCodeMemberPrincipal}
					status = http.StatusForbidden
				}
				translated := original
				translated.Path = "/api/v2/admin/invite-codes/" + original.Path[len(inviteCodeLegacyPath):]
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, principal) || pair.Principal != nil ||
					pair.Method != http.MethodDelete || pair.OperationID != "deleteAdminInviteCode" ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != status || pair.Expect.Status != status {
					return nil, fmt.Errorf("%s: unsupported administrator invite-code refusal acceptance exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
