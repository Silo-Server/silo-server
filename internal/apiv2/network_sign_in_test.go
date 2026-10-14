package apiv2

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
)

func networkSignInDeps(sessions *fakeSessionService) Dependencies {
	deps := pilotDeps(nil, nil)
	deps.Sessions = sessions
	return deps
}

func TestSignInWithNetworkIdentity(t *testing.T) {
	sessions := &fakeSessionService{networkPeer: true}
	h := newTestHandler(t, networkSignInDeps(sessions))
	path := Prefix + "/auth/network/5/sign-in"

	rec := do(t, h, http.MethodPost, path, `{}`, map[string]string{"User-Agent": "Silo/1.0 (tvOS)"})
	var pair TokenPair
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &pair) != nil || pair.AccessToken != "acc" || pair.User.Username != "laura" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if sessions.lastNetwork.InstallationID != 5 || sessions.lastNetwork.DeviceName != "Silo/1.0 (tvOS)" {
		t.Fatalf("input = %+v", sessions.lastNetwork)
	}

	// A cross-site form post cannot sign the device in: the body must be
	// JSON, which a form cannot send without a CORS preflight.
	requireProblem(t, do(t, h, http.MethodPost, path, "", nil), TypeUnsupportedMediaType)
	requireProblem(t, do(t, h, http.MethodPost, path, `{}`, map[string]string{"Content-Type": "text/plain"}), TypeUnsupportedMediaType)
	requireProblem(t, do(t, h, http.MethodPost, path, `x=1`, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}), TypeUnsupportedMediaType)

	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/auth/network/6/sign-in", `{}`, nil), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/auth/network/0/sign-in", `{}`, nil), TypeValidationFailed)

	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/auth/network/7/sign-in", `{}`, nil), TypeNetworkIdentityRequired)
}

// The shared handler's refusals keep their problem types on v2.
func TestSignInWithNetworkIdentityRefusals(t *testing.T) {
	refusal := func(status int, code string, cause error) error {
		err := &handlers.APIError{Status: status, Code: code, Message: "refused"}
		if cause != nil {
			return err.WithCause(cause)
		}
		return err
	}
	for _, tc := range []struct {
		err  error
		want ProblemType
	}{
		{refusal(http.StatusForbidden, "not_permitted", auth.ErrNotPermitted), TypeNotPermitted},
		{refusal(http.StatusForbidden, "not_permitted", auth.ErrAccountRequired), TypeAccountRequired},
		{refusal(http.StatusConflict, "email_in_use", nil), TypeEmailInUse},
		{refusal(http.StatusServiceUnavailable, "provider_unavailable", nil), TypeProviderUnavailable},
		{refusal(http.StatusForbidden, "network_identity_required", nil), TypeNetworkIdentityRequired},
		{refusal(http.StatusForbidden, "user_disabled", nil), TypePermissionDenied},
		{refusal(http.StatusNotFound, "not_found", nil), TypeNotFound},
	} {
		if p := loginProblem(tc.err); p.Type != tc.want.URI() {
			t.Fatalf("%v -> %s, want %s", tc.err, p.Type, tc.want.ID)
		}
	}
}

func TestListAuthProvidersOffersNetworkProviderToOverlayPeers(t *testing.T) {
	rec := do(t, newTestHandler(t, networkSignInDeps(&fakeSessionService{networkPeer: true})), http.MethodGet, Prefix+"/auth/providers", "", nil)
	var out AuthProviderCollection
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var network *AuthProvider
	for i := range out.Items {
		if out.Items[i].Mode == auth.ProviderModeNetwork {
			network = &out.Items[i]
		}
	}
	if network == nil || network.NetworkSignInPath != Prefix+"/auth/network/5/sign-in" || network.Default || network.NativeStartPath != "" ||
		network.NetworkIdentity == nil || network.NetworkIdentity.DisplayName != "Laura Example" || network.NetworkIdentity.Username != "laura@example.test" {
		t.Fatalf("network provider = %+v in %s", network, rec.Body.String())
	}
	if !out.PasswordLogin {
		t.Fatal("a network provider must not change password_login")
	}

	rec = do(t, newTestHandler(t, networkSignInDeps(&fakeSessionService{})), http.MethodGet, Prefix+"/auth/providers", "", nil)
	if strings.Contains(rec.Body.String(), `"network`) {
		t.Fatalf("network fields outside the overlay: %s", rec.Body.String())
	}
}

func TestAccountIdentityNetworkLink(t *testing.T) {
	f := &fakeExternalSignIn{}
	h := newTestHandler(t, externalSignInDeps(f))
	path := Prefix + "/account/identities/link-network"
	body := func(installation, password string) string {
		return `{"installation_id":"` + installation + `","password":"` + password + `"}`
	}
	rec := do(t, h, http.MethodPost, path, body("5", "right password"), bearer(memberToken))
	var identity AccountIdentity
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") != Prefix+"/account/identities" || json.Unmarshal(rec.Body.Bytes(), &identity) != nil || identity.ID != "8" || identity.ProviderID != "plugin:5:tailscale" {
		t.Fatal(rec.Code, rec.Header(), rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "external_subject") {
		t.Fatal("account view leaks the subject", rec.Body.String())
	}
	if len(f.networkLinks) != 1 || f.networkLinks[0].InstallationID != 5 {
		t.Fatalf("links = %+v", f.networkLinks)
	}
	p := requireProblem(t, do(t, h, http.MethodPost, path, body("5", "wrong"), bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.password" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, path, body("5", "off overlay"), bearer(memberToken)), TypeNetworkIdentityRequired)
	requireProblem(t, do(t, h, http.MethodPost, path, body("4", "right password"), bearer(memberToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPost, path, body("5", "right password"), nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPost, path, body("5", "right password"), with(bearer(apiKeyToken), "X-Profile-Id", "p-primary")), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodPost, path, body("5", "right password"), bearer(impersonatedToken)), TypePermissionDenied)
	if len(f.networkLinks) != 1 {
		t.Fatalf("refusals linked: %+v", f.networkLinks)
	}

	for err, want := range map[error]ProblemType{
		auth.ErrNetworkIdentityRequired: TypeNetworkIdentityRequired,
		auth.ErrInvalidCredentials:      TypeNotPermitted,
		auth.ErrNotPermitted:            TypeNotPermitted,
		auth.ErrPasswordLoginDisabled:   TypeLocalPasswordRequired,
		auth.ErrAccountAlreadyLinked:    TypeAlreadyLinked,
		auth.ErrIdentityLinkedElsewhere: TypeIdentityLinkedElsewhere,
		auth.ErrProviderUnavailable:     TypeProviderUnavailable,
		auth.ErrUserDisabled:            TypePermissionDenied,
		auth.ErrUnknownAuthInstallation: TypeNotFound,
	} {
		var problem *Problem
		if !errors.As(networkLinkProblem(err), &problem) || problem.Type != want.URI() {
			t.Fatalf("%v -> %+v, want %s", err, problem, want.ID)
		}
	}
}
