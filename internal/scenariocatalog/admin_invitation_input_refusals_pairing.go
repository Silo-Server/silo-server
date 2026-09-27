package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
)

const adminInvitationInputV2Path = "/api/v2/admin/invitations"

// RequiredAdminInvitationInputRefusalsScenarios is the complete reserved frozen cohort.
var RequiredAdminInvitationInputRefusalsScenarios = []string{"adm_inv_create.invalid_email", "adm_inv_create.malformed"}

func AdminInvitationInputRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{adminInvitationCreateLegacyPath}, RequiredAdminInvitationInputRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: adminInvitationCreateLegacyPath}
				status := http.StatusBadRequest
				if s.ID == "adm_inv_create.invalid_email" {
					var a, b map[string]any
					expected := map[string]any{"email": "not-an-address"}
					if json.Unmarshal(s.Request.Body, &a) != nil || json.Unmarshal(pair.Request.Body, &b) != nil || !reflect.DeepEqual(a, expected) || !reflect.DeepEqual(b, expected) {
						return nil, fmt.Errorf("%s: changed invitation input refusal body", s.ID)
					}
					original.Body = s.Request.Body
					status = http.StatusUnprocessableEntity
				} else {
					original.RawBody = new("x")
				}
				translated := original
				translated.Path = adminInvitationInputV2Path
				if original.RawBody == nil {
					translated.Body = pair.Request.Body
				}
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, Principal{Class: adminInvitationCreateAdminPrincipal}) || pair.Principal != nil ||
					pair.Method != http.MethodPost || pair.OperationID != "createAdminInvitation" ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != http.StatusBadRequest || pair.Expect.Status != status {
					return nil, fmt.Errorf("%s: unsupported invitation input refusal exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
