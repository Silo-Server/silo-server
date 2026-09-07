package metadata

import (
	"context"
	"log/slog"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
)

// ArtworkAvailability is durable knowledge about one immutable revision.
// Verified distinguishes a completed delivery check (including no usable keys)
// from publication that has not yet been checked through external delivery.
type ArtworkAvailability struct {
	Published   []string
	Deliverable []string
	External    bool
	Verified    bool
}

type ArtworkAvailabilityReader interface {
	ArtworkAvailability(context.Context, []string) (map[string]ArtworkAvailability, error)
}

// These types gained their widest rung in the current ladder version. Legacy
// artwork without a manifest can safely use only an established lower rung.
var ladderTypesWithAddedRung = map[string]bool{
	ImageCacheImagePoster: true,
	ImageCacheImageStill:  true,
	ImageCacheImageLogo:   true,
}

// keyVariant extracts the variant name from a cached artwork key, e.g.
// "tmdb/movies/550/poster/w780.abc123.webp" -> "w780".
func keyVariant(key string) string {
	name := path.Base(strings.TrimSpace(key))
	name = strings.TrimSuffix(name, path.Ext(name))
	if dot := strings.IndexByte(name, '.'); dot >= 0 {
		name = name[:dot]
	}
	return name
}

// variantKey rebuilds a cached artwork key at a different rung, preserving the
// revision and extension.
func variantKey(key, variant string) string {
	dir := strings.TrimSuffix(artworkkey.Directory(key), "/")
	if dir == "" {
		return key
	}
	original := artworkkey.Original(dir, artworkkey.Revision(key), path.Ext(key))
	return artworkkey.Variant(original, variant)
}

// resolvePublishedLadderKeys performs one catalog lookup and local selection.
// Neither cold caches nor missing manifests cause storage or delivery probes.
func resolvePublishedLadderKeys(ctx context.Context, reader ArtworkAvailabilityReader, entries []resolveEntry) map[string]string {
	originals := make([]string, 0, len(entries))
	for _, entry := range entries {
		if catalog.ImageTypeFromCachedPath(entry.originalPath) != "" {
			originals = append(originals, variantKey(entry.originalPath, artworkkey.OriginalVariant))
		}
	}
	var states map[string]ArtworkAvailability
	if reader != nil && len(originals) > 0 {
		var err error
		states, err = reader.ArtworkAvailability(ctx, originals)
		if err != nil {
			slog.WarnContext(ctx, "artwork publication lookup failed; using established variants", "error", err)
		}
	}
	resolved := make(map[string]string, len(entries))
	for _, entry := range entries {
		key := entry.originalPath
		state, known := states[variantKey(key, artworkkey.OriginalVariant)]
		resolved[key] = selectPublishedVariant(key, state, known)
	}
	return resolved
}

func selectPublishedVariant(key string, state ArtworkAvailability, known bool) string {
	imageType := catalog.ImageTypeFromCachedPath(key)
	if imageType == "" {
		return key
	}
	if !known || (state.External && !state.Verified) {
		// An unverified external path is not proof the newly added size works.
		if ladderTypesWithAddedRung[imageType] && keyVariant(key) == imagesize.Variant(imageType, imagesize.Large) {
			if lower, ok := imagesize.NextLower(imageType, keyVariant(key)); ok {
				key = variantKey(key, lower)
			}
		}
		if !known {
			return key
		}
	}
	keys := state.Published
	if state.External && state.Verified {
		keys = state.Deliverable
	}
	candidate := key
	for {
		if slices.Contains(keys, candidate) {
			return candidate
		}
		lower, ok := imagesize.NextLower(imageType, keyVariant(candidate))
		if !ok {
			break
		}
		candidate = variantKey(key, lower)
	}
	original := variantKey(key, artworkkey.OriginalVariant)
	if slices.Contains(keys, original) {
		return original
	}
	// A known empty manifest or failed delivery is a placeholder, not a guess.
	return ""
}

func minURLExpiry(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
