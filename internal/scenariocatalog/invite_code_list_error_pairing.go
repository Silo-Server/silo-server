package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
)

// RequiredInviteCodeListErrorScenarios is the complete reserved frozen cohort.
var RequiredInviteCodeListErrorScenarios = []string{"codes_list.error_shape"}

func InviteCodeListErrorAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{inviteCodeLegacyPath}, RequiredInviteCodeListErrorScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: inviteCodeLegacyPath}
				translated := Request{Path: inviteCodeInputV2Path}
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, Principal{Class: inviteCodeMemberPrincipal}) || pair.Principal != nil ||
					pair.Method != http.MethodGet || pair.OperationID != "listAdminInviteCodes" ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != http.StatusForbidden || pair.Expect.Status != http.StatusForbidden {
					return nil, fmt.Errorf("%s: unsupported invite-code list refusal envelope exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
