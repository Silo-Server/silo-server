package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
)

// RequiredInviteCodeDeleteInputScenarios is the complete reserved frozen cohort.
var RequiredInviteCodeDeleteInputScenarios = []string{"codes_delete.bad_id"}

func InviteCodeDeleteInputAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodDelete, []string{inviteCodeUpdateInputLegacyRoute}, RequiredInviteCodeDeleteInputScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: inviteCodeLegacyPath + "abc"}
				translated := Request{Path: inviteCodeInputV2Path + "/abc"}
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, Principal{Class: inviteCodeAdminPrincipal}) || pair.Principal != nil ||
					pair.Method != http.MethodDelete || pair.OperationID != "deleteAdminInviteCode" ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != http.StatusBadRequest || pair.Expect.Status != http.StatusUnprocessableEntity {
					return nil, fmt.Errorf("%s: unsupported invite-code deletion input exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
