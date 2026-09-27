package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
)

const invitationTokenWeakPasswordCase = "inv_accept.weak_password"

var RequiredInvitationTokenLifecycleScenarios = []string{
	"inv_lookup.ok", "inv_lookup.meaning", "inv_lookup.shape", "inv_lookup.accepted", "inv_lookup.expired", "inv_lookup.unknown", "inv_lookup.trailing_slash",
	"inv_accept.ok", "inv_accept.meaning", "inv_accept.single_use", invitationTokenWeakPasswordCase, "inv_accept.malformed", "inv_accept.unknown", "inv_accept.shape",
}

const invitationTokenLegacyPrefix = "/api/v1/invitations/"
const invitationTokenPassword = "fixture-invitee-password"
const invitationTokenPasswordField = "password"

func InvitationTokenLifecycleAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	var selected []*Catalog
	for i, g := range []struct {
		method, path, op string
		lo, hi           int
	}{
		{http.MethodGet, "/api/v1/invitations/{token}/", "lookupInvitation", 0, 7},
		{http.MethodPost, "/api/v1/invitations/{token}/accept", "acceptInvitation", 7, 14},
	} {
		part, err := requiredAcceptance(catalogs, g.method, []string{g.path}, RequiredInvitationTokenLifecycleScenarios[g.lo:g.hi])
		if err != nil {
			return nil, err
		}
		for _, c := range part {
			for _, r := range c.Rows {
				for _, s := range r.Scenarios {
					pair := s.V2Expectation
					token := "${invitation_token}"
					if s.ID == "inv_lookup.accepted" {
						token = "${invitation_token_accepted}"
					}
					if s.ID == "inv_lookup.expired" {
						token = "fixture-invitation-token-expired"
					}
					if strings.HasSuffix(s.ID, ".unknown") || s.ID == invitationTokenWeakPasswordCase {
						token = "no-such-token"
					}
					want := Request{Path: invitationTokenLegacyPrefix + token}
					status := http.StatusOK
					fresh := false
					if i == 0 {
						if s.ID != "inv_lookup.trailing_slash" {
							want.Path += "/"
						}
						if token != "${invitation_token}" {
							status = http.StatusNotFound
						}
					} else {
						want.Path += "/accept"
						status = http.StatusCreated
						fresh = true
						if s.ID == "inv_accept.malformed" {
							want.RawBody = new("nope")
							status = http.StatusBadRequest
							fresh = false
						} else {
							password := invitationTokenPassword
							if s.ID == invitationTokenWeakPasswordCase {
								password = "short"
								status = http.StatusBadRequest
								fresh = false
							}
							var a, b map[string]any
							expected := map[string]any{invitationTokenPasswordField: password}
							if json.Unmarshal(s.Request.Body, &a) != nil || json.Unmarshal(pair.Request.Body, &b) != nil || !reflect.DeepEqual(a, expected) || !reflect.DeepEqual(b, expected) {
								return nil, fmt.Errorf("%s: changed invitation token body", s.ID)
							}
							want.Body = s.Request.Body
							if s.ID == "inv_accept.unknown" {
								status = http.StatusNotFound
								fresh = false
							}
							if s.ID == "inv_accept.single_use" {
								status = http.StatusNotFound
								want.Repeat = 2
							}
						}
					}
					v2status := status
					if s.ID == invitationTokenWeakPasswordCase {
						v2status = http.StatusUnprocessableEntity
					}
					translated := want
					translated.Path = strings.TrimSuffix(strings.Replace(want.Path, "/api/v1/", "/api/v2/", 1), "/")
					if want.Body != nil {
						translated.Body = pair.Request.Body
					}
					if !reflect.DeepEqual(s.Request, want) || !reflect.DeepEqual(pair.Request, translated) || !reflect.DeepEqual(s.Principal, Principal{Class: adminInvitationCreatePublicPrincipal}) || pair.Principal != nil || !reflect.DeepEqual(s.Requires, []string{frozenDatabaseRequirement}) || len(s.Settings) != 0 || s.FreshState != fresh || len(s.Then) != 0 || len(pair.Then) != 0 || pair.Method != g.method || pair.OperationID != g.op || s.Expect.Status != status || pair.Expect.Status != v2status {
						return nil, fmt.Errorf("%s: changed invitation token exchange", s.ID)
					}
				}
			}
		}
		selected = append(selected, part...)
	}
	return selected, nil
}
