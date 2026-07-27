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
	Enabled    bool   `json:"enabled"`
	PublicURL  string `json:"public_url"`
	ServerName string `json:"server_name"`
}

// ConnectInfoForConfig resolves the compat connection details using the same
// config-then-database precedence as WebComponentStatusForConfig, without
// touching the filesystem.
func ConnectInfoForConfig(cfg *config.Config, settings map[string]string) ConnectInfo {
	info := ConnectInfo{Enabled: compatEnabled(cfg, settings)}
	if cfg != nil {
		info.PublicURL = cfg.JellyfinCompat.PublicURL
		info.ServerName = cfg.JellyfinCompat.ServerName
	}
	info.PublicURL = stringSetting(settings, "jellyfin_compat.public_url", info.PublicURL)
	info.ServerName = stringSetting(settings, "jellyfin_compat.server_name", info.ServerName)
	return info
}

// compatEnabled reports whether the Jellyfin compatibility API is turned on,
// letting a stored server setting override the bootstrap config.
func compatEnabled(cfg *config.Config, settings map[string]string) bool {
	enabled := false
	if cfg != nil {
		enabled = cfg.JellyfinCompat.Enabled
	}
	if raw := strings.TrimSpace(settings["jellyfin_compat.enabled"]); raw != "" {
		enabled = strings.EqualFold(raw, "true") || raw == "1" || strings.EqualFold(raw, "yes")
	}
	return enabled
}
