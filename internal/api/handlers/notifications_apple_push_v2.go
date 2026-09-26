package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

type applePushFinalizer interface {
	OrderedAppleAvailable() bool
	ApplyApplePushAndFinalize(context.Context, notifications.ApplePushCommand, func(notifications.ApplePushReceipt) error) (notifications.ApplePushReceipt, error)
}
type OrderedApplePushV2 struct {
	store    applePushFinalizer
	validate EventsSocketValidator
	issuer   ApplePushDisplayTokenIssuer
}

func NewOrderedApplePushV2(store *notifications.PushDeviceService, issuer ApplePushDisplayTokenIssuer, sessions eventsSessionValidator, users access.UserRepository, resolver apimw.ViewerResolver, primary apimw.PrimaryProfileChecker) *OrderedApplePushV2 {
	if jwt, ok := issuer.(*auth.JWTService); ok && jwt == nil {
		issuer = nil
	}
	return &OrderedApplePushV2{store: store, issuer: issuer, validate: newSocketAuthorityValidator(sessions, users, resolver, primary)}
}
func (h *OrderedApplePushV2) OrderedApplePushAvailable() bool {
	return h != nil && h.store != nil && h.store.OrderedAppleAvailable() && h.validate != nil && h.issuer != nil
}
func (h *OrderedApplePushV2) RegisterApplePush(ctx context.Context, cmd notifications.ApplePushCommand, identity evt.SocketIdentity) (notifications.ApplePushReceipt, string, time.Time, error) {
	empty := notifications.ApplePushReceipt{}
	if !h.OrderedApplePushAvailable() {
		return empty, "", time.Time{}, notifications.ErrPushDeviceUnavailable
	}
	if cmd.UserID != identity.UserID || cmd.ProfileID != identity.ProfileID || identity.ProfileID == "" {
		return empty, "", time.Time{}, apiError(http.StatusForbidden, "forbidden", "Current login authority is required")
	}
	validated, effective, err := h.validate(ctx, identity)
	if err != nil {
		return empty, "", time.Time{}, apiError(http.StatusForbidden, "forbidden", "Current login authority is required")
	}
	scope, ok := access.GetScope(validated)
	if !ok {
		return empty, "", time.Time{}, apiError(http.StatusForbidden, "forbidden", "Current profile authority is required")
	}
	identity.AccessFingerprint = eventsScopeFingerprint(scope)
	identity.EffectiveRole = effective.Role
	var token string
	var expiry time.Time
	receipt, err := h.store.ApplyApplePushAndFinalize(ctx, cmd, func(current notifications.ApplePushReceipt) error {
		// Revalidate after any database lock wait, before minting under the row locks.
		_, claims, e := h.validate(ctx, identity)
		if e != nil {
			return apiError(http.StatusForbidden, "forbidden", "Current login authority is required")
		}
		if current.Removed || !current.Enabled {
			return nil
		}
		token, expiry, e = h.issuer.GenerateApplePushDisplayToken(identity.UserID, claims.Role, identity.SessionID, identity.ProfileID, identity.ImpersonatorUserID)
		if e != nil {
			token = ""
			expiry = time.Time{}
		} // Registration still succeeds; existing access-token fallback remains.
		return nil
	})
	if err != nil {
		return empty, "", time.Time{}, err
	}
	return receipt, token, expiry, nil
}
