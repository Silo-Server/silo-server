package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
)

const (
	inviteCodeZeroUsesScenario = "codes_create.zero_uses"
	inviteCodeInputLabel       = "label"
	inviteCodeInputV2Path      = "/api/v2/admin/invite-codes"
)

// RequiredInviteCodeCreateInputScenarios is the complete reserved frozen cohort.
var RequiredInviteCodeCreateInputScenarios = []string{inviteCodeZeroUsesScenario, "codes_create.malformed"}

func InviteCodeCreateInputAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{inviteCodeLegacyPath}, RequiredInviteCodeCreateInputScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: inviteCodeLegacyPath}
				status := http.StatusBadRequest
				if s.ID == inviteCodeZeroUsesScenario {
					var originalBody, translatedBody map[string]any
					expectedBody := map[string]any{inviteCodeInputLabel: "x", "max_uses": float64(0)}
					if json.Unmarshal(s.Request.Body, &originalBody) != nil || json.Unmarshal(pair.Request.Body, &translatedBody) != nil || !reflect.DeepEqual(originalBody, expectedBody) || !reflect.DeepEqual(translatedBody, expectedBody) {
						return nil, fmt.Errorf("%s: changed invite-code creation input body", s.ID)
					}
					original.Body = s.Request.Body
					status = http.StatusUnprocessableEntity
				} else {
					original.RawBody = new("x")
				}
				translated := original
				translated.Path = inviteCodeInputV2Path
				if s.ID == inviteCodeZeroUsesScenario {
					translated.Body = pair.Request.Body
				}
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, Principal{Class: inviteCodeAdminPrincipal}) || pair.Principal != nil ||
					pair.Method != http.MethodPost || pair.OperationID != "createAdminInviteCode" ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != http.StatusBadRequest || pair.Expect.Status != status {
					return nil, fmt.Errorf("%s: unsupported invite-code creation input exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
