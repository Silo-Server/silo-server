package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
)

// RequiredInviteCodeTopUpRefusalsScenarios is the complete reserved frozen cohort.
var RequiredInviteCodeTopUpRefusalsScenarios = []string{"codes_topup.admin_secondary_profile", "codes_topup.non_admin", "codes_topup.no_token"}

func InviteCodeTopUpRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/admin/invite-codes/{id}/top-up"}, RequiredInviteCodeTopUpRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				var originalBody, translatedBody map[string]any
				expectedBody := map[string]any{"additional_uses": float64(1)}
				if json.Unmarshal(s.Request.Body, &originalBody) != nil || json.Unmarshal(pair.Request.Body, &translatedBody) != nil || !reflect.DeepEqual(originalBody, expectedBody) || !reflect.DeepEqual(translatedBody, expectedBody) {
					return nil, fmt.Errorf("%s: changed invite-code top-up refusal body", s.ID)
				}
				original := Request{Path: inviteCodeLegacyPath + "${invite_code_id}/top-up", Body: s.Request.Body}
				principal := Principal{Class: inviteCodePublicPrincipal}
				status := http.StatusUnauthorized
				switch s.ID {
				case "codes_topup.no_token":
					original.Path = inviteCodeLegacyPath + "1/top-up"
				case "codes_topup.admin_secondary_profile":
					principal = Principal{Class: inviteCodeAdminPrincipal, Profile: inviteCodeDeleteSecondaryProfile}
					status = http.StatusForbidden
				case "codes_topup.non_admin":
					principal = Principal{Class: inviteCodeMemberPrincipal}
					status = http.StatusForbidden
				}
				translated := original
				translated.Body = pair.Request.Body
				translated.Path = "/api/v2/admin/invite-codes/" + original.Path[len(inviteCodeLegacyPath):]
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, principal) || pair.Principal != nil ||
					pair.Method != http.MethodPost || pair.OperationID != "topUpAdminInviteCode" ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != status || pair.Expect.Status != status {
					return nil, fmt.Errorf("%s: unsupported administrator invite-code refusal acceptance exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
