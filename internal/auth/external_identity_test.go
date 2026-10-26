package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

func TestExternalIdentityFromTypedResponse(t *testing.T) {
	verified := true
	identity, err := externalIdentityFromResponse(context.Background(), 3, &pluginv1.AuthenticateResponse{
		ExternalSubject: "https://id.example.test|abc",
		Issuer:          "https://id.example.test",
		Username:        "alice",
		Email:           " alice@example.test ",
		EmailVerified:   &verified,
		DisplayName:     "Alice",
		ManagedRole:     pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_ADMIN,
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "https://id.example.test|abc" || identity.Username != "alice" || identity.Email != "alice@example.test" ||
		!identity.emailVerified() || identity.ManagedRole != pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_ADMIN {
		t.Fatalf("identity = %+v", identity)
	}
}

// The claims Struct is the plugin's own: the host reads none of it. Above
// all a claims email_verified never verifies an email, since it would open
// email auto-match.
func TestExternalIdentityIgnoresClaims(t *testing.T) {
	identity, err := externalIdentityFromResponse(context.Background(), 3, &pluginv1.AuthenticateResponse{
		ExternalSubject: "subject",
		Email:           "bob@example.test",
		Claims: mustStruct(t, map[string]any{
			"iss": "https://legacy.example.test", "preferred_username": "bob", "email": "other@example.test", "email_verified": true,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity.Issuer != "" || identity.Username != "" || identity.Email != "bob@example.test" || identity.emailVerified() {
		t.Fatalf("identity = %+v", identity)
	}
}

func TestExternalIdentityDenials(t *testing.T) {
	for _, tc := range []struct {
		denial pluginv1.AuthDenial
		want   error
	}{
		{pluginv1.AuthDenial_AUTH_DENIAL_INVALID_CREDENTIALS, ErrInvalidCredentials},
		{pluginv1.AuthDenial_AUTH_DENIAL_NOT_PERMITTED, ErrNotPermitted},
		{pluginv1.AuthDenial_AUTH_DENIAL_ACCOUNT_DISABLED, ErrProviderAccountDisabled},
		{pluginv1.AuthDenial_AUTH_DENIAL_PASSWORD_EXPIRED, ErrProviderPasswordExpired},
		{pluginv1.AuthDenial_AUTH_DENIAL_PROVIDER_UNAVAILABLE, ErrProviderUnavailable},
		{pluginv1.AuthDenial(99), ErrInvalidCredentials},
	} {
		// A denial wins even when a (misbehaving) plugin also sets a subject.
		_, err := externalIdentityFromResponse(context.Background(), 3, &pluginv1.AuthenticateResponse{ExternalSubject: "s", Denial: tc.denial})
		if !errors.Is(err, tc.want) {
			t.Errorf("%v: err = %v, want %v", tc.denial, err, tc.want)
		}
	}
	// Sign-in treats a provider-disabled account like a disabled Silo one.
	if !errors.Is(ErrProviderAccountDisabled, ErrUserDisabled) {
		t.Error("ErrProviderAccountDisabled is not an ErrUserDisabled")
	}
	for _, response := range []*pluginv1.AuthenticateResponse{nil, {}, {ExternalSubject: "  "}} {
		if _, err := externalIdentityFromResponse(context.Background(), 3, response); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%+v: err = %v", response, err)
		}
	}
}

func TestPluginCallErrorMapping(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		err  error
		want error
	}{
		{plugins.ErrInstallationDisabled, ErrInvalidCredentials},
		{status.Error(codes.Unauthenticated, "bad password"), ErrInvalidCredentials},
		{status.Error(codes.Unavailable, "down"), ErrProviderUnavailable},
		{status.Error(codes.Internal, "boom"), ErrProviderUnavailable},
		{errors.New("start failed"), ErrProviderUnavailable},
		{ErrUserDisabled, ErrUserDisabled},
	} {
		if got := pluginCallError(ctx, 3, "authenticate", tc.err); !errors.Is(got, tc.want) {
			t.Errorf("%v: got %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestUsernameBase(t *testing.T) {
	for _, tc := range []struct {
		identity ExternalIdentity
		want     string
	}{
		{ExternalIdentity{Username: "Alice Smith", Email: "a@example.test", Subject: "s"}, "alice_smith"},
		{ExternalIdentity{Username: "!!!", Email: "Bob.Jones@example.test", Subject: "s"}, "bob.jones"},
		{ExternalIdentity{Subject: "https://id.example.test|8F14E45F"}, "8f14e45f"},
		{ExternalIdentity{Subject: "|||"}, "user"},
		// Kanidm sends its SPN, Entra a UPN: keep the name, not name+domain.
		{ExternalIdentity{Username: "alice@idm.example.test", Email: "a@example.test", Subject: "s"}, "alice"},
		{ExternalIdentity{Username: "Bob.Jones@contoso.example", Subject: "s"}, "bob.jones"},
		{ExternalIdentity{Username: "@example.test", Email: "carol@example.test", Subject: "s"}, "carol"},
	} {
		if got := usernameBase(tc.identity); got != tc.want {
			t.Errorf("%+v: got %q, want %q", tc.identity, got, tc.want)
		}
	}
}

func TestNormalizedProviderEmail(t *testing.T) {
	for in, want := range map[string]string{
		"Alice@Example.Test":       NormalizeEmail("Alice@Example.Test"),
		"":                         "",
		"not an email":             "",
		"Alice <alice@example.io>": "",
	} {
		if got := normalizedProviderEmail(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestOAuthFailureReason(t *testing.T) {
	for err, want := range map[error]string{
		ErrNotPermitted:                              "not_permitted",
		ErrAccountRequired:                           "account_required",
		fmt.Errorf("wrap: %w", ErrEmailInUse):        "email_in_use",
		ErrIdentityLinkedElsewhere:                   "identity_linked_elsewhere",
		ErrUserDisabled:                              "account_disabled",
		ErrProviderUnavailable:                       "provider_unavailable",
		ErrInvalidCredentials:                        "login_failed",
		errors.New("database fell over"):             "login_failed",
		fmt.Errorf("x: %w", ErrAccountAlreadyLinked): "already_linked",
	} {
		if got := OAuthFailureReason(err); got != want {
			t.Errorf("%v: got %q, want %q", err, got, want)
		}
	}
}

func TestProviderListMergesPluginSourceAndPicksDefault(t *testing.T) {
	svc := NewService(&LocalProvider{}, nil, nil, nil, nil, nil, nil)
	if got := svc.ListProviders(); len(got) != 1 || got[0].ID != LocalProviderID || !got[0].Default {
		t.Fatalf("local only = %+v", got)
	}
	source := staticProviderSource{
		{Info: LoginProviderInfo{ID: "plugin:3:ldap", DisplayName: "Directory", Mode: "credentials", InstallationID: 3}, Provider: &credentialsDirectory{}},
	}
	svc.SetPluginProviderSource(source)
	got := svc.ListProviders()
	if len(got) != 2 || got[0].ID != LocalProviderID || !got[0].Default || got[1].Default {
		t.Fatalf("with plugin = %+v", got)
	}
	// A binding marked default_login takes the default.
	source[0].Info.Default = true
	got = svc.ListProviders()
	if got[0].ID != "plugin:3:ldap" || !got[0].Default {
		t.Fatalf("plugin default = %+v", got)
	}
	if svc.passwordProviderByID("plugin:3:ldap") == nil || svc.passwordProviderByID("plugin:9:none") != nil {
		t.Fatal("provider lookup")
	}
	// Without a users repository, routing cannot look the name up and keeps
	// the password local.
	if id, err := svc.routePasswordLogin(context.Background(), "anyone"); err != nil || id != LocalProviderID {
		t.Fatalf("route = %q, %v", id, err)
	}
}

func TestUsableBreakGlass(t *testing.T) {
	base := models.User{BreakGlass: true, Role: models.RoleAdmin, Enabled: true, LocalPasswordLoginEnabled: true}
	if !usableBreakGlass(&base) {
		t.Fatal("usable account refused")
	}
	for name, mutate := range map[string]func(*models.User){
		"flag off":     func(u *models.User) { u.BreakGlass = false },
		"not admin":    func(u *models.User) { u.Role = models.RoleUser },
		"disabled":     func(u *models.User) { u.Enabled = false },
		"password off": func(u *models.User) { u.LocalPasswordLoginEnabled = false },
	} {
		user := base
		mutate(&user)
		if usableBreakGlass(&user) {
			t.Errorf("%s: counted as usable", name)
		}
	}
}

func mustStruct(t *testing.T, values map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestPasswordSignInNeverReachesAnOAuthProvider: naming an OAuth provider in
// a password sign-in is refused before the plugin is asked, so the password
// never reaches it.
func TestPasswordSignInNeverReachesAnOAuthProvider(t *testing.T) {
	svc := NewService(&LocalProvider{}, nil, nil, nil, nil, nil, nil)
	oauth := &credentialsDirectory{}
	svc.SetPluginProviderSource(staticProviderSource{
		{Info: LoginProviderInfo{ID: "plugin:4:oidc", DisplayName: "SSO", Mode: ProviderModeOAuth, InstallationID: 4}, Provider: oauth},
	})
	if _, _, err := svc.LoginWithProvider(context.Background(), "plugin:4:oidc", "alice", "secret", "test", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
	if len(oauth.asked) != 0 {
		t.Fatalf("the OAuth provider was asked for %v", oauth.asked)
	}
}
