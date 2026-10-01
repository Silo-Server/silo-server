package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
)

const inviteCodeDeletionsOperation = "deleteAdminInviteCode"

// RequiredInviteCodeDeletionsScenarios is the complete reserved frozen cohort.
var RequiredInviteCodeDeletionsScenarios = []string{"codes_delete.ok", "codes_delete.gone", "codes_delete.shape"}

func InviteCodeDeletionsAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodDelete, []string{inviteCodeUpdateInputLegacyRoute}, RequiredInviteCodeDeletionsScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: inviteCodeLegacyPath + "${invite_code_id}"}
				status := http.StatusNoContent
				switch s.ID {
				case "codes_delete.gone":
					original.Repeat = 2
					status = http.StatusNotFound
				case "codes_delete.shape":
					original.Path = inviteCodeLegacyPath + "${invite_code_disabled_id}"
				}
				translated := original
				translated.Path = inviteCodeInputV2Path + "/" + original.Path[len(inviteCodeLegacyPath):]
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, Principal{Class: inviteCodeAdminPrincipal}) || pair.Principal != nil ||
					pair.Method != http.MethodDelete || pair.OperationID != inviteCodeDeletionsOperation ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != status || pair.Expect.Status != status {
					return nil, fmt.Errorf("%s: unsupported invite-code deletion exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
