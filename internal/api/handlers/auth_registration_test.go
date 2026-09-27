package handlers

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestRegistrationPasswordRejectedBeforeService(t *testing.T) {
	t.Parallel()
	h := &AuthHandler{}
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
