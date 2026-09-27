package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
)

const (
	inviteCodeMissingTopUpRoute      = "/api/v1/admin/invite-codes/{id}/top-up"
	inviteCodeMissingUpdateOperation = "updateAdminInviteCode"
	inviteCodeMissingTopUpOperation  = "topUpAdminInviteCode"
	inviteCodeMissingAdditionalUses  = "additional_uses"
)

// RequiredInviteCodeMissingScenarios is the complete reserved frozen cohort.
var RequiredInviteCodeMissingScenarios = []string{"codes_update.not_found", "codes_topup.not_found"}

func InviteCodeMissingAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPut, []string{inviteCodeUpdateInputLegacyRoute}, RequiredInviteCodeMissingScenarios[:1])
	if err != nil {
		return nil, err
	}
	topup, err := requiredAcceptance(catalogs, http.MethodPost, []string{inviteCodeMissingTopUpRoute}, RequiredInviteCodeMissingScenarios[1:])
	if err != nil {
		return nil, err
	}
	selected = append(selected, topup...)
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: inviteCodeLegacyPath + "999999", Body: s.Request.Body}
				method, operation := http.MethodPut, inviteCodeMissingUpdateOperation
				expectedBody := map[string]any{inviteCodeInputLabel: "x"}
				if s.ID == RequiredInviteCodeMissingScenarios[1] {
					original.Path += "/top-up"
					method, operation = http.MethodPost, inviteCodeMissingTopUpOperation
					expectedBody = map[string]any{inviteCodeMissingAdditionalUses: float64(1)}
				}
				var originalBody, translatedBody map[string]any
				if json.Unmarshal(s.Request.Body, &originalBody) != nil || json.Unmarshal(pair.Request.Body, &translatedBody) != nil || !reflect.DeepEqual(originalBody, expectedBody) || !reflect.DeepEqual(translatedBody, expectedBody) {
					return nil, fmt.Errorf("%s: changed invite-code missing-record body", s.ID)
				}
				translated := original
				translated.Path = inviteCodeInputV2Path + "/" + original.Path[len(inviteCodeLegacyPath):]
				translated.Body = pair.Request.Body
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, Principal{Class: inviteCodeAdminPrincipal}) || pair.Principal != nil ||
					pair.Method != method || pair.OperationID != operation ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != http.StatusNotFound || pair.Expect.Status != http.StatusNotFound {
					return nil, fmt.Errorf("%s: unsupported invite-code missing-record exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
