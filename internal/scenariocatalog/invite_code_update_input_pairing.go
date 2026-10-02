package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
)

const (
	inviteCodeBadIDScenario = "codes_update.bad_id"
)

const inviteCodeUpdateInputLegacyRoute = "/api/v1/admin/invite-codes/{id}"

// RequiredInviteCodeUpdateInputScenarios is the complete reserved frozen cohort.
var RequiredInviteCodeUpdateInputScenarios = []string{inviteCodeBadIDScenario, "codes_update.malformed"}

func InviteCodeUpdateInputAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPut, []string{inviteCodeUpdateInputLegacyRoute}, RequiredInviteCodeUpdateInputScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: inviteCodeLegacyPath + "${invite_code_id}"}
				status := http.StatusBadRequest
				if s.ID == inviteCodeBadIDScenario {
					var originalBody, translatedBody map[string]any
					expectedBody := map[string]any{inviteCodeInputLabel: "x"}
					if json.Unmarshal(s.Request.Body, &originalBody) != nil || json.Unmarshal(pair.Request.Body, &translatedBody) != nil || !reflect.DeepEqual(originalBody, expectedBody) || !reflect.DeepEqual(translatedBody, expectedBody) {
						return nil, fmt.Errorf("%s: changed invite-code update input body", s.ID)
					}
					original.Body = s.Request.Body
					original.Path = inviteCodeLegacyPath + "abc"
					status = http.StatusUnprocessableEntity
				} else {
					original.RawBody = new("x")
				}
				translated := original
				translated.Path = inviteCodeInputV2Path + "/" + original.Path[len(inviteCodeLegacyPath):]
				if s.ID == inviteCodeBadIDScenario {
					translated.Body = pair.Request.Body
				}
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, Principal{Class: inviteCodeAdminPrincipal}) || pair.Principal != nil ||
					pair.Method != http.MethodPut || pair.OperationID != "updateAdminInviteCode" ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != http.StatusBadRequest || pair.Expect.Status != status {
					return nil, fmt.Errorf("%s: unsupported invite-code update input exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
