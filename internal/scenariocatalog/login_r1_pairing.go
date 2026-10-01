package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
)

var RequiredLoginR1Scenarios = []string{"login.ok.r1", "login.user_meaning.r1", "login.email_alias.r1", "login.grouped_download_policy.r1", "login.admin_permissions.r1", "login.wrong_password.r1", "login.unknown_user.r1", "login.disabled.r1", "login.unknown_provider.r1", "login.missing_fields.r1", "login.malformed_json.r1", "login.unknown_fields_ignored.r1", "login.rate_limited.r1"}

func LoginR1Acceptance(catalogs []*Catalog) ([]*Catalog, error) {
	want := map[string]bool{}
	for _, id := range RequiredLoginR1Scenarios {
		want[id] = false
	}
	var selected []*Catalog
	for _, c := range catalogs {
		copy := *c
		copy.Rows = nil
		for _, r := range c.Rows {
			if r.Listener != listenerAPI || r.Method != http.MethodPost || r.Path != loginCredentialsLegacyRoute || r.RegistrationIndex != 1 {
				continue
			}
			row := r
			row.Scenarios = nil
			for _, s := range r.Scenarios {
				seen, ok := want[s.ID]
				if !ok {
					continue
				}
				if seen {
					return nil, fmt.Errorf("duplicate required scenario %s", s.ID)
				}
				if err := ValidatePairing(s.V2Expectation); err != nil {
					return nil, fmt.Errorf("%s: %w", s.ID, err)
				}
				p := s.V2Expectation
				repeat := 0
				requires := []string{frozenDatabaseRequirement}
				switch s.ID {
				case "login.rate_limited.r1":
					repeat = 11
					requires = []string{"rate_limiter"}
				case "login.missing_fields.r1", "login.malformed_json.r1":
					requires = nil
				}
				if p.OperationID != lifecycleLoginOperation || p.Method != http.MethodPost || p.Principal != nil || !reflect.DeepEqual(s.Principal, Principal{Class: lifecyclePublicPrincipal}) || s.Request.Repeat != repeat || len(s.Then) != 0 || len(p.Then) != 0 || len(s.Settings) != 0 || !reflect.DeepEqual(s.Requires, requires) || !passwordSessionRequestSame(s.Request, p.Request) {
					return nil, fmt.Errorf("%s: changed rate-limited login exchange", s.ID)
				}
				want[s.ID] = true
				row.Scenarios = append(row.Scenarios, s)
			}
			copy.Rows = append(copy.Rows, row)
		}
		if len(copy.Rows) > 0 {
			selected = append(selected, &copy)
		}
	}
	for _, id := range RequiredLoginR1Scenarios {
		if !want[id] {
			return nil, fmt.Errorf("required scenario %s is missing", id)
		}
	}
	return selected, nil
}
