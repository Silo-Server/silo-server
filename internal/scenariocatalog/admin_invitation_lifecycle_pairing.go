package scenariocatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
)

const adminInvitationLifecycleRevokeOperation = "revokeAdminInvitation"
const adminInvitationLifecycleGuestEmail = "fixture-guest@silo.example.test"

const (
	adminInvitationLifecycleRoleField    = "role"
	adminInvitationLifecycleUserRole     = "user"
	adminInvitationLifecycleNoteField    = "note"
	adminInvitationLifecycleWelcomeNote  = "welcome"
	adminInvitationLifecycleInviteeEmail = "fixture-invitee@silo.example.test"
)

var RequiredAdminInvitationLifecycleScenarios = []string{
	"adm_inv_list.ok", "adm_inv_list.meaning", "adm_inv_list.shape", "adm_inv_list.sorted", "adm_inv_list.admin_no_profile", "adm_inv_list.error_shape",
	"adm_inv_create.ok", "adm_inv_create.meaning", "adm_inv_create.public_url_fallback", "adm_inv_create.supersedes", "adm_inv_create.shape",
	"adm_inv_resend.ok", "adm_inv_resend.meaning", "adm_inv_resend.shape",
	"adm_inv_revoke.ok", "adm_inv_revoke.idempotent", "adm_inv_revoke.accepted", "adm_inv_revoke.shape",
}

func AdminInvitationLifecycleAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	var selected []*Catalog
	for i, group := range []struct {
		method, route, operation string
		start, end               int
	}{
		{http.MethodGet, adminInvitationCreateLegacyPath, "listAdminInvitations", 0, 6},
		{http.MethodPost, adminInvitationCreateLegacyPath, adminInvitationRoleOperation, 6, 11},
		{http.MethodPost, adminInvitationResendLegacyPath, adminInvitationEmailConflictResendOperation, 11, 14},
		{http.MethodDelete, adminInvitationRevokeLegacyPath, adminInvitationLifecycleRevokeOperation, 14, 18},
	} {
		part, err := requiredAcceptance(catalogs, group.method, []string{group.route}, RequiredAdminInvitationLifecycleScenarios[group.start:group.end])
		if err != nil {
			return nil, err
		}
		for _, c := range part {
			for _, r := range c.Rows {
				for _, s := range r.Scenarios {
					pair := s.V2Expectation
					want := Request{Path: adminInvitationCreateLegacyPath}
					principal := Principal{Class: adminInvitationCreateAdminPrincipal}
					status, v2status := http.StatusOK, http.StatusOK
					var settings map[string]string
					fresh := i > 0
					switch i {
					case 0:
						if s.ID == "adm_inv_list.admin_no_profile" {
							principal.Class = "admin"
						}
						if s.ID == "adm_inv_list.error_shape" {
							principal.Class = adminInvitationResendMemberPrincipal
							status = http.StatusForbidden
							v2status = status
						}
					case 1:
						status = http.StatusCreated
						v2status = status
						body := map[string]any{adminInvitationRoleEmail: adminInvitationLifecycleGuestEmail, adminInvitationLifecycleRoleField: adminInvitationLifecycleUserRole, adminInvitationLifecycleNoteField: adminInvitationLifecycleWelcomeNote}
						if s.ID == "adm_inv_create.supersedes" {
							body = map[string]any{adminInvitationRoleEmail: adminInvitationLifecycleInviteeEmail}
						}
						var a, b map[string]any
						if json.Unmarshal(s.Request.Body, &a) != nil || json.Unmarshal(pair.Request.Body, &b) != nil || !reflect.DeepEqual(a, body) || !reflect.DeepEqual(b, body) {
							return nil, fmt.Errorf("%s: changed lifecycle body", s.ID)
						}
						want.Body = s.Request.Body
						if s.ID == "adm_inv_create.ok" || s.ID == "adm_inv_create.meaning" {
							settings = map[string]string{"server.public_url": "${public_url}"}
						}
					case 2:
						want.Path += "${invitation_pending_id}/resend"
						v2status = http.StatusCreated
					case 3:
						want.Path += "${invitation_pending_id}"
						status = http.StatusNoContent
						v2status = status
						if s.ID == "adm_inv_revoke.accepted" {
							want.Path = adminInvitationCreateLegacyPath + "${invitation_accepted_id}"
							fresh = false
						}
						if s.ID == "adm_inv_revoke.idempotent" {
							want.Repeat = 2
						}
					}
					translated := want
					translated.Path = strings.TrimSuffix(strings.Replace(want.Path, "/api/v1/", "/api/v2/", 1), "/")
					translated.Body = pair.Request.Body
					if i != 1 {
						translated.Body = nil
					}
					if !reflect.DeepEqual(s.Request, want) || !reflect.DeepEqual(pair.Request, translated) || !reflect.DeepEqual(s.Principal, principal) || pair.Principal != nil || s.FreshState != fresh || !reflect.DeepEqual(s.Settings, settings) || len(s.Requires) != 0 || len(s.Then) != 0 || len(pair.Then) != 0 || pair.Method != group.method || pair.OperationID != group.operation || s.Expect.Status != status || pair.Expect.Status != v2status {
						return nil, fmt.Errorf("%s: changed lifecycle exchange", s.ID)
					}
				}
			}
		}
		selected = append(selected, part...)
	}
	return selected, nil
}
