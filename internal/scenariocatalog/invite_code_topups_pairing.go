package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
)

// RequiredInviteCodeTopUpsScenarios is the complete reserved frozen cohort.
var RequiredInviteCodeTopUpsScenarios = []string{"codes_topup.ok", "codes_topup.meaning", "codes_topup.shape"}

func InviteCodeTopUpsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{inviteCodeMissingTopUpRoute}, RequiredInviteCodeTopUpsScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: inviteCodeLegacyPath + "${invite_code_id}/top-up", Body: s.Request.Body}
				var originalBody, translatedBody map[string]any
				amount := float64(2)
				if s.ID == "codes_topup.shape" {
					amount = 1
				}
				expectedBody := map[string]any{inviteCodeMissingAdditionalUses: amount}
				if json.Unmarshal(s.Request.Body, &originalBody) != nil || json.Unmarshal(pair.Request.Body, &translatedBody) != nil || !reflect.DeepEqual(originalBody, expectedBody) || !reflect.DeepEqual(translatedBody, expectedBody) {
					return nil, fmt.Errorf("%s: changed invite-code top-up body", s.ID)
				}
				translated := Request{Path: inviteCodeInputV2Path + "/${invite_code_id}/top-up", Body: pair.Request.Body}
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, Principal{Class: inviteCodeAdminPrincipal}) || pair.Principal != nil ||
					pair.Method != http.MethodPost || pair.OperationID != inviteCodeMissingTopUpOperation ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != http.StatusOK || pair.Expect.Status != http.StatusOK {
					return nil, fmt.Errorf("%s: unsupported invite-code top-up exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
