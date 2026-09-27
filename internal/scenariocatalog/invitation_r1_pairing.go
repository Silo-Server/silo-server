package scenariocatalog

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
)

var RequiredInvitationR1Scenarios = []string{"inv_lookup.ok.r1", "inv_lookup.meaning.r1", "inv_lookup.shape.r1", "inv_lookup.accepted.r1", "inv_lookup.expired.r1", "inv_lookup.unknown.r1", "inv_lookup.trailing_slash.r1", "inv_lookup.rate_limited.r1", "inv_accept.ok.r1", "inv_accept.meaning.r1", "inv_accept.single_use.r1", "inv_accept.weak_password.r1", "inv_accept.malformed.r1", "inv_accept.unknown.r1", "inv_accept.shape.r1", "inv_accept.rate_limited.r1"}

func InvitationR1Acceptance(catalogs []*Catalog) ([]*Catalog, error) {
	want := map[string]bool{}
	for _, id := range RequiredInvitationR1Scenarios {
		want[id] = false
	}
	var selected []*Catalog
	for _, c := range catalogs {
		copy := *c
		copy.Rows = nil
		for _, r := range c.Rows {
			if r.Listener != listenerAPI || r.RegistrationIndex != 1 || ((r.Method != http.MethodGet || r.Path != "/api/v1/invitations/{token}/") && (r.Method != http.MethodPost || r.Path != "/api/v1/invitations/{token}/accept")) {
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
				op := "acceptInvitation"
				method := http.MethodPost
				original := s.Request
				if strings.HasPrefix(s.ID, "inv_lookup.") {
					op = "lookupInvitation"
					method = http.MethodGet
					original.Path = strings.TrimSuffix(original.Path, "/")
				}
				if strings.Contains(s.ID, ".rate_limited.") {
					repeat = 11
					requires = []string{"rate_limiter"}
				} else if s.ID == "inv_accept.single_use.r1" {
					repeat = 2
				}
				if p.OperationID != op || p.Method != method || r.Method != method || p.Principal != nil || !reflect.DeepEqual(s.Principal, Principal{Class: lifecyclePublicPrincipal}) || s.Request.Repeat != repeat || len(s.Then) != 0 || len(p.Then) != 0 || len(s.Settings) != 0 || !reflect.DeepEqual(s.Requires, requires) || !passwordSessionRequestSame(original, p.Request) {
					return nil, fmt.Errorf("%s: changed invitation r1 exchange", s.ID)
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
	for _, id := range RequiredInvitationR1Scenarios {
		if !want[id] {
			return nil, fmt.Errorf("required scenario %s is missing", id)
		}
	}
	return selected, nil
}
