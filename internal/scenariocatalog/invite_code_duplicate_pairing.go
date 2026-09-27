package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
)

const (
	inviteCodeDuplicateMaxUses   = "max_uses"
	inviteCodeDuplicateOperation = "createAdminInviteCode"
)

// RequiredInviteCodeDuplicateScenarios is the complete reserved frozen cohort.
var RequiredInviteCodeDuplicateScenarios = []string{"codes_create.duplicate_code"}

func InviteCodeDuplicateAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{inviteCodeLegacyPath}, RequiredInviteCodeDuplicateScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: inviteCodeLegacyPath, Body: s.Request.Body}
				var originalBody, translatedBody map[string]any
				expectedBody := map[string]any{"code": "${invite_code}", inviteCodeInputLabel: "dup", inviteCodeDuplicateMaxUses: float64(1)}
				if json.Unmarshal(s.Request.Body, &originalBody) != nil || json.Unmarshal(pair.Request.Body, &translatedBody) != nil || !reflect.DeepEqual(originalBody, expectedBody) || !reflect.DeepEqual(translatedBody, expectedBody) {
					return nil, fmt.Errorf("%s: changed duplicate invite-code body", s.ID)
				}
				translated := Request{Path: inviteCodeInputV2Path, Body: pair.Request.Body}
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, Principal{Class: inviteCodeAdminPrincipal}) || pair.Principal != nil ||
					pair.Method != http.MethodPost || pair.OperationID != inviteCodeDuplicateOperation ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != http.StatusInternalServerError || pair.Expect.Status != http.StatusConflict {
					return nil, fmt.Errorf("%s: unsupported duplicate invite-code exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
