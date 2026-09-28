package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
)

// RequiredInviteCodeEmptyUpdateScenarios is the complete reserved frozen cohort.
var RequiredInviteCodeEmptyUpdateScenarios = []string{"codes_update.partial"}

func InviteCodeEmptyUpdateAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPut, []string{inviteCodeUpdateInputLegacyRoute}, RequiredInviteCodeEmptyUpdateScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: inviteCodeLegacyPath + "${invite_code_id}", Body: s.Request.Body}
				var originalBody, translatedBody map[string]any
				expectedBody := map[string]any{}
				if json.Unmarshal(s.Request.Body, &originalBody) != nil || json.Unmarshal(pair.Request.Body, &translatedBody) != nil || !reflect.DeepEqual(originalBody, expectedBody) || !reflect.DeepEqual(translatedBody, expectedBody) {
					return nil, fmt.Errorf("%s: changed empty invite-code update body", s.ID)
				}
				translated := Request{Path: inviteCodeInputV2Path + "/${invite_code_id}", Body: pair.Request.Body}
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, Principal{Class: inviteCodeAdminPrincipal}) || pair.Principal != nil ||
					pair.Method != http.MethodPut || pair.OperationID != inviteCodeMissingUpdateOperation ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != http.StatusNoContent || pair.Expect.Status != http.StatusNoContent {
					return nil, fmt.Errorf("%s: unsupported empty invite-code update exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
