package jellycompat

import (
	"strings"

	"github.com/Silo-Server/silo-server/internal/config"
)

// ConnectInfo is the non-sensitive subset of the compat configuration a signed-in
// user needs in order to point a Jellyfin-protocol client at this server.
//
// It deliberately carries no install/filesystem detail: unlike the admin status
// endpoint, this is readable by every account, so it exposes only what a client
// would discover anyway by connecting to the compat listener.
type ConnectInfo struct {
	// Enabled reports whether a listener is actually accepting connections
	// right now, not what the stored setting asks for.
	Enabled bool `json:"enabled"`
	// PendingRestart reports that an administrator has changed the enabled
	// setting to something the running process has not adopted yet.
	PendingRestart bool   `json:"pending_restart"`
	PublicURL      string `json:"public_url"`
	ServerName     string `json:"server_name"`
}

// ConnectInfoForConfig resolves the compat connection details for an end user.
//
// Enabled comes from the boot-time config rather than the stored setting:
// jellyfin_compat.enabled is restart-required (see config.RestartRequiredKeys),
// and cmd/silo builds the compat server from the boot config alone. Reporting
// the stored value would promise credentials for a listener that does not exist
// yet, or claim the API is off while the running listener keeps serving. The
// address fields apply without a restart, so those do honour the override.
func ConnectInfoForConfig(cfg *config.Config, settings map[string]string) ConnectInfo {
	info := ConnectInfo{}
	if cfg != nil {
		info.Enabled = cfg.JellyfinCompat.Enabled
		info.PublicURL = cfg.JellyfinCompat.PublicURL
		info.ServerName = cfg.JellyfinCompat.ServerName
	}
	info.PendingRestart = configuredCompatEnabled(cfg, settings) != info.Enabled
	info.PublicURL = stringSetting(settings, "jellyfin_compat.public_url", info.PublicURL)
	info.ServerName = stringSetting(settings, "jellyfin_compat.server_name", info.ServerName)
	return info
}

// configuredCompatEnabled reports the *desired* compat state: the stored server
// setting if an administrator has set one, otherwise the bootstrap config. This
// is what the admin surface shows; it only matches the running listener once the
// server has been restarted.
func configuredCompatEnabled(cfg *config.Config, settings map[string]string) bool {
	enabled := false
	if cfg != nil {
		enabled = cfg.JellyfinCompat.Enabled
	}
	if raw := strings.TrimSpace(settings["jellyfin_compat.enabled"]); raw != "" {
		enabled = strings.EqualFold(raw, "true") || raw == "1" || strings.EqualFold(raw, "yes")
	}
	return enabled
}
