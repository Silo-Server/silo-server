package config

import (
	"fmt"
	"net/url"
	"strings"
)

// DefaultThemeCatalogURL is the catalog fetched by the runtime when no
// override is stored. It is also part of the Admin settings effective-value
// contract, so the UI never has to duplicate or guess the active default.
const DefaultThemeCatalogURL = "https://raw.githubusercontent.com/Silo-Server/silo-themes/main/catalog.json"

var allowedThemeRemoteHosts = map[string]struct{}{
	"raw.githubusercontent.com":     {},
	"github.com":                    {},
	"objects.githubusercontent.com": {},
}

// ValidateThemeRemoteURL applies the shared allowlist used when accepting a
// catalog setting and when fetching theme resources. Keeping this contract in
// one package prevents the Admin UI from persisting a URL the runtime refuses.
func ValidateThemeRemoteURL(parsed *url.URL) error {
	if parsed == nil || parsed.Scheme != "https" || parsed.Host == "" {
		return url.InvalidHostError("theme URL must use HTTPS on an approved GitHub host")
	}
	if _, allowed := allowedThemeRemoteHosts[parsed.Hostname()]; !allowed {
		return url.InvalidHostError("theme URL must use HTTPS on an approved GitHub host")
	}
	return nil
}

func normalizeAdminThemeURL(key, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || ValidateThemeRemoteURL(parsed) != nil {
		return "", fmt.Errorf("%s must use HTTPS on an approved GitHub host", key)
	}
	return strings.TrimRight(value, "/"), nil
}
