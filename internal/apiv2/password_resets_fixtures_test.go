package apiv2

func passwordResetFixtureCases() []fixtureCase {
	cases := []fixtureCase{
		{name: "admin_password_reset_link", operationID: "createAdminUserPasswordReset", method: "POST", path: "/api/v2/admin/users/7/password-reset", body: `{"delivery":"link"}`, headers: actingRequestAdmin, status: 201, schema: "AdminPasswordReset"},
		{name: "admin_password_reset_email", operationID: "createAdminUserPasswordReset", method: "POST", path: "/api/v2/admin/users/7/password-reset", body: `{"delivery":"email"}`, headers: actingRequestAdmin, status: 201, schema: "AdminPasswordReset"},
		{name: "password_reset_capability", operationID: "getPasswordResetCapability", method: "GET", path: "/api/v2/capabilities/password-reset", status: 200, schema: "PasswordResetCapability"},
		{name: "password_reset_lookup", operationID: "lookupPasswordReset", method: "GET", path: "/api/v2/password-resets/live", status: 200, schema: "PasswordResetLookup"},
		{name: "password_reset_completed", operationID: "completePasswordReset", method: "POST", path: "/api/v2/password-resets/live/complete", body: `{"password":"synthetic-password"}`, status: 200, schema: "PasswordResetCompletion"},
		{name: "password_reset_completed_sign_in_required", operationID: "completePasswordReset", method: "POST", path: "/api/v2/password-resets/sign-in-required/complete", body: `{"password":"synthetic-password"}`, status: 200, schema: "PasswordResetCompletion"},
	}
	for i := range cases {
		c := &cases[i]
		c.scenario = "Password reset link lifecycle with synthetic identities and credentials."
		c.assertHeaders = []string{"Cache-Control", "Content-Type"}
		c.schema = "#/components/schemas/" + c.schema
	}
	return cases
}
