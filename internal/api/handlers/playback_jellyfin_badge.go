package handlers

import (
	"strings"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// jellyfinBadge is isJellyfinEcosystemClient for a live session row, which
// also reads the user agent. A play named after another app's session
// (playback.GrantedAppClientName) is never badged: the name is the server's,
// and the user agent beside it is the app's own choice. The name-only callers
// need no such check, since the reserved name holds no Jellyfin client token.
func jellyfinBadge(clientName, userAgent string) bool {
	if strings.TrimSpace(clientName) == playback.GrantedAppClientName {
		return false
	}
	return isJellyfinEcosystemClient(clientName, userAgent)
}
