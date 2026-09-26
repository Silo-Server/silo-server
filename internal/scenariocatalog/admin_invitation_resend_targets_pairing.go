package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
)

// RequiredAdminInvitationResendTargetsScenarios is the complete reserved frozen cohort.
var RequiredAdminInvitationResendTargetsScenarios = []string{"adm_inv_resend.bad_id", "adm_inv_resend.not_found"}

func AdminInvitationResendTargetsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodPost, []string{adminInvitationResendLegacyPath}, RequiredAdminInvitationResendTargetsScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				id := "999999"
				originalStatus, status := http.StatusNotFound, http.StatusNotFound
				if s.ID == "adm_inv_resend.bad_id" {
					id = "abc"
					originalStatus, status = http.StatusBadRequest, http.StatusUnprocessableEntity
				}
				original := Request{Path: adminInvitationCreateLegacyPath + id + "/resend"}
				translated := Request{Path: adminInvitationInputV2Path + "/" + id + "/resend"}
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, Principal{Class: adminInvitationResendAdminPrincipal}) || pair.Principal != nil ||
					pair.Method != http.MethodPost || pair.OperationID != "resendAdminInvitation" ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != originalStatus || pair.Expect.Status != status {
					return nil, fmt.Errorf("%s: unsupported invitation resend target refusal", s.ID)
				}
			}
		}
	}
	return selected, nil
}
