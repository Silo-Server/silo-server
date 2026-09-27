package mail

import (
	"context"
	"strings"

	"github.com/Silo-Server/silo-server/internal/branding"
)

// AccountLinkBase is the externally reachable origin that account emails
// (invitations, password resets) build their links on: the server.public_url
// setting, else fallback. Empty when neither is set; such links cannot be made.
func AccountLinkBase(ctx context.Context, settings SettingReader, fallback string) string {
	if settings != nil {
		if base, err := settings.Get(ctx, "server.public_url"); err == nil {
			if base = strings.TrimRight(strings.TrimSpace(base), "/"); base != "" {
				return base
			}
		}
	}
	return strings.TrimRight(fallback, "/")
}

// ServerName is the branded server name account emails and their landing
// screens show, defaulting to "Silo".
func ServerName(ctx context.Context, settings SettingReader) string {
	if settings != nil {
		if name, err := settings.Get(ctx, branding.KeyServerName); err == nil {
			if name = strings.TrimSpace(name); name != "" {
				return name
			}
		}
	}
	return branding.DefaultServerName
}
