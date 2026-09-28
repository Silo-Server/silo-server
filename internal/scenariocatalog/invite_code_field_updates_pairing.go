package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
)

// RequiredInviteCodeFieldUpdatesScenarios is the complete reserved frozen cohort.
var RequiredInviteCodeFieldUpdatesScenarios = []string{"codes_update.ok", "codes_update.disable", "codes_update.shape"}

func InviteCodeFieldUpdatesAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPut, []string{inviteCodeUpdateInputLegacyRoute}, RequiredInviteCodeFieldUpdatesScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: inviteCodeLegacyPath + "${invite_code_id}", Body: s.Request.Body}
				var originalBody, translatedBody map[string]any
				expectedBody := map[string]any{inviteCodeInputLabel: "x"}
				switch s.ID {
				case "codes_update.ok":
					expectedBody = map[string]any{inviteCodeInputLabel: "renamed"}
				case "codes_update.disable":
					expectedBody = map[string]any{"enabled": false}
				}
				if json.Unmarshal(s.Request.Body, &originalBody) != nil || json.Unmarshal(pair.Request.Body, &translatedBody) != nil || !reflect.DeepEqual(originalBody, expectedBody) || !reflect.DeepEqual(translatedBody, expectedBody) {
					return nil, fmt.Errorf("%s: changed invite-code field update body", s.ID)
				}
				translated := Request{Path: inviteCodeInputV2Path + "/${invite_code_id}", Body: pair.Request.Body}
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, Principal{Class: inviteCodeAdminPrincipal}) || pair.Principal != nil ||
					pair.Method != http.MethodPut || pair.OperationID != inviteCodeMissingUpdateOperation ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != http.StatusNoContent || pair.Expect.Status != http.StatusNoContent {
					return nil, fmt.Errorf("%s: unsupported invite-code field update exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
