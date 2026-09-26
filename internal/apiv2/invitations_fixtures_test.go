package apiv2

func invitationFixtureCases() []fixtureCase {
	cases := []fixtureCase{
		{name: "invitation_capabilities", operationID: "getInvitationCapabilities", method: "GET", path: "/api/v2/invitations/capabilities", status: 200, schema: "InvitationCapabilities"},
		{name: "admin_invitation_capabilities", operationID: "getAdminInvitationCapabilities", method: "GET", path: "/api/v2/admin/invitations/capabilities", headers: actingRequestAdmin, status: 200, schema: "InvitationCapabilities"},
		{name: "invitation_lookup", operationID: "lookupInvitation", method: "GET", path: "/api/v2/invitations/pending", status: 200, schema: "InvitationLookup"},
		{name: "invitation_accepted", operationID: "acceptInvitation", method: "POST", path: "/api/v2/invitations/pending/accept", body: `{"password":"synthetic-password"}`, status: 201, schema: "InvitationAcceptance"},
		{name: "invitation_accepted_sign_in_required", operationID: "acceptInvitation", method: "POST", path: "/api/v2/invitations/sign-in-required/accept", body: `{"password":"synthetic-password"}`, status: 201, schema: "InvitationAcceptance"},
		{name: "admin_invitation_list", operationID: "listAdminInvitations", method: "GET", path: "/api/v2/admin/invitations?limit=1", headers: actingRequestAdmin, status: 200, schema: "CollectionAdminInvitation"},
		{name: "admin_invitation_get", operationID: "getAdminInvitation", method: "GET", path: "/api/v2/admin/invitations/7", headers: actingRequestAdmin, status: 200, schema: "AdminInvitation"},
		{name: "admin_invitation_created", operationID: "createAdminInvitation", method: "POST", path: "/api/v2/admin/invitations", body: `{"email":"invitee@example.invalid"}`, headers: actingRequestAdmin, status: 201, schema: "InvitationDelivery"},
		{name: "admin_invitation_delivery_uncertain", operationID: "createAdminInvitation", method: "POST", path: "/api/v2/admin/invitations", body: `{"email":"invitee@example.invalid","note":"smtp-failed"}`, headers: actingRequestAdmin, status: 201, schema: "InvitationDelivery"},
		{name: "admin_invitation_resent", operationID: "resendAdminInvitation", method: "POST", path: "/api/v2/admin/invitations/7/resend", headers: actingRequestAdmin, status: 201, schema: "InvitationDelivery"},
		{name: "admin_invitation_revoked", operationID: "revokeAdminInvitation", method: "DELETE", path: "/api/v2/admin/invitations/7", headers: actingRequestAdmin, status: 204},
	}
	for i := range cases {
		c := &cases[i]
		c.scenario = "Invitation lifecycle with synthetic identities and credentials."
		c.assertHeaders = []string{"Cache-Control"}
		if c.schema != "" {
			c.schema = "#/components/schemas/" + c.schema
			c.assertHeaders = append(c.assertHeaders, "Content-Type")
		}
		if c.operationID == "createAdminInvitation" || c.operationID == "resendAdminInvitation" {
			c.assertHeaders = append(c.assertHeaders, "Location")
		}
	}
	return cases
}
