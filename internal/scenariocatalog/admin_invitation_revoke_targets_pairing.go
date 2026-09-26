package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
)

// RequiredAdminInvitationRevokeTargetsScenarios is the complete reserved frozen cohort.
var RequiredAdminInvitationRevokeTargetsScenarios = []string{"adm_inv_revoke.bad_id", "adm_inv_revoke.not_found"}

func AdminInvitationRevokeTargetsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodDelete, []string{adminInvitationRevokeLegacyPath}, RequiredAdminInvitationRevokeTargetsScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				id := "999999"
				originalStatus, status := http.StatusNotFound, http.StatusNotFound
				if s.ID == "adm_inv_revoke.bad_id" {
					id = "abc"
					originalStatus, status = http.StatusBadRequest, http.StatusUnprocessableEntity
				}
				original := Request{Path: adminInvitationCreateLegacyPath + id}
				translated := Request{Path: adminInvitationInputV2Path + "/" + id}
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, Principal{Class: adminInvitationRevokeAdminPrincipal}) || pair.Principal != nil ||
					pair.Method != http.MethodDelete || pair.OperationID != "revokeAdminInvitation" ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != originalStatus || pair.Expect.Status != status {
					return nil, fmt.Errorf("%s: unsupported invitation revoke target refusal", s.ID)
				}
			}
		}
	}
	return selected, nil
}
