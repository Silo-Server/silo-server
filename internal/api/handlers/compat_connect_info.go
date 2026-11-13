package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/jellycompat"
	"github.com/Silo-Server/silo-server/internal/models"
)

// compatConnectInfoKeys are the only settings this endpoint consults. They are
// read individually rather than via GetAll so an authenticated page view never
// loads or decrypts unrelated secrets, and so a decryption failure elsewhere in
// the settings table cannot mask a valid compat override.
var compatConnectInfoKeys = []string{
	"jellyfin_compat.enabled",
	"jellyfin_compat.public_url",
	"jellyfin_compat.server_name",
}

// CompatConnectInfoHandler serves the account-facing view of the compatibility
// listeners: where to point a third-party client, and whether it is even on.
//
// The admin status endpoint covers the same ground for operators, but it also
// reports install paths and version provenance, so it stays admin-only. This
// handler exposes only the address a client would learn by connecting anyway.
type CompatConnectInfoHandler struct {
	Config       *config.Config
	SettingsRepo ServerSettingsStore
	Users        CompatConnectUsers
	SignIn       CompatPasswordSignIn
}

// CompatConnectUsers looks up the requesting account; *auth.UserRepository
// serves it.
type CompatConnectUsers interface {
	GetByID(ctx context.Context, id int) (*models.User, error)
}

// CompatPasswordSignIn answers whether an account can sign in with a password
// where no provider is named; *auth.Service serves it.
type CompatPasswordSignIn interface {
	PasswordSignInAvailable(ctx context.Context, user *models.User) (bool, error)
}

func NewCompatConnectInfoHandler(
	cfg *config.Config,
	settings ServerSettingsStore,
	users CompatConnectUsers,
	signIn CompatPasswordSignIn,
) *CompatConnectInfoHandler {
	return &CompatConnectInfoHandler{Config: cfg, SettingsRepo: settings, Users: users, SignIn: signIn}
}

type CompatConnectAccountInfo struct {
	// PasswordLoginAvailable reports whether this account can sign in to a
	// Jellyfin client with a password. Compat login routes it as any
	// password sign-in that names no provider: an account with its own
	// password sign-in uses its local password, which the server's switch
	// (unless break-glass) and a network provider's refusal can stop; any
	// other account uses its directory (LDAP) password when it is linked to
	// the enabled directory. An account with neither, such as one that only
	// signs in with OIDC, cannot sign in to a Jellyfin client.
	PasswordLoginAvailable bool `json:"password_login_available"`
}

type CompatConnectInfoResponse struct {
	Jellyfin jellycompat.ConnectInfo  `json:"jellyfin"`
	Account  CompatConnectAccountInfo `json:"account"`
}

// GetConnectInfo is the shared account-facing view used by both API versions.
func (h *CompatConnectInfoHandler) GetConnectInfo(ctx context.Context) CompatConnectInfoResponse {
	return CompatConnectInfoResponse{
		Jellyfin: jellycompat.ConnectInfoForConfig(h.Config, h.compatSettings(ctx)),
		Account: CompatConnectAccountInfo{
			PasswordLoginAvailable: h.passwordLoginAvailable(ctx),
		},
	}
}

// compatSettings loads the stored overrides this endpoint cares about. A
// missing store or a failed read is not fatal: the bootstrap config alone still
// describes a usable listener, so fall back to it rather than failing a purely
// informational read.
func (h *CompatConnectInfoHandler) compatSettings(ctx context.Context) map[string]string {
	if h.SettingsRepo == nil {
		return nil
	}
	settings := make(map[string]string, len(compatConnectInfoKeys))
	for _, key := range compatConnectInfoKeys {
		value, err := h.SettingsRepo.Get(ctx, key)
		if err != nil {
			continue
		}
		settings[key] = value
	}
	return settings
}

// passwordLoginAvailable asks the sign-in service whether the requesting
// account can sign in to a Jellyfin client with a password. It defaults to
// true when the account or the answer cannot be read: the page then shows its
// normal instructions rather than telling someone their working login is
// unsupported because of a lookup blip.
func (h *CompatConnectInfoHandler) passwordLoginAvailable(ctx context.Context) bool {
	if h.Users == nil || h.SignIn == nil {
		return true
	}
	userID := middleware.GetUserID(ctx)
	if userID == 0 {
		return true
	}
	user, err := h.Users.GetByID(ctx, userID)
	if err != nil || user == nil {
		return true
	}
	available, err := h.SignIn.PasswordSignInAvailable(ctx, user)
	return err != nil || available
}
