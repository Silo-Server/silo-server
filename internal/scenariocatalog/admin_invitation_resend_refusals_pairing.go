package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
)

// RequiredAdminInvitationResendRefusalsScenarios is the complete reserved frozen cohort.
var RequiredAdminInvitationResendRefusalsScenarios = []string{"adm_inv_resend.admin_secondary_profile", "adm_inv_resend.non_admin", "adm_inv_resend.no_token"}

const (
	adminInvitationResendLegacyPath          = "/api/v1/admin/invitations/{id}/resend"
	adminInvitationResendAuthorizationHeader = "Authorization"
	adminInvitationResendPublicPrincipal     = "public"
	adminInvitationResendAdminPrincipal      = "acting_admin"
	adminInvitationResendMemberPrincipal     = "primary_profile"
)

func AdminInvitationResendRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{adminInvitationResendLegacyPath}, RequiredAdminInvitationResendRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: "/api/v1/admin/invitations/1/resend"}
				principal := Principal{Class: adminInvitationResendPublicPrincipal}
				status := http.StatusUnauthorized
				switch s.ID {
				case "adm_inv_resend.admin_secondary_profile":
					principal = Principal{Class: adminInvitationResendAdminPrincipal, Profile: inviteCodeDeleteSecondaryProfile}
					status = http.StatusForbidden
				case "adm_inv_resend.non_admin":
					principal = Principal{Class: adminInvitationResendMemberPrincipal}
					status = http.StatusForbidden
				}
				translated := original
				translated.Path = "/api/v2/admin/invitations/1/resend"
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, principal) || pair.Principal != nil ||
					pair.Method != http.MethodPost || pair.OperationID != "resendAdminInvitation" ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != status || pair.Expect.Status != status {
					return nil, fmt.Errorf("%s: unsupported administrator invitation resend refusal acceptance exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
