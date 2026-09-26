package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
)

const adminInvitationEmailConflictResendOperation = "resendAdminInvitation"

// RequiredAdminInvitationEmailConflictsScenarios is the complete reserved frozen cohort.
var RequiredAdminInvitationEmailConflictsScenarios = []string{"adm_inv_create.email_taken", "adm_inv_resend.accepted"}

func AdminInvitationEmailConflictsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	var selected []*Catalog
	for i, route := range []string{adminInvitationCreateLegacyPath, adminInvitationResendLegacyPath} {
		part, err := requiredAcceptance(catalogs, http.MethodPost, []string{route}, RequiredAdminInvitationEmailConflictsScenarios[i:i+1])
		if err != nil {
			return nil, err
		}
		selected = append(selected, part...)
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: adminInvitationCreateLegacyPath, Body: s.Request.Body}
				translated := Request{Path: adminInvitationInputV2Path, Body: pair.Request.Body}
				operation := adminInvitationRoleOperation
				if s.ID == RequiredAdminInvitationEmailConflictsScenarios[0] {
					var a, b map[string]any
					expected := map[string]any{adminInvitationRoleEmail: "${member_email}"}
					if json.Unmarshal(s.Request.Body, &a) != nil || json.Unmarshal(pair.Request.Body, &b) != nil || !reflect.DeepEqual(a, expected) || !reflect.DeepEqual(b, expected) {
						return nil, fmt.Errorf("%s: changed invitation email conflict body", s.ID)
					}
				} else {
					original = Request{Path: adminInvitationCreateLegacyPath + "${invitation_accepted_id}/resend"}
					translated = Request{Path: adminInvitationInputV2Path + "/${invitation_accepted_id}/resend"}
					operation = adminInvitationEmailConflictResendOperation
				}
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, Principal{Class: adminInvitationCreateAdminPrincipal}) || pair.Principal != nil ||
					pair.Method != http.MethodPost || pair.OperationID != operation ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != http.StatusConflict || pair.Expect.Status != http.StatusConflict {
					return nil, fmt.Errorf("%s: unsupported invitation email conflict exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
