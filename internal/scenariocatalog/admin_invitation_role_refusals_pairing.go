package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
)

const (
	adminInvitationRoleEmail     = "email"
	adminInvitationRoleOperation = "createAdminInvitation"
)

// RequiredAdminInvitationRoleRefusalsScenarios is the complete reserved frozen cohort.
var RequiredAdminInvitationRoleRefusalsScenarios = []string{"adm_inv_create.bad_role", "adm_inv_create.admin_grouped"}

func AdminInvitationRoleRefusalsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{adminInvitationCreateLegacyPath}, RequiredAdminInvitationRoleRefusalsScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				expected := map[string]any{adminInvitationRoleEmail: "fixture-guest@silo.example.test", "role": "owner"}
				originalStatus := http.StatusForbidden
				if s.ID == "adm_inv_create.admin_grouped" {
					expected["role"] = "admin"
					expected["access_group_id"] = float64(1)
					originalStatus = http.StatusUnprocessableEntity
				}
				var a, b map[string]any
				if json.Unmarshal(s.Request.Body, &a) != nil || json.Unmarshal(pair.Request.Body, &b) != nil || !reflect.DeepEqual(a, expected) || !reflect.DeepEqual(b, expected) {
					return nil, fmt.Errorf("%s: changed invitation role refusal body", s.ID)
				}
				original := Request{Path: adminInvitationCreateLegacyPath, Body: s.Request.Body}
				translated := Request{Path: adminInvitationInputV2Path, Body: pair.Request.Body}
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, Principal{Class: adminInvitationCreateAdminPrincipal}) || pair.Principal != nil ||
					pair.Method != http.MethodPost || pair.OperationID != adminInvitationRoleOperation ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != originalStatus || pair.Expect.Status != http.StatusUnprocessableEntity {
					return nil, fmt.Errorf("%s: unsupported invitation role refusal", s.ID)
				}
			}
		}
	}
	return selected, nil
}
