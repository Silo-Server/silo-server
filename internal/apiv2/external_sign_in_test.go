package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// fakeExternalSignIn answers the external sign-in seam from memory. The
// member account (1) has one identity; unlinking it is refused unless
// allowUnlink is set.
type fakeExternalSignIn struct {
	allowUnlink bool
	unlinked    []int64
	linked      []handlers.AdminIdentityLinkInput
	staged      []plugins.StagedConfig
	testErr     error
	// credentialLinks are the directory links made.
	credentialLinks []handlers.CredentialsLinkInput
	// networkLinks are the network identity links made.
	networkLinks []handlers.NetworkLinkInput
}

func fixtureIdentity() handlers.ExternalIdentityView {
	signedIn := time.Date(2026, 1, 3, 4, 5, 6, 0, time.UTC)
	checked := time.Date(2026, 1, 3, 16, 7, 8, 0, time.UTC)
	return handlers.ExternalIdentityView{
		ID: 4, InstallationID: 3, ProviderID: "plugin:3:oidc", ProviderName: "Company SSO",
		ExternalSubject: "https://id.example.test/realms/silo|8f14e45f", Issuer: "https://id.example.test",
		Username: "alice", Email: "alice@example.test", DisplayName: "Alice Example",
		LinkedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), LastSignInAt: &signedIn,
		LastCheckedAt: &checked, LastCheckStatus: "active",
	}
}

func (f *fakeExternalSignIn) IdentitiesAvailable() bool     { return true }
func (f *fakeExternalSignIn) ConnectionTestAvailable() bool { return true }
func (f *fakeExternalSignIn) ListIdentities(_ context.Context, userID int) ([]handlers.ExternalIdentityView, error) {
	if userID > 3 {
		return nil, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Account not found"}
	}
	if userID != 1 {
		return nil, nil
	}
	return []handlers.ExternalIdentityView{fixtureIdentity()}, nil
}
func (f *fakeExternalSignIn) CanUnlinkAccountIdentity(_ context.Context, userID int) (bool, error) {
	return f.allowUnlink, nil
}
func (f *fakeExternalSignIn) UnlinkAccountIdentity(_ context.Context, userID int, identityID int64) error {
	if identityID == 6 && userID == 1 {
		// A second identity: the account keeps another way to sign in.
		f.unlinked = append(f.unlinked, identityID)
		return nil
	}
	if identityID != 4 || userID != 1 {
		return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Identity not found"}
	}
	if !f.allowUnlink {
		return &handlers.APIError{Status: http.StatusConflict, Code: "last_sign_in_method", Message: "The account has no other way to sign in; ask an administrator to set a password for it first"}
	}
	f.unlinked = append(f.unlinked, identityID)
	return nil
}
func (f *fakeExternalSignIn) LinkAdminUserIdentity(_ context.Context, userID int, in handlers.AdminIdentityLinkInput) (handlers.ExternalIdentityView, error) {
	if strings.HasSuffix(in.ExternalSubject, "|already") {
		return handlers.ExternalIdentityView{}, &handlers.APIError{Status: http.StatusConflict, Code: "already_linked", Message: "The account is already linked to this provider"}
	}
	if strings.HasSuffix(in.ExternalSubject, "|taken") {
		return handlers.ExternalIdentityView{}, &handlers.APIError{Status: http.StatusConflict, Code: "identity_linked_elsewhere", Message: "This identity is already linked to another account"}
	}
	f.linked = append(f.linked, in)
	view := fixtureIdentity()
	view.ID, view.InstallationID, view.ExternalSubject, view.Username, view.Email, view.DisplayName = 9, in.InstallationID, in.ExternalSubject, in.Username, in.Email, in.DisplayName
	view.LastSignInAt, view.LastCheckedAt, view.LastCheckStatus = nil, nil, ""
	return view, nil
}
func (f *fakeExternalSignIn) UnlinkAdminUserIdentity(_ context.Context, userID int, identityID int64) error {
	if identityID != 4 {
		return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Identity not found"}
	}
	f.unlinked = append(f.unlinked, identityID)
	return nil
}
func (f *fakeExternalSignIn) TestAuthBinding(_ context.Context, installationID int, capabilityID string, staged []plugins.StagedConfig) (plugins.AuthConnectionTestResult, error) {
	if f.testErr != nil {
		return plugins.AuthConnectionTestResult{}, f.testErr
	}
	f.staged = staged
	// Capability "ldap" is a password provider; every other one is OAuth.
	modes := []string{"oauth2"}
	if capabilityID == "ldap" {
		modes = []string{"password"}
	}
	return plugins.AuthConnectionTestResult{OK: false, Steps: []plugins.AuthConnectionTestStep{
		{ID: "discovery", Label: "Discovery document reachable", OK: true, Message: "Loaded https://id.example.test/.well-known/openid-configuration"},
		{ID: "issuer", Label: "Issuer matches", OK: false, Message: "The discovery document names a different issuer"},
	}, AuthModes: modes}, nil
}

func (f *fakeExternalSignIn) CredentialsLinkingAvailable() bool { return true }

// LinkAccountIdentityCredentials links directory installation 4 for the
// local password "right password": the directory knows alice with
// "directory password", and bob belongs to another account.
func (f *fakeExternalSignIn) LinkAccountIdentityCredentials(_ context.Context, userID int, in handlers.CredentialsLinkInput) (handlers.ExternalIdentityView, error) {
	switch {
	case in.InstallationID != 4:
		return handlers.ExternalIdentityView{}, auth.ErrUnknownAuthInstallation
	case in.Password != "right password":
		return handlers.ExternalIdentityView{}, auth.ErrLinkTicketPassword
	case in.DirectoryUsername == "bob":
		return handlers.ExternalIdentityView{}, auth.ErrIdentityLinkedElsewhere
	case in.DirectoryUsername != "alice" || in.DirectoryPassword != "directory password":
		return handlers.ExternalIdentityView{}, auth.ErrInvalidCredentials
	}
	f.credentialLinks = append(f.credentialLinks, in)
	view := fixtureIdentity()
	view.ID, view.InstallationID, view.ProviderID, view.ProviderName = 7, 4, "plugin:4:ldap", "Company directory"
	view.ExternalSubject, view.Issuer = "4f1c8a52-3b1d-4d7e-9c2a-6b8f0e1d2c3a", "ldaps://ldap.example.test"
	view.LastSignInAt = nil
	return view, nil
}

func (f *fakeExternalSignIn) NetworkLinkingAvailable() bool { return true }

// LinkAccountIdentityNetwork links network installation 5 for the local
// password "right password" when the request came through its overlay
// (password "off overlay" stands in for one that did not).
func (f *fakeExternalSignIn) LinkAccountIdentityNetwork(_ context.Context, userID int, in handlers.NetworkLinkInput) (handlers.ExternalIdentityView, error) {
	switch {
	case in.InstallationID != 5:
		return handlers.ExternalIdentityView{}, auth.ErrUnknownAuthInstallation
	case in.Password == "off overlay":
		return handlers.ExternalIdentityView{}, auth.ErrNetworkIdentityRequired
	case in.Password != "right password":
		return handlers.ExternalIdentityView{}, auth.ErrLinkTicketPassword
	}
	f.networkLinks = append(f.networkLinks, in)
	view := fixtureIdentity()
	view.ID, view.InstallationID, view.ProviderID, view.ProviderName = 8, 5, "plugin:5:tailscale", "Tailscale"
	view.ExternalSubject, view.Issuer = "controlplane.tailscale.com|123456789", "https://controlplane.tailscale.com"
	view.Username, view.Email = "alice@example.test", "alice@example.test"
	view.LastSignInAt = nil
	return view, nil
}

func externalSignInDeps(f *fakeExternalSignIn) Dependencies {
	deps := pilotDeps(nil, nil)
	deps.ExternalSignIn = f
	return deps
}

func TestExternalSignInCapabilities(t *testing.T) {
	rec := do(t, newTestHandler(t, externalSignInDeps(&fakeExternalSignIn{})), http.MethodGet, Prefix+"/auth/external-sign-in/capabilities", "", nil)
	var body ExternalSignInCapabilities
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.State != StateAvailable || !body.Identities || !body.AdminIdentities || !body.BreakGlass || !body.ConnectionTest || !body.LiveProviderChanges || !body.ProviderRecheck || !body.CredentialsLinking {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = do(t, newTestHandler(t, pilotDeps(nil, nil)), http.MethodGet, Prefix+"/auth/external-sign-in/capabilities", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"state":"not_configured"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestAccountIdentities(t *testing.T) {
	f := &fakeExternalSignIn{}
	h := newTestHandler(t, externalSignInDeps(f))
	rec := do(t, h, http.MethodGet, Prefix+"/account/identities", "", bearer(memberToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"provider_name":"Company SSO"`) || strings.Contains(rec.Body.String(), "external_subject") ||
		!strings.Contains(rec.Body.String(), `"can_unlink":false`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/account/identities", "", nil), TypeAuthenticationRequired)
	// An account with no identity has nothing to disconnect.
	if rec := do(t, h, http.MethodGet, Prefix+"/account/identities", "", bearer(adminToken)); !strings.Contains(rec.Body.String(), `{"items":[],"can_unlink":false}`) {
		t.Fatal(rec.Code, rec.Body.String())
	}

	// The last way in cannot be removed.
	requireProblem(t, do(t, h, http.MethodDelete, Prefix+"/account/identities/4", "", bearer(memberToken)), TypeLastSignInMethod)
	f.allowUnlink = true
	if rec := do(t, h, http.MethodGet, Prefix+"/account/identities", "", bearer(memberToken)); !strings.Contains(rec.Body.String(), `"can_unlink":true`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodDelete, Prefix+"/account/identities/4", "", bearer(memberToken)); rec.Code != http.StatusNoContent || len(f.unlinked) != 1 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodDelete, Prefix+"/account/identities/5", "", bearer(memberToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodDelete, Prefix+"/account/identities/zero", "", bearer(memberToken)), TypeValidationFailed)
	// Only the account's own signed-in session may disconnect a sign-in.
	requireProblem(t, do(t, h, http.MethodDelete, Prefix+"/account/identities/4", "", bearer(apiKeyToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodDelete, Prefix+"/account/identities/4", "", bearer(impersonatedToken)), TypePermissionDenied)
	if len(f.unlinked) != 1 {
		t.Fatalf("unlinked = %v", f.unlinked)
	}
	requireProblem(t, do(t, newTestHandler(t, pilotDeps(nil, nil)), http.MethodGet, Prefix+"/account/identities", "", bearer(memberToken)), TypeDependencyUnavailable)
}

func TestAdminUserIdentities(t *testing.T) {
	f := &fakeExternalSignIn{}
	h := newTestHandler(t, externalSignInDeps(f))
	base := Prefix + "/admin/users/1/identities"
	rec := do(t, h, http.MethodGet, base, "", actingRequestAdmin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"external_subject":"https://id.example.test/realms/silo|8f14e45f"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, base, "", with(bearer(memberToken), "X-Profile-Id", "p-owner")), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/users/9/identities", "", actingRequestAdmin), TypeNotFound)

	rec = do(t, h, http.MethodPost, base, `{"installation_id":"3","external_subject":"https://id.example.test/realms/silo|new","username":"alice"}`, actingRequestAdmin)
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") != base || !strings.Contains(rec.Body.String(), `"last_sign_in_at":null`) {
		t.Fatal(rec.Code, rec.Header(), rec.Body.String())
	}
	if len(f.linked) != 1 || f.linked[0].InstallationID != 3 || f.linked[0].Username != "alice" {
		t.Fatalf("linked = %+v", f.linked)
	}
	requireProblem(t, do(t, h, http.MethodPost, base, `{"installation_id":"3","external_subject":"https://id.example.test/realms/silo|taken"}`, actingRequestAdmin), TypeIdentityLinkedElsewhere)
	requireProblem(t, do(t, h, http.MethodPost, base, `{"installation_id":"3","external_subject":"https://id.example.test/realms/silo|already"}`, actingRequestAdmin), TypeAlreadyLinked)
	requireProblem(t, do(t, h, http.MethodPost, base, `{"installation_id":"0","external_subject":"x"}`, actingRequestAdmin), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, base, `{"installation_id":"3","external_subject":""}`, actingRequestAdmin), TypeValidationFailed)

	if rec := do(t, h, http.MethodDelete, base+"/4", "", actingRequestAdmin); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodDelete, base+"/5", "", actingRequestAdmin), TypeNotFound)
}

func TestAdminPluginAuthBindingTest(t *testing.T) {
	f := &fakeExternalSignIn{}
	h := newTestHandler(t, externalSignInDeps(f))
	path := Prefix + "/admin/plugins/installations/3/auth-binding/test"
	rec := do(t, h, http.MethodPost, path, `{"capability_id":"oidc","config":[{"key":"oidc","value":{"issuer":"https://id.example.test","client_secret":""}}]}`, actingRequestAdmin)
	var body AuthConnectionTestResult
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.OK || len(body.Steps) != 2 || body.Steps[1].OK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	// The admin copies the callback URL to register at the provider.
	if body.CallbackURL != "https://silo.example.test/api/v2/auth/oauth/3/callback" {
		t.Fatalf("callback_url = %q", body.CallbackURL)
	}
	if len(f.staged) != 1 || f.staged[0].Key != "oidc" || f.staged[0].Value["issuer"] != "https://id.example.test" {
		t.Fatalf("staged = %+v", f.staged)
	}
	// A password (LDAP) provider has no redirect URI to register.
	rec = do(t, h, http.MethodPost, path, `{"capability_id":"ldap","config":[]}`, actingRequestAdmin)
	body = AuthConnectionTestResult{}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.CallbackURL != "" {
		t.Fatalf("ldap test: %d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, path, `{"config":[{"key":" ","value":{}}]}`, actingRequestAdmin), TypeValidationFailed)
	f.testErr = plugins.ErrAuthConnectionTestUnsupported
	requireProblem(t, do(t, h, http.MethodPost, path, `{}`, actingRequestAdmin), TypeConflict)
	f.testErr = plugins.ErrInstallationNotFound
	requireProblem(t, do(t, h, http.MethodPost, path, `{}`, actingRequestAdmin), TypeNotFound)
}

// TestAccountIdentityCredentialsLink: an account links its directory
// identity after re-entering its local password; the refusals keep their
// problem types.
func TestAccountIdentityCredentialsLink(t *testing.T) {
	f := &fakeExternalSignIn{}
	h := newTestHandler(t, externalSignInDeps(f))
	path := Prefix + "/account/identities/link-credentials"
	body := func(installation, password, username, directoryPassword string) string {
		return `{"installation_id":"` + installation + `","password":"` + password + `","username":"` + username + `","directory_password":"` + directoryPassword + `"}`
	}
	rec := do(t, h, http.MethodPost, path, body("4", "right password", "alice", "directory password"), bearer(memberToken))
	var identity AccountIdentity
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") != Prefix+"/account/identities" || json.Unmarshal(rec.Body.Bytes(), &identity) != nil || identity.ID != "7" || identity.ProviderID != "plugin:4:ldap" {
		t.Fatal(rec.Code, rec.Header(), rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "external_subject") || strings.Contains(rec.Body.String(), "directory password") {
		t.Fatal("account view leaks", rec.Body.String())
	}
	if len(f.credentialLinks) != 1 || f.credentialLinks[0].InstallationID != 4 {
		t.Fatalf("links = %+v", f.credentialLinks)
	}
	p := requireProblem(t, do(t, h, http.MethodPost, path, body("4", "wrong", "alice", "directory password"), bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.password" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	p = requireProblem(t, do(t, h, http.MethodPost, path, body("4", "right password", "alice", "nope"), bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.directory_password" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, path, body("4", "right password", "bob", "directory password"), bearer(memberToken)), TypeIdentityLinkedElsewhere)
	requireProblem(t, do(t, h, http.MethodPost, path, body("3", "right password", "alice", "directory password"), bearer(memberToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPost, path, body("0", "right password", "alice", "directory password"), bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, path, body("4", "right password", "", "directory password"), bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, path, body("4", "right password", "alice", "directory password"), nil), TypeAuthenticationRequired)
	// Only the account's own signed-in session links.
	requireProblem(t, do(t, h, http.MethodPost, path, body("4", "right password", "alice", "directory password"), with(bearer(apiKeyToken), "X-Profile-Id", "p-primary")), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodPost, path, body("4", "right password", "alice", "directory password"), bearer(impersonatedToken)), TypePermissionDenied)
	if len(f.credentialLinks) != 1 {
		t.Fatalf("refusals linked: %+v", f.credentialLinks)
	}

	// Null members are refused like missing ones.
	requireProblem(t, do(t, h, http.MethodPost, path, `{"installation_id":"4","password":"right password","username":null,"directory_password":"directory password"}`, bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, path, `{"installation_id":"4","password":"right password","username":"alice"}`, bearer(memberToken)), TypeValidationFailed)

	// The service's own refusals: each has the problem type clients branch
	// on (the fixed link-credentials contract).
	for err, want := range map[error]ProblemType{
		auth.ErrPasswordLoginDisabled:   TypeLocalPasswordRequired,
		auth.ErrAccountAlreadyLinked:    TypeAlreadyLinked,
		auth.ErrIdentityLinkedElsewhere: TypeIdentityLinkedElsewhere,
		auth.ErrNotPermitted:            TypeNotPermitted,
		auth.ErrProviderAccountDisabled: TypeAccountDisabled,
		auth.ErrProviderPasswordExpired: TypeProviderPasswordExpired,
		auth.ErrProviderUnavailable:     TypeProviderUnavailable,
		auth.ErrUserDisabled:            TypePermissionDenied,
		auth.ErrUnknownAuthInstallation: TypeNotFound,
	} {
		var problem *Problem
		if !errors.As(credentialsLinkProblem(err), &problem) || problem.Type != want.URI() {
			t.Fatalf("%v -> %+v, want %s", err, problem, want.ID)
		}
	}
}
