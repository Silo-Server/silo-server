package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
)

// RequiredInviteCodeTopUpInputScenarios is the complete reserved frozen cohort.
var RequiredInviteCodeTopUpInputScenarios = []string{"codes_topup.zero", "codes_topup.bad_id"}

func InviteCodeTopUpInputAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{"/api/v1/admin/invite-codes/{id}/top-up"}, RequiredInviteCodeTopUpInputScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: inviteCodeLegacyPath + "${invite_code_id}/top-up", Body: s.Request.Body}
				uses := float64(0)
				if s.ID == "codes_topup.bad_id" {
					original.Path = inviteCodeLegacyPath + "abc/top-up"
					uses = 1
				}
				var originalBody, translatedBody map[string]any
				expectedBody := map[string]any{"additional_uses": uses}
				if json.Unmarshal(s.Request.Body, &originalBody) != nil || json.Unmarshal(pair.Request.Body, &translatedBody) != nil || !reflect.DeepEqual(originalBody, expectedBody) || !reflect.DeepEqual(translatedBody, expectedBody) {
					return nil, fmt.Errorf("%s: changed invite-code top-up input body", s.ID)
				}
				translated := original
				translated.Path = inviteCodeInputV2Path + "/" + original.Path[len(inviteCodeLegacyPath):]
				translated.Body = pair.Request.Body
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, Principal{Class: inviteCodeAdminPrincipal}) || pair.Principal != nil ||
					pair.Method != http.MethodPost || pair.OperationID != "topUpAdminInviteCode" ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != http.StatusBadRequest || pair.Expect.Status != http.StatusUnprocessableEntity {
					return nil, fmt.Errorf("%s: unsupported invite-code top-up input exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
