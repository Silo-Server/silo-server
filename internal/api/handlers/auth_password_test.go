package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
)

type stubAccountPasswordService struct {
	available bool
	err       error
	userID    int
	sessionID string
	current   string
	new       string
}

func (s *stubAccountPasswordService) PasswordChangeAvailable(context.Context, int) (bool, error) {
	return s.available, s.err
}

func (s *stubAccountPasswordService) ChangePassword(_ context.Context, userID int, sessionID, currentPassword, newPassword string) error {
	s.userID = userID
	s.sessionID = sessionID
	s.current = currentPassword
	s.new = newPassword
	return s.err
}

func passwordClaims() auth.Claims {
	return auth.Claims{UserID: 42, Role: "user", SessionID: "session-1", TokenType: auth.TokenTypeAccess}
}

func newPasswordHandler(passwords accountPasswordService) *AuthHandler {
	handler := new(AuthHandler)
	handler.passwords = passwords
	handler.SetPrimaryProfileChecker(func(_ context.Context, _ int, profileID string) (bool, bool, error) {
		return profileID == "primary", profileID == "primary" || profileID == "secondary", nil
	})
	return handler
}

// A session holding a temporary password can do nothing else, including pick
// a profile, so it may replace the password with no profile declared.
func TestTemporaryPasswordSessionMayChangeWithoutProfile(t *testing.T) {
	t.Parallel()

	service := &stubAccountPasswordService{available: true}
	handler := newPasswordHandler(service)
	claims := passwordClaims()
	claims.PasswordChangeRequired = true
	if err := handler.AuthorizePasswordChange(context.Background(), &claims, ""); err != nil {
		t.Fatalf("restricted session refused: %v", err)
	}
	claims.ImpersonatorUserID = new(int)
	if err := handler.AuthorizePasswordChange(context.Background(), &claims, ""); err == nil {
		t.Fatal("impersonated session allowed to change the password")
	}

	settled := passwordClaims()
	if err := handler.AuthorizePasswordChange(context.Background(), &settled, ""); err == nil {
		t.Fatal("ordinary session without a profile allowed to change the password")
	}
}

// Only the account's own login session acting as the primary profile (or an
// admin with no profile selected) may replace the password; the capability
// reports the same answer.
func TestAuthorizePasswordChange(t *testing.T) {
	t.Parallel()

	handler := newPasswordHandler(&stubAccountPasswordService{available: true})
	admin := passwordClaims()
	admin.Role = "admin"
	for _, tc := range []struct {
		name    string
		claims  auth.Claims
		profile string
		allowed bool
	}{
		{"primary profile", passwordClaims(), "primary", true},
		{"secondary profile", passwordClaims(), "secondary", false},
		{"admin without selected profile", admin, "", true},
		{"admin on secondary profile", admin, "secondary", false},
		{"API key", auth.Claims{UserID: 42, Role: "admin", TokenType: auth.TokenTypeAPIKey}, "primary", false},
		{"impersonation", auth.Claims{UserID: 42, Role: "user", SessionID: "session-1", TokenType: auth.TokenTypeAccess, ImpersonatorUserID: new(7)}, "primary", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			claims := tc.claims
			err := handler.AuthorizePasswordChange(context.Background(), &claims, tc.profile)
			if tc.allowed && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !tc.allowed {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
					t.Fatalf("err = %v, want 403", err)
				}
			}
			capability, err := handler.AccountPasswordCapability(context.Background(), &claims, tc.profile)
			if err != nil || capability.ChangePassword != tc.allowed {
				t.Fatalf("capability = %+v, %v; want change_password %v", capability, err, tc.allowed)
			}
		})
	}
}

func TestChangePasswordMapsCredentialErrors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
		field  string
	}{
		{"wrong current password", auth.ErrCurrentPasswordInvalid, http.StatusBadRequest, "invalid_current_password", "current_password"},
		{"weak password", auth.ErrPasswordTooShort, http.StatusBadRequest, codeWeakPassword, "new_password"},
		{"password too long", auth.ErrPasswordTooLong, http.StatusBadRequest, codePasswordTooLong, "new_password"},
		{"reused temporary password", auth.ErrPasswordUnchanged, http.StatusBadRequest, codeWeakPassword, "new_password"},
		{"local login disabled", auth.ErrPasswordLoginDisabled, http.StatusConflict, "password_login_disabled", ""},
		{"repository failure", errors.New("database unavailable"), http.StatusInternalServerError, "internal_error", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			service := &stubAccountPasswordService{available: true, err: tc.err}
			claims := passwordClaims()
			err := newPasswordHandler(service).ChangePassword(context.Background(), &claims, "current password", "new password")
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != tc.status || apiErr.Code != tc.code || apiErr.Field != tc.field {
				t.Fatalf("err = %#v, want %d %s field %q", err, tc.status, tc.code, tc.field)
			}
			if service.userID != 42 || service.sessionID != "session-1" || service.new != "new password" {
				t.Fatalf("password call = (%d, %q, %q)", service.userID, service.sessionID, service.new)
			}
		})
	}
}
