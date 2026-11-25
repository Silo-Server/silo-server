package handlers

import (
	"context"
	"net/http"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// AdminListAppKeys lists an account's app keys for an administrator.
func (h *AuthHandler) AdminListAppKeys(ctx context.Context, userID int) (auth.AdminAccountKeys, error) {
	return h.appKeys.AdminListKeys(ctx, userID)
}

// AdminRevokeAppDeviceKey revokes one of an account's device keys as the
// calling administrator.
func (h *AuthHandler) AdminRevokeAppDeviceKey(ctx context.Context, userID int, id string) error {
	actor, err := adminKeyActor(ctx)
	if err != nil {
		return err
	}
	return ownerError(h.appKeys.AdminRevokeDeviceKey(ctx, actor, userID, id))
}

// AdminRevokeAppPersonKey revokes an account's person key as the calling
// administrator.
func (h *AuthHandler) AdminRevokeAppPersonKey(ctx context.Context, userID int) error {
	actor, err := adminKeyActor(ctx)
	if err != nil {
		return err
	}
	return ownerError(h.appKeys.AdminRevokePersonKey(ctx, actor, userID))
}

// AdminDenyPendingAppKey refuses one of an account's waiting device keys as
// the calling administrator.
func (h *AuthHandler) AdminDenyPendingAppKey(ctx context.Context, userID int, id string) error {
	actor, err := adminKeyActor(ctx)
	if err != nil {
		return err
	}
	return ownerError(h.appKeys.AdminDenyPendingKey(ctx, actor, userID, id))
}

// adminKeyActor is the calling administrator's account, as the neighboring
// admin login-session and pre-approval operations read it.
func adminKeyActor(ctx context.Context) (int, error) {
	claims := apimw.GetClaims(ctx)
	if claims == nil || claims.Role != models.RoleAdmin {
		return 0, apiError(http.StatusForbidden, "forbidden", "Admin access required")
	}
	return claims.UserID, nil
}
