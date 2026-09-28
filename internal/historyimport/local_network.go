package historyimport

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/netguard"
)

// SettingAllowPrivateDestinations lets every account import from, and sync
// with, media servers on the Silo server's own network. It is off by default:
// an address a user supplies is then limited to the public internet, because
// otherwise anyone who can sign in could make the server send requests to
// devices on its network. Admin accounts are never limited, and neither are
// servers an admin configured as import sources.
const SettingAllowPrivateDestinations = "media_servers.allow_private_destinations"

// Messages for a server address the outbound guard refused. They are safe to
// show the user who supplied the address.
const (
	PrivateAddressMessage = "That server address is on this server's local network. Use an address that works over the internet, or ask the server admin to allow local network servers."
	BlockedAddressMessage = "That server address can't be used."
)

// ServerAddressMessage returns the user-facing message for err when the
// outbound guard refused the server address behind it.
func ServerAddressMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, netguard.ErrBlockedDestination):
		return BlockedAddressMessage, true
	case errors.Is(err, netguard.ErrPrivateDestination):
		return PrivateAddressMessage, true
	default:
		return "", false
	}
}

// SettingReader reads live server settings. Satisfied by
// catalog.EncryptedSettingsRepo; declared locally to avoid a catalog
// dependency.
type SettingReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// LocalNetworkAccess decides whether a media server address a user supplied
// may be on the Silo server's own network. History import and webhook sync
// share one instance.
type LocalNetworkAccess struct {
	settings SettingReader
	repo     *Repository
}

// NewLocalNetworkAccess reads SettingAllowPrivateDestinations from settings
// and account roles through repo.
func NewLocalNetworkAccess(settings SettingReader, repo *Repository) *LocalNetworkAccess {
	return &LocalNetworkAccess{settings: settings, repo: repo}
}

// Allowed reports whether userID may use local network server addresses. It
// reads the setting and the account on every call, so a change applies to the
// next request or run, including runs already queued. A nil receiver or a
// failed read denies.
func (a *LocalNetworkAccess) Allowed(ctx context.Context, userID int) bool {
	if a == nil {
		return false
	}
	if a.settings != nil {
		value, err := a.settings.Get(ctx, SettingAllowPrivateDestinations)
		if err != nil {
			slog.WarnContext(ctx, "history import: reading local network setting failed", "error", err)
		} else if strings.EqualFold(strings.TrimSpace(value), "true") {
			return true
		}
	}
	if a.repo == nil || userID <= 0 {
		return false
	}
	admin, err := a.repo.isEnabledAdmin(ctx, userID)
	if err != nil {
		slog.WarnContext(ctx, "history import: reading account role failed", "user_id", userID, "error", err)
		return false
	}
	return admin
}

// Context returns ctx marked for local network access when userID is allowed
// it, for requests to a server that user supplied.
func (a *LocalNetworkAccess) Context(ctx context.Context, userID int) context.Context {
	if a.Allowed(ctx, userID) {
		return netguard.WithPrivateAccess(ctx)
	}
	return ctx
}

// CheckServerURL returns the context for requests to rawURL, a server address
// userID supplied, or the guard's refusal when the address is not allowed.
// It gives a clear error before anything is queued or stored; the transport
// still checks every connection the requests make.
func (a *LocalNetworkAccess) CheckServerURL(ctx context.Context, userID int, rawURL string) (context.Context, error) {
	ctx = a.Context(ctx, userID)
	if err := netguard.CheckURL(ctx, rawURL, netguard.PrivateAccess(ctx)); err != nil {
		return ctx, err
	}
	return ctx, nil
}

func (r *Repository) isEnabledAdmin(ctx context.Context, userID int) (bool, error) {
	var admin bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM users WHERE id = $1 AND role = $2 AND enabled)`,
		userID, models.RoleAdmin,
	).Scan(&admin)
	return admin, err
}

// privateNetworkProvider fetches with access to the Silo server's own network,
// for runs against a server an admin chose or a user trusted with it.
type privateNetworkProvider struct {
	Provider
}

func (p privateNetworkProvider) Fetch(ctx context.Context) ([]Record, []string, error) {
	return p.Provider.Fetch(netguard.WithPrivateAccess(ctx))
}
