package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
)

const inviteCodeListReadsOperation = "listAdminInviteCodes"

// RequiredInviteCodeListReadsScenarios is the complete reserved frozen cohort.
var RequiredInviteCodeListReadsScenarios = []string{"codes_list.ok", "codes_list.meaning", "codes_list.shape", "codes_list.sorted"}

func InviteCodeListReadsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{inviteCodeLegacyPath}, RequiredInviteCodeListReadsScenarios)
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
					!reflect.DeepEqual(s.Principal, Principal{Class: inviteCodeAdminPrincipal}) || pair.Principal != nil ||
					pair.Method != http.MethodGet || pair.OperationID != inviteCodeListReadsOperation ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != http.StatusOK || pair.Expect.Status != http.StatusOK {
					return nil, fmt.Errorf("%s: unsupported invite-code list read exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
