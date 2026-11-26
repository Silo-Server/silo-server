package handlers

import (
	"context"
	"errors"
	"net/http"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

// PreapproveDeviceLogin approves a sign-in in advance, as the caller's own
// login session, for the device holding the secret behind input.Binding.
// Only v2 preapproveDeviceLogin calls it.
func (h *AuthHandler) PreapproveDeviceLogin(ctx context.Context, input auth.DevicePreapprovalInput, userID int) (*auth.DevicePreapprovalResult, error) {
	if h.device == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Device login is not configured")
	}
	if userID == 0 {
		return nil, apiError(http.StatusUnauthorized, "unauthorized", "Authentication required")
	}
	if err := deviceDecisionSessionError(ctx); err != nil {
		return nil, err
	}
	// The device's session continues the approving session's provider chain.
	if claims := apimw.GetClaims(ctx); claims != nil {
		ctx = auth.WithClaims(ctx, claims)
	}
	result, err := h.device.Preapprove(ctx, input, userID)
	if err != nil {
		return nil, devicePreapprovalError(err)
	}
	return result, nil
}

// ListDevicePreapprovals lists the account's waiting pre-approved sign-ins,
// for the caller's own login session (auth.SessionAllows).
func (h *AuthHandler) ListDevicePreapprovals(ctx context.Context, userID int) ([]auth.DevicePreapproval, error) {
	if h.device == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Device login is not configured")
	}
	if claims := apimw.GetClaims(ctx); claims != nil {
		ctx = auth.WithClaims(ctx, claims)
	}
	items, err := h.device.ListPreapprovals(ctx, userID)
	if err != nil {
		return nil, devicePreapprovalError(err)
	}
	return items, nil
}

// RevokeDevicePreapproval withdraws one of the account's pre-approved
// sign-ins, for the caller's own login session (auth.SessionAllows).
func (h *AuthHandler) RevokeDevicePreapproval(ctx context.Context, userID int, id string) error {
	if h.device == nil {
		return apiError(http.StatusServiceUnavailable, "unavailable", "Device login is not configured")
	}
	if claims := apimw.GetClaims(ctx); claims != nil {
		ctx = auth.WithClaims(ctx, claims)
	}
	if err := h.device.RevokePreapproval(ctx, userID, id); err != nil {
		return devicePreapprovalError(err)
	}
	return nil
}

// AdminListDevicePreapprovals lists another account's waiting pre-approved
// sign-ins for an administrator.
func (h *AuthHandler) AdminListDevicePreapprovals(ctx context.Context, userID int) ([]auth.DevicePreapproval, error) {
	if h.device == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Device login is not configured")
	}
	items, err := h.device.AdminListPreapprovals(ctx, userID)
	if err != nil {
		return nil, devicePreapprovalError(err)
	}
	return items, nil
}

// AdminRevokeDevicePreapproval withdraws one of another account's
// pre-approved sign-ins as the calling administrator.
func (h *AuthHandler) AdminRevokeDevicePreapproval(ctx context.Context, userID int, id string) error {
	if h.device == nil {
		return apiError(http.StatusServiceUnavailable, "unavailable", "Device login is not configured")
	}
	claims := apimw.GetClaims(ctx)
	if claims == nil || claims.Role != models.RoleAdmin {
		return apiError(http.StatusForbidden, "forbidden", "Admin access required")
	}
	if err := h.device.AdminRevokePreapproval(ctx, claims.UserID, userID, id); err != nil {
		return devicePreapprovalError(err)
	}
	return nil
}

func devicePreapprovalError(err error) error {
	var out *APIError
	switch {
	case errors.Is(err, auth.ErrDevicePreapprovalLimit):
		out = apiError(http.StatusConflict, "preapproval_limit", "This account already has the most waiting sign-ins approved in advance; revoke one first")
	case errors.Is(err, auth.ErrDevicePreapprovalUnbound):
		out = fieldError("binding", "binding must be 43 characters of unpadded base64url")
	case errors.Is(err, auth.ErrNotPermitted):
		out = apiError(http.StatusForbidden, "not_permitted", "This sign-in's provider identity is gone; sign in again to approve a device")
	case errors.Is(err, auth.ErrNotFound):
		out = apiError(http.StatusNotFound, "not_found", "Account not found")
	case errors.Is(err, auth.ErrActingProfileChanged):
		out = apiError(http.StatusForbidden, "profile_verification_required", "The main profile changed; verify it again")
	case errors.Is(err, auth.ErrOwnerProtected), errors.Is(err, auth.ErrAdminProtected):
		return ownerError(err)
	default:
		return deviceDecisionError(err)
	}
	out.cause = err
	return out
}

// devicePollError maps the poll failures a pre-approved request adds.
func devicePollError(err error) *APIError {
	var attempts *auth.DevicePreapprovalAttemptsError
	switch {
	case errors.As(err, &attempts):
		e := apiError(http.StatusTooManyRequests, "rate_limited", "Too many wrong binding secrets for this sign-in. Try again later.")
		e.RetryAfter = ratelimit.RetryAfterSeconds(attempts.RetryAfter)
		return e
	case errors.Is(err, auth.ErrProviderUnavailable):
		e := apiError(http.StatusServiceUnavailable, "provider_unavailable", "The sign-in provider this device was approved through is unavailable; try again later")
		e.RetryAfter = 60
		return e
	}
	return nil
}
