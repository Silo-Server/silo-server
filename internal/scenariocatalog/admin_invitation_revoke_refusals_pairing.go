package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
)

// RequiredAdminInvitationRevokeRefusalsScenarios is the complete reserved frozen cohort.
var RequiredAdminInvitationRevokeRefusalsScenarios = []string{"adm_inv_revoke.admin_secondary_profile", "adm_inv_revoke.non_admin", "adm_inv_revoke.no_token"}

const (
	adminInvitationRevokeLegacyPath          = "/api/v1/admin/invitations/{id}"
	adminInvitationRevokeAuthorizationHeader = "Authorization"
	adminInvitationRevokePublicPrincipal     = "public"
	adminInvitationRevokeAdminPrincipal      = "acting_admin"
	adminInvitationRevokeMemberPrincipal     = "primary_profile"
)

func AdminInvitationRevokeRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodDelete, []string{adminInvitationRevokeLegacyPath}, RequiredAdminInvitationRevokeRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: "/api/v1/admin/invitations/1"}
				principal := Principal{Class: adminInvitationRevokePublicPrincipal}
				status := http.StatusUnauthorized
				switch s.ID {
				case "adm_inv_revoke.admin_secondary_profile":
					principal = Principal{Class: adminInvitationRevokeAdminPrincipal, Profile: "admin_secondary"}
					status = http.StatusForbidden
				case "adm_inv_revoke.non_admin":
					principal = Principal{Class: adminInvitationRevokeMemberPrincipal}
					status = http.StatusForbidden
				}
				translated := original
				translated.Path = "/api/v2/admin/invitations/1"
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, principal) || pair.Principal != nil ||
					pair.Method != http.MethodDelete || pair.OperationID != "revokeAdminInvitation" ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != status || pair.Expect.Status != status {
					return nil, fmt.Errorf("%s: unsupported administrator invitation revoke refusal acceptance exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
