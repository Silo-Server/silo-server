package handlers

import (
	"context"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
)

// ArtworkURLResolver mints a fetchable URL for a logical artwork key. It is
// satisfied by *artworkurl.Resolver, which is what decides — once, for every
// surface — whether the client fetches from the object store directly or
// through Silo's signed artwork route.
//
// Upload surfaces depend on this interface rather than on an S3 client so they
// work on both backends, and so their availability stops being a statement
// about which object store an operator happens to run.
type ArtworkURLResolver interface {
	ResolveArtworkURL(ctx context.Context, key string) (artworkstore.ResolvedURL, error)
}

type targetArtworkURLResolver interface {
	ResolveTargetURL(ctx context.Context, target artworkurl.Target, variant string) (artworkstore.ResolvedURL, error)
}

func resolveTargetStoredImageURL(ctx context.Context, resolver ArtworkURLResolver, target artworkurl.Target, path, variant string) string {
	path = strings.TrimSpace(path)
	switch {
	case path == "":
		return ""
	case strings.HasPrefix(path, "http://"), strings.HasPrefix(path, "https://"), strings.HasPrefix(path, "/"):
		return path
	case resolver == nil:
		return ""
	}
	targetResolver, ok := resolver.(targetArtworkURLResolver)
	if !ok {
		if variant != artworkkey.OriginalVariant {
			path = artworkkey.Variant(path, variant)
		}
		return resolveStoredImageURL(ctx, resolver, path)
	}
	target = target.WithReference(path)
	resolved, err := targetResolver.ResolveTargetURL(ctx, target, variant)
	if err != nil {
		slog.DebugContext(ctx, "resolving target artwork reference failed", "component", "api", "surface", target.Surface, "error", err)
		return ""
	}
	return resolved.URL
}

// resolveStoredImageURL turns a persisted image reference into something a
// client can load.
//
// Three kinds of value reach this function and only the third is a store key:
//
//   - an absolute http(s) URL, from a provider or a generated avatar service;
//   - an app-relative path such as "/images/collection-templates/x.webp",
//     served straight out of the frontend bundle;
//   - a logical artwork key, in either the portable artwork/v1 layout or a
//     legacy per-row upload layout that predates it.
//
// Failures return an empty string. A broken image is the right outcome for an
// object that is genuinely missing, and it is strictly better than emitting a
// URL that cannot work.
func resolveStoredImageURL(ctx context.Context, resolver ArtworkURLResolver, path string) string {
	path = strings.TrimSpace(path)
	switch {
	case path == "":
		return ""
	case strings.HasPrefix(path, "http://"), strings.HasPrefix(path, "https://"):
		return path
	case strings.HasPrefix(path, "/"):
		return path
	case resolver == nil:
		return ""
	}
	resolved, err := resolver.ResolveArtworkURL(ctx, path)
	if err != nil {
		slog.DebugContext(ctx, "resolving stored image reference failed",
			"component", "api", "path", path, "error", err)
		return ""
	}
	return resolved.URL
}

// resolveStoredCardImageURL resolves the card-sized variant of a stored image,
// falling back to whatever the reference itself names when it carries no
// variant ladder.
func resolveStoredCardImageURL(ctx context.Context, resolver ArtworkURLResolver, path string) string {
	return resolveStoredImageURL(ctx, resolver, cardThumbnailPath(path))
}
