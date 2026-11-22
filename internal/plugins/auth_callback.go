package plugins

import (
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
)

const (
	AuthCallbackInstallation = "installation"
	AuthCallbackStable       = "stable"
)

// OAuthCallbackPath is the callback route under an API prefix.
func OAuthCallbackPath(prefix string, installationID int, mode string) string {
	base := prefix + "/auth/oauth/"
	if mode == AuthCallbackStable {
		return base + "callback"
	}
	return base + strconv.Itoa(installationID) + "/callback"
}

// OAuthCallbackURL is the redirect URI on the configured public URL.
func OAuthCallbackURL(publicURL, prefix string, installationID int, mode string) string {
	publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if publicURL == "" {
		return ""
	}
	return publicURL + OAuthCallbackPath(prefix, installationID, mode)
}

// ValidateAuthCallbackMode accepts an explicit mode only for a capability
// that the login registry presents as OAuth.
func ValidateAuthCallbackMode(mode string, authModes []string) error {
	if mode == "" {
		return nil
	}
	if mode != AuthCallbackInstallation && mode != AuthCallbackStable {
		return &ConfigValidationError{Message: "callback mode must be installation or stable"}
	}
	if !slices.Contains(authModes, manifest.AuthModeOAuth2) ||
		slices.Contains(authModes, manifest.AuthModePassword) ||
		slices.Contains(authModes, manifest.AuthModeNetwork) {
		return &ConfigValidationError{Message: "callback mode is only available for an OAuth sign-in capability"}
	}
	return nil
}
