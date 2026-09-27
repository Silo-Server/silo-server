package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
)

// RequiredInviteCodeRefusalsScenarios is the complete reserved frozen cohort.
var RequiredInviteCodeRefusalsScenarios = []string{"codes_list.admin_secondary_profile", "codes_list.non_admin", "codes_list.no_token"}

const (
	inviteCodeLegacyPath          = "/api/v1/admin/invite-codes/"
	inviteCodeAuthorizationHeader = "Authorization"
	inviteCodePublicPrincipal     = "public"
	inviteCodeAdminPrincipal      = "acting_admin"
	inviteCodeMemberPrincipal     = "primary_profile"
)

func InviteCodeRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{inviteCodeLegacyPath}, RequiredInviteCodeRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: inviteCodeLegacyPath}
				principal := Principal{Class: inviteCodePublicPrincipal}
				status := http.StatusUnauthorized
				switch s.ID {
				case "codes_list.admin_secondary_profile":
					principal = Principal{Class: inviteCodeAdminPrincipal, Profile: "admin_secondary"}
					status = http.StatusForbidden
				case "codes_list.non_admin":
					principal = Principal{Class: inviteCodeMemberPrincipal}
					status = http.StatusForbidden
				}
				translated := original
				translated.Path = "/api/v2/admin/invite-codes"
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, principal) || pair.Principal != nil ||
					pair.Method != http.MethodGet || pair.OperationID != "listAdminInviteCodes" ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != status || pair.Expect.Status != status {
					return nil, fmt.Errorf("%s: unsupported administrator invite-code refusal acceptance exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
