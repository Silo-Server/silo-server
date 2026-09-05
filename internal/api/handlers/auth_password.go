package handlers

import (
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
	claims := apimw.GetClaims(r.Context())
	allowed, err := h.passwordChangeAllowed(r, claims)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to verify the active profile")
		return
	}
	if allowed && h.passwords != nil {
		allowed, err = h.passwords.PasswordChangeAvailable(r.Context(), claims.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load password capabilities")
			return
		}
	} else {
		allowed = false
	}

	writeJSON(w, http.StatusOK, accountPasswordCapabilityResponse{
		SchemaVersion:           1,
		ChangePassword:          allowed,
		RequiresCurrentPassword: true,
		MinimumPasswordLength:   auth.MinimumPasswordLength,
		MaximumPasswordBytes:    auth.MaximumPasswordBytes,
	})
}

// HandleChangePassword handles POST /auth/account/password. The password is
// account-wide, so only the active primary profile may replace it. An admin
// without a selected profile is also allowed, matching the app's acting-admin
// policy, but selecting a secondary profile removes that authority.
func (h *AuthHandler) HandleChangePassword(w http.ResponseWriter, r *http.Request) {
	claims := apimw.GetClaims(r.Context())
	allowed, err := h.passwordChangeAllowed(r, claims)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to verify the active profile")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "password_change_forbidden", "Changing the account password requires the account's primary profile")
		return
	}
	if h.passwords == nil {
		writeError(w, http.StatusServiceUnavailable, "password_change_unavailable", "Password changes are unavailable")
		return
	}

	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Current password and new password are required")
		return
	}

	err = h.passwords.ChangePassword(r.Context(), claims.UserID, req.CurrentPassword, req.NewPassword)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, auth.ErrCurrentPasswordInvalid):
		writeError(w, http.StatusBadRequest, "invalid_current_password", "Current password is incorrect")
	case errors.Is(err, auth.ErrPasswordTooShort):
		writeError(w, http.StatusBadRequest, "weak_password", "Password must be at least 8 characters")
	case errors.Is(err, auth.ErrPasswordTooLong):
		writeError(w, http.StatusBadRequest, "password_too_long", "Password must be at most 72 bytes")
	case errors.Is(err, auth.ErrPasswordLoginDisabled):
		writeError(w, http.StatusConflict, "password_login_disabled", "This account does not use local password sign-in")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to change password")
	}
}

func (h *AuthHandler) passwordChangeAllowed(r *http.Request, claims *auth.Claims) (bool, error) {
	if claims == nil || claims.TokenType != auth.TokenTypeAccess || claims.SessionID == "" || claims.ImpersonatorUserID != nil {
		return false, nil
	}

	profileID := strings.TrimSpace(apimw.GetProfileID(r.Context()))
	if profileID == "" {
		profileID = strings.TrimSpace(r.Header.Get("X-Profile-Id"))
	}
	if profileID == "" {
		return claims.Role == models.RoleAdmin, nil
	}
	if h.checkPrimaryProfile == nil {
		return false, nil
	}

	isPrimary, found, err := h.checkPrimaryProfile(r.Context(), claims.UserID, profileID)
	if err != nil {
		return false, err
	}
	return found && isPrimary, nil
}
