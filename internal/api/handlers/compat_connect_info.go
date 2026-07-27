package handlers

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/jellycompat"
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
	Users        UserRepository
}

func NewCompatConnectInfoHandler(
	cfg *config.Config,
	settings ServerSettingsStore,
	users UserRepository,
) *CompatConnectInfoHandler {
	return &CompatConnectInfoHandler{Config: cfg, SettingsRepo: settings, Users: users}
}

type compatConnectAccountInfo struct {
	// PasswordLoginAvailable reports whether this account can authenticate
	// with a password at all. Compat login is hardwired to the local provider,
	// so SSO/plugin-provisioned accounts cannot sign in to a Jellyfin client
	// no matter what they type.
	PasswordLoginAvailable bool `json:"password_login_available"`
}

type compatConnectInfoResponse struct {
	Jellyfin jellycompat.ConnectInfo  `json:"jellyfin"`
	Account  compatConnectAccountInfo `json:"account"`
}

// HandleGetConnectInfo handles GET /compat/connect-info.
func (h *CompatConnectInfoHandler) HandleGetConnectInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	writeJSON(w, http.StatusOK, compatConnectInfoResponse{
		Jellyfin: jellycompat.ConnectInfoForConfig(h.Config, h.compatSettings(ctx)),
		Account: compatConnectAccountInfo{
			PasswordLoginAvailable: h.passwordLoginAvailable(ctx),
		},
	})
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

// passwordLoginAvailable reports whether the requesting account can use the
// password flow. It defaults to true when the user cannot be resolved: the page
// then shows its normal instructions rather than telling someone their working
// login is unsupported because of a lookup blip.
func (h *CompatConnectInfoHandler) passwordLoginAvailable(ctx context.Context) bool {
	if h.Users == nil {
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
	return user.LocalPasswordLoginEnabled
}
