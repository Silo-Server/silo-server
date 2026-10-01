package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
)

// RequiredAccountMeRefusalScenarios is the complete reserved frozen cohort.
var RequiredAccountMeRefusalScenarios = []string{"me.no_token", "me.bad_token"}

const (
	accountMeLegacyPath          = "/api/v1/auth/me"
	accountMeAuthorizationHeader = "Authorization"
	accountMePublicPrincipal     = "public"
)

func AccountMeRefusalAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	selected, err := requiredAcceptance(catalogs, http.MethodGet, []string{accountMeLegacyPath}, RequiredAccountMeRefusalScenarios)
	if err != nil {
		return nil, err
	}
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				pair := s.V2Expectation
				original := Request{Path: accountMeLegacyPath}
				if s.ID == "me.bad_token" {
					original.Headers = map[string]*string{accountMeAuthorizationHeader: new("Bearer not-a-jwt")}
				}
				translated := original
				translated.Path = "/api/v2/account/me"
				if !reflect.DeepEqual(s.Request, original) || !reflect.DeepEqual(pair.Request, translated) ||
					!reflect.DeepEqual(s.Principal, Principal{Class: accountMePublicPrincipal}) || pair.Principal != nil ||
					pair.Method != http.MethodGet || pair.OperationID != "getCurrentUser" ||
					len(s.Then) != 0 || len(pair.Then) != 0 || len(s.Requires) != 0 || len(s.Settings) != 0 ||
					s.Expect.Status != http.StatusUnauthorized || pair.Expect.Status != http.StatusUnauthorized {
					return nil, fmt.Errorf("%s: unsupported account refusal acceptance exchange", s.ID)
				}
			}
		}
	}
	return selected, nil
}
