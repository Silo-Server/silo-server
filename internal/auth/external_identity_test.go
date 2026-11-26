package auth

import (
	"context"
	"errors"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/models"
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

func TestUsableBreakGlass(t *testing.T) {
	base := models.User{BreakGlass: true, Role: models.RoleAdmin, Enabled: true, LocalPasswordLoginEnabled: true, PasswordHash: "$2a$stored"}
	if !usableBreakGlass(&base) {
		t.Fatal("usable account refused")
	}
	for name, mutate := range map[string]func(*models.User){
		"flag off":     func(u *models.User) { u.BreakGlass = false },
		"not admin":    func(u *models.User) { u.Role = models.RoleUser },
		"disabled":     func(u *models.User) { u.Enabled = false },
		"password off": func(u *models.User) { u.LocalPasswordLoginEnabled = false },
		"no password":  func(u *models.User) { u.PasswordHash = "" },
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
