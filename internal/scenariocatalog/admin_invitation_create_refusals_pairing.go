package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
)

// RequiredAdminInvitationCreateRefusalsScenarios is the complete reserved frozen cohort.
var RequiredAdminInvitationCreateRefusalsScenarios = []string{"adm_inv_create.admin_secondary_profile", "adm_inv_create.non_admin", "adm_inv_create.no_token"}

const (
	adminInvitationCreateLegacyPath          = "/api/v1/admin/invitations/"
	adminInvitationCreateAuthorizationHeader = "Authorization"
	adminInvitationCreatePublicPrincipal     = "public"
	adminInvitationCreateAdminPrincipal      = "acting_admin"
	adminInvitationCreateMemberPrincipal     = "primary_profile"
)

func AdminInvitationCreateRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{adminInvitationCreateLegacyPath}, RequiredAdminInvitationCreateRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				var originalBody, translatedBody map[string]any
				expectedBody := map[string]any{"email": "fixture-guest@silo.example.test", "role": "user", "note": "welcome"}
				if json.Unmarshal(s.Request.Body, &originalBody) != nil || json.Unmarshal(pair.Request.Body, &translatedBody) != nil || !reflect.DeepEqual(originalBody, expectedBody) || !reflect.DeepEqual(translatedBody, expectedBody) {
					return nil, fmt.Errorf("%s: changed invitation creation refusal body", s.ID)
				}
				original := Request{Path: "/api/v1/admin/invitations/", Body: s.Request.Body}
				principal := Principal{Class: adminInvitationCreatePublicPrincipal}
				status := http.StatusUnauthorized
				switch s.ID {
				case "adm_inv_create.admin_secondary_profile":
					principal = Principal{Class: adminInvitationCreateAdminPrincipal, Profile: inviteCodeDeleteSecondaryProfile}
					status = http.StatusForbidden
				case "adm_inv_create.non_admin":
					principal = Principal{Class: adminInvitationCreateMemberPrincipal}
					status = http.StatusForbidden
				}
				translated := original
				translated.Body = pair.Request.Body
				translated.Path = "/api/v2/admin/invitations"
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, principal) || pair.Principal != nil ||
					pair.Method != http.MethodPost || pair.OperationID != "createAdminInvitation" ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != status || pair.Expect.Status != status {
					return nil, fmt.Errorf("%s: unsupported administrator invitation create refusal acceptance exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
