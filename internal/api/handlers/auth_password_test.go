package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
)

type stubAccountPasswordService struct {
	available bool
	err       error
	userID    int
	current   string
	new       string
}

func (s *stubAccountPasswordService) PasswordChangeAvailable(context.Context, int) (bool, error) {
	return s.available, s.err
}

func (s *stubAccountPasswordService) ChangePassword(_ context.Context, userID int, currentPassword, newPassword string) error {
	s.userID = userID
	s.current = currentPassword
	s.new = newPassword
	return s.err
}

func passwordRequest(t *testing.T, method, profileID string, claims auth.Claims, body any) *http.Request {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
	}
	req := httptest.NewRequest(method, "/api/v1/auth/account/password", bytes.NewReader(payload))
	ctx := apimw.SetClaims(req.Context(), &claims)
	if profileID != "" {
		ctx = apimw.SetProfileID(ctx, profileID)
	}
	return req.WithContext(ctx)
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

func TestHandleChangePasswordRequiresPrimaryProfile(t *testing.T) {
	t.Parallel()

	service := &stubAccountPasswordService{available: true}
	handler := newPasswordHandler(service)
	body := changePasswordRequest{CurrentPassword: "current password", NewPassword: "new password"}

	t.Run("primary profile", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.HandleChangePassword(rec, passwordRequest(t, http.MethodPost, "primary", passwordClaims(), body))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusNoContent, rec.Body.String())
		}
		if service.userID != 42 || service.current != body.CurrentPassword || service.new != body.NewPassword {
			t.Fatalf("password call = (%d, %q, %q)", service.userID, service.current, service.new)
		}
	})

	t.Run("secondary profile", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.HandleChangePassword(rec, passwordRequest(t, http.MethodPost, "secondary", passwordClaims(), body))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusForbidden, rec.Body.String())
		}
	})

	t.Run("admin without selected profile", func(t *testing.T) {
		claims := passwordClaims()
		claims.Role = "admin"
		rec := httptest.NewRecorder()
		handler.HandleChangePassword(rec, passwordRequest(t, http.MethodPost, "", claims, body))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusNoContent, rec.Body.String())
		}
	})

	t.Run("admin on secondary profile", func(t *testing.T) {
		claims := passwordClaims()
		claims.Role = "admin"
		rec := httptest.NewRecorder()
		handler.HandleChangePassword(rec, passwordRequest(t, http.MethodPost, "secondary", claims, body))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusForbidden, rec.Body.String())
		}
	})
}

func TestHandleChangePasswordRejectsNonUserSessions(t *testing.T) {
	t.Parallel()

	handler := newPasswordHandler(&stubAccountPasswordService{available: true})
	body := changePasswordRequest{CurrentPassword: "current password", NewPassword: "new password"}

	tests := []struct {
		name   string
		claims auth.Claims
	}{
		{name: "API key", claims: auth.Claims{UserID: 42, Role: "admin", TokenType: auth.TokenTypeAPIKey}},
		{name: "impersonation", claims: auth.Claims{UserID: 42, Role: "user", SessionID: "session-1", TokenType: auth.TokenTypeAccess, ImpersonatorUserID: new(7)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			handler.HandleChangePassword(rec, passwordRequest(t, http.MethodPost, "primary", tt.claims, body))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusForbidden, rec.Body.String())
			}
		})
	}
}

func TestHandleChangePasswordMapsCredentialErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "wrong current password", err: auth.ErrCurrentPasswordInvalid, wantStatus: http.StatusBadRequest, wantCode: "invalid_current_password"},
		{name: "weak password", err: auth.ErrPasswordTooShort, wantStatus: http.StatusBadRequest, wantCode: "weak_password"},
		{name: "password too long", err: auth.ErrPasswordTooLong, wantStatus: http.StatusBadRequest, wantCode: "password_too_long"},
		{name: "local login disabled", err: auth.ErrPasswordLoginDisabled, wantStatus: http.StatusConflict, wantCode: "password_login_disabled"},
		{name: "repository failure", err: errors.New("database unavailable"), wantStatus: http.StatusInternalServerError, wantCode: "internal_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler := newPasswordHandler(&stubAccountPasswordService{available: true, err: tt.err})
			rec := httptest.NewRecorder()
			handler.HandleChangePassword(rec, passwordRequest(t, http.MethodPost, "primary", passwordClaims(), changePasswordRequest{
				CurrentPassword: "current password",
				NewPassword:     "new password",
			}))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			var response errorResponse
			if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Error != tt.wantCode {
				t.Fatalf("error = %q, want %q", response.Error, tt.wantCode)
			}
		})
	}
}

func TestAccountPasswordCapability(t *testing.T) {
	t.Parallel()

	handler := newPasswordHandler(&stubAccountPasswordService{available: true})
	rec := httptest.NewRecorder()
	req := passwordRequest(t, http.MethodGet, "primary", passwordClaims(), nil)
	handler.HandleAccountPasswordCapability(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response accountPasswordCapabilityResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.ChangePassword || !response.RequiresCurrentPassword {
		t.Fatalf("capability = %#v", response)
	}
	if response.MinimumPasswordLength != auth.MinimumPasswordLength || response.MaximumPasswordBytes != auth.MaximumPasswordBytes {
		t.Fatalf("password limits = (%d, %d)", response.MinimumPasswordLength, response.MaximumPasswordBytes)
	}
}
