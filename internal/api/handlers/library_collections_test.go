package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// keyEchoResolver resolves each artwork key to itself, so a test reads which
// variant was asked for.
type keyEchoResolver struct{}

func (keyEchoResolver) ResolveURLs(_ context.Context, keys []string) map[string]catalog.ResolvedImageURL {
	out := make(map[string]catalog.ResolvedImageURL, len(keys))
	for _, key := range keys {
		out[key] = catalog.ResolvedImageURL{URL: "resolved:" + key}
	}
	return out
}
