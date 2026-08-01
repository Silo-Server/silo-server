package plugins

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/remotestream"
)

// validateProviderStreamURL accepts only externally reachable HTTP(S) URLs.
// Plugin output is fetched by ffprobe/FFmpeg from the server, so accepting a
// loopback, link-local, private, or otherwise non-global destination would
// turn a compromised plugin into an SSRF primitive.
func validateProviderStreamURL(ctx context.Context, raw string) (string, error) {
	validated, err := remotestream.ValidateURL(ctx, raw)
	if err != nil {
		return "", err
	}
	return validated.String(), nil
}
