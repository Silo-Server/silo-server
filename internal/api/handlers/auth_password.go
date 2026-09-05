package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type accountPasswordCapabilityResponse struct {
	SchemaVersion           int  `json:"schema_version"`
	ChangePassword          bool `json:"change_password"`
	RequiresCurrentPassword bool `json:"requires_current_password"`
	MinimumPasswordLength   int  `json:"minimum_password_length"`
	MaximumPasswordBytes    int  `json:"maximum_password_bytes"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// HandleAccountPasswordCapability reports whether this authenticated account
// and active profile may use local self-service password changes.
func (h *AuthHandler) HandleAccountPasswordCapability(w http.ResponseWriter, r *http.Request) {
	view, err := h.AccountPasswordCapability(r.Context(), apimw.GetClaims(r.Context()), requestProfileID(r))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, accountPasswordCapabilityResponse{
		SchemaVersion:           1,
		ChangePassword:          view.ChangePassword,
		RequiresCurrentPassword: view.RequiresCurrentPassword,
		MinimumPasswordLength:   view.MinimumPasswordLength,
		MaximumPasswordBytes:    view.MaximumPasswordBytes,
	})
}

// HandleChangePassword handles POST /auth/account/password. The password is
// account-wide, so only the active primary profile may replace it. An admin
// without a selected profile is also allowed, matching the app's acting-admin
// policy, but selecting a secondary profile removes that authority.
func (h *AuthHandler) HandleChangePassword(w http.ResponseWriter, r *http.Request) {
	claims := apimw.GetClaims(r.Context())
	if err := h.AuthorizePasswordChange(r.Context(), claims, requestProfileID(r)); err != nil {
		writeAPIError(w, err)
		return
	}

	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if err := h.ChangePassword(r.Context(), claims, req.CurrentPassword, req.NewPassword); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AccountPasswordCapabilityView is the self-service password capability of
// one account and active profile.
type AccountPasswordCapabilityView struct {
	// Configured is false when no password service is wired.
	Configured bool
	// ChangePassword is whether this caller may replace the account password.
	ChangePassword          bool
	RequiresCurrentPassword bool
	MinimumPasswordLength   int
	MaximumPasswordBytes    int
}

// AccountPasswordCapability answers the password capability for the caller
// and its declared profile. v1 GET /auth/account/capability and v2
// getAccountPasswordCapability both call it.
func (h *AuthHandler) AccountPasswordCapability(ctx context.Context, claims *auth.Claims, profileID string) (AccountPasswordCapabilityView, error) {
	allowed, err := h.passwordChangeAllowed(ctx, claims, profileID)
	if err != nil {
		return AccountPasswordCapabilityView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to verify the active profile")
	}
	if allowed && h.passwords != nil {
		allowed, err = h.passwords.PasswordChangeAvailable(ctx, claims.UserID)
		if err != nil {
			return AccountPasswordCapabilityView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load password capabilities")
		}
	} else {
		allowed = false
	}
	return AccountPasswordCapabilityView{
		Configured:              h.passwords != nil,
		ChangePassword:          allowed,
		RequiresCurrentPassword: true,
		MinimumPasswordLength:   auth.MinimumPasswordLength,
		MaximumPasswordBytes:    auth.MaximumPasswordBytes,
	}, nil
}

// AuthorizePasswordChange decides whether the caller may replace the account
// password before any body is read: the active profile must be the primary
// one (or an admin with no profile selected), and a password service must be
// wired. v1 POST /auth/account/password and v2 changePassword both call it,
// then ChangePassword.
func (h *AuthHandler) AuthorizePasswordChange(ctx context.Context, claims *auth.Claims, profileID string) error {
	allowed, err := h.passwordChangeAllowed(ctx, claims, profileID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to verify the active profile")
	}
	if !allowed {
		return apiError(http.StatusForbidden, "password_change_forbidden", "Changing the account password requires the account's primary profile")
	}
	if h.passwords == nil {
		return apiError(http.StatusServiceUnavailable, "password_change_unavailable", "Password changes are unavailable")
	}
	return nil
}

// ChangePassword replaces the account password after AuthorizePasswordChange
// allowed it. A rejected password names the member in Field so v2 renders it
// as a validation problem there.
func (h *AuthHandler) ChangePassword(ctx context.Context, claims *auth.Claims, currentPassword, newPassword string) error {
	if currentPassword == "" || newPassword == "" {
		return apiError(http.StatusBadRequest, "bad_request", "Current password and new password are required")
	}
	err := h.passwords.ChangePassword(ctx, claims.UserID, currentPassword, newPassword)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, auth.ErrCurrentPasswordInvalid):
		return &APIError{Status: http.StatusBadRequest, Code: "invalid_current_password", Message: "Current password is incorrect", Field: "current_password"}
	case errors.Is(err, auth.ErrPasswordTooShort):
		return &APIError{Status: http.StatusBadRequest, Code: "weak_password", Message: "Password must be at least 8 characters", Field: "new_password"}
	case errors.Is(err, auth.ErrPasswordTooLong):
		return &APIError{Status: http.StatusBadRequest, Code: "password_too_long", Message: "Password must be at most 72 bytes", Field: "new_password"}
	case errors.Is(err, auth.ErrPasswordLoginDisabled):
		return apiError(http.StatusConflict, "password_login_disabled", "This account does not use local password sign-in")
	}
	return apiError(http.StatusInternalServerError, "internal_error", "Failed to change password")
}

func (h *AuthHandler) passwordChangeAllowed(ctx context.Context, claims *auth.Claims, profileID string) (bool, error) {
	if claims == nil || claims.TokenType != auth.TokenTypeAccess || claims.SessionID == "" || claims.ImpersonatorUserID != nil {
		return false, nil
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return claims.Role == models.RoleAdmin, nil
	}
	if h.checkPrimaryProfile == nil {
		return false, nil
	}
	isPrimary, found, err := h.checkPrimaryProfile(ctx, claims.UserID, profileID)
	if err != nil {
		return false, err
	}
	return found && isPrimary, nil
}
