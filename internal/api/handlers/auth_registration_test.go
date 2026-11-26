package handlers

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// The auth service refuses the password before reading any store (it has
// none here); the handler reports it at the password member.
func TestRegistrationPasswordRejectedAtPasswordMember(t *testing.T) {
	t.Parallel()
	h := &AuthHandler{service: &auth.Service{}}
	for _, tt := range []struct{ password, code string }{
		{"short", codeWeakPassword},
		{strings.Repeat("界", 25), codePasswordTooLong},
	} {
		in := RegistrationInput{Username: "user", Email: "user@example.test", Password: tt.password, InviteCode: "invite"}
		for _, register := range []func() error{
			func() error { _, err := h.SetupInitialUser(t.Context(), in); return err },
			func() error { _, err := h.Signup(t.Context(), in); return err },
		} {
			err := register()
			apiErr, ok := errors.AsType[*APIError](err)
			if !ok || apiErr.Status != http.StatusBadRequest || apiErr.Code != tt.code || apiErr.Field != "password" {
				t.Fatalf("registration error = %#v", err)
			}
		}
	}
}

// The administrator's refusal keeps the rule it broke, so the v2 listener can
// name it at the password member.
func TestAdminPasswordRefusalKeepsTheRule(t *testing.T) {
	t.Parallel()
	err := (&AdminHandler{}).validateAdminPassword(t.Context(), "short")
	if message, ok := auth.PasswordRuleMessage(err); !ok || message != "Use at least 8 characters." {
		t.Fatalf("validateAdminPassword error = %v; want the policy's rule", err)
	}
}
