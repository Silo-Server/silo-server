package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
)

// RequiredAdminInvitationRefusalsScenarios is the complete reserved frozen cohort.
var RequiredAdminInvitationRefusalsScenarios = []string{"adm_inv_list.admin_secondary_profile", "adm_inv_list.non_admin", "adm_inv_list.no_token"}

const (
	adminInvitationLegacyPath          = "/api/v1/admin/invitations/"
	adminInvitationAuthorizationHeader = "Authorization"
	adminInvitationPublicPrincipal     = "public"
	adminInvitationAdminPrincipal      = "acting_admin"
	adminInvitationMemberPrincipal     = "primary_profile"
)

func AdminInvitationRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{adminInvitationLegacyPath}, RequiredAdminInvitationRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: adminInvitationLegacyPath}
				principal := Principal{Class: adminInvitationPublicPrincipal}
				status := http.StatusUnauthorized
				switch s.ID {
				case "adm_inv_list.admin_secondary_profile":
					principal = Principal{Class: adminInvitationAdminPrincipal, Profile: "admin_secondary"}
					status = http.StatusForbidden
				case "adm_inv_list.non_admin":
					principal = Principal{Class: adminInvitationMemberPrincipal}
					status = http.StatusForbidden
				}
				translated := original
				translated.Path = "/api/v2/admin/invitations"
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, principal) || pair.Principal != nil ||
					pair.Method != http.MethodGet || pair.OperationID != "listAdminInvitations" ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != status || pair.Expect.Status != status {
					return nil, fmt.Errorf("%s: unsupported administrator invitation refusal acceptance exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
