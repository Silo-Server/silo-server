package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
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
	SignIn       CompatPasswordSignIn
}

// CompatPasswordSignIn answers how the requesting account's password
// sign-in goes; *auth.Service serves it with the decision the
// compatibility sign-ins make.
type CompatPasswordSignIn interface {
	PasswordSignInFor(ctx context.Context, userID int) (auth.PasswordSignIn, error)
}

func NewCompatConnectInfoHandler(
	cfg *config.Config,
	settings ServerSettingsStore,
	signIn CompatPasswordSignIn,
) *CompatConnectInfoHandler {
	return &CompatConnectInfoHandler{Config: cfg, SettingsRepo: settings, SignIn: signIn}
}

type CompatConnectAccountInfo struct {
	// PasswordLoginAvailable reports whether this account's own Silo
	// password signs in: it has one, its own password sign-in is on, the
	// server's switch is on or the account is break-glass, and no network
	// provider refuses the person.
	PasswordLoginAvailable bool `json:"password_login_available"`
	// DirectoryLoginAvailable reports whether a password sign-in that names
	// no provider, as a Jellyfin client's does, goes to the server's
	// directory (LDAP) for this account: the directory username and
	// password sign in.
	DirectoryLoginAvailable bool `json:"directory_login_available"`
	// DirectoryUsername is the account's username at that directory; empty
	// when no directory identity is linked.
	DirectoryUsername string `json:"directory_username,omitempty"`
}

type CompatConnectInfoResponse struct {
	Jellyfin jellycompat.ConnectInfo  `json:"jellyfin"`
	Account  CompatConnectAccountInfo `json:"account"`
}

// GetConnectInfo is the shared account-facing view used by both API versions.
func (h *CompatConnectInfoHandler) GetConnectInfo(ctx context.Context) CompatConnectInfoResponse {
	return CompatConnectInfoResponse{
		Jellyfin: jellycompat.ConnectInfoForConfig(h.Config, h.compatSettings(ctx)),
		Account:  h.accountInfo(ctx),
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

// accountInfo reports how the requesting account signs in with a password.
// It shows the password instructions when the account or its sign-in
// cannot be read: the page then shows its normal instructions rather than
// telling someone their working login is unsupported because of a lookup
// blip.
func (h *CompatConnectInfoHandler) accountInfo(ctx context.Context) CompatConnectAccountInfo {
	fallback := CompatConnectAccountInfo{PasswordLoginAvailable: true}
	if h.SignIn == nil {
		return fallback
	}
	userID := middleware.GetUserID(ctx)
	if userID == 0 {
		return fallback
	}
	signIn, err := h.SignIn.PasswordSignInFor(ctx, userID)
	if err != nil {
		return fallback
	}
	return CompatConnectAccountInfo{
		PasswordLoginAvailable:  signIn.Local,
		DirectoryLoginAvailable: signIn.Directory,
		DirectoryUsername:       signIn.DirectoryUsername,
	}
}
