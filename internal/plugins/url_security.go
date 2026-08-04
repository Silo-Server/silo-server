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

// validateProviderStreamURLSyntax validates only the structure of a provider
// stream URL — not whether it resolves to a public address. Used when the
// plugin admin has enabled allow_insecure_http for private/local hosts.
func validateProviderStreamURLSyntax(raw string) (string, error) {
	parsed, err := remotestream.ValidateURLSyntax(raw)
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}
