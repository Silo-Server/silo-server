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
// Deliverable holds the latest verdict for the current delivery scope; while
// Verified is false it predates the current publication, so it proves which
// variants delivered but not which are missing.
type ArtworkAvailability struct {
	Published   []string
	Deliverable []string
	External    bool
	Verified    bool
}

type ArtworkAvailabilityReader interface {
	ArtworkAvailability(context.Context, []string) (map[string]ArtworkAvailability, error)
}

// These types' ladders have grown since revision manifests were introduced.
// Before external delivery is verified, their widest rung (the one the latest
// ladder version added) steps down one rung. Legacy artwork without a manifest
// was cached before any of that growth, so it may only use the rungs every
// ladder version has generated: up to the type's medium rung, which is pinned
// to the pre-image_size default.
var ladderTypesWithAddedRung = map[string]bool{
	ImageCacheImagePoster:   true,
	ImageCacheImageStill:    true,
	ImageCacheImageLogo:     true,
	ImageCacheImageBackdrop: true,
}

// keyVariant extracts the variant name from a cached artwork key, e.g.
// "tmdb/movies/550/poster/w500.abc123.webp" -> "w500".
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
// Without a reader every key resolves to itself.
//
// Where delivery cannot lag behind a write (local storage, direct S3), a
// published revision has every rung its ladder version generated, so only a
// rung a later ladder version added can be missing; only those keys are
// looked up, and everything else resolves to itself without a query.
func resolvePublishedLadderKeys(ctx context.Context, reader ArtworkAvailabilityReader, entries []resolveEntry) map[string]string {
	if reader == nil {
		resolved := make(map[string]string, len(entries))
		for _, entry := range entries {
			resolved[entry.originalPath] = entry.originalPath
		}
		return resolved
	}
	lookupAll := readsExternalDelivery(reader)
	originals := make([]string, 0, len(entries))
	for _, entry := range entries {
		if catalog.ImageTypeFromCachedPath(entry.originalPath) == "" {
			continue
		}
		if lookupAll || isAddedRung(entry.originalPath) {
			originals = append(originals, variantKey(entry.originalPath, artworkkey.OriginalVariant))
		}
	}
	var states map[string]ArtworkAvailability
	if len(originals) > 0 {
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

// readsExternalDelivery reports whether reader serves a backend whose delivery
// can lag behind a write. A reader that cannot say is treated as external, so
// every key is looked up.
func readsExternalDelivery(reader ArtworkAvailabilityReader) bool {
	if r, ok := reader.(interface{ ExternalDelivery() bool }); ok {
		return r.ExternalDelivery()
	}
	return true
}

// isAddedRung reports whether key names a rung wider than its type's medium
// rung on a type whose ladder grew: the rungs artwork cached under an earlier
// ladder version may lack. Medium is pinned to the pre-image_size default, so
// every ladder version generated it and everything narrower.
func isAddedRung(key string) bool {
	imageType := catalog.ImageTypeFromCachedPath(key)
	if !ladderTypesWithAddedRung[imageType] {
		return false
	}
	return clampToEstablishedRung(imageType, key) != key
}

func selectPublishedVariant(key string, state ArtworkAvailability, known bool) string {
	imageType := catalog.ImageTypeFromCachedPath(key)
	if imageType == "" {
		return key
	}
	if !known {
		if ladderTypesWithAddedRung[imageType] {
			key = clampToEstablishedRung(imageType, key)
		}
		return key
	}
	// An unverified external path is not proof the newly added size works,
	// unless an earlier verdict in this scope already delivered it.
	if state.External && !state.Verified && ladderTypesWithAddedRung[imageType] &&
		keyVariant(key) == imagesize.Variant(imageType, imagesize.Large) && !slices.Contains(state.Deliverable, key) {
		if lower, ok := imagesize.NextLower(imageType, keyVariant(key)); ok {
			key = variantKey(key, lower)
		}
	}
	keys := state.Published
	if state.External && state.Verified {
		keys = state.Deliverable
	}
	candidate := key
	if keyVariant(key) == artworkkey.OriginalVariant && !slices.Contains(keys, key) {
		candidate = variantKey(key, imagesize.Variant(imageType, imagesize.Large))
	}
	if published := widestPublishedWithin(key, candidate, keys); published != "" {
		return published
	}
	original := variantKey(key, artworkkey.OriginalVariant)
	if slices.Contains(keys, original) {
		return original
	}
	// A known empty manifest or failed delivery is a placeholder, not a guess.
	return ""
}

// widestPublishedWithin is the widest width rung of key's revision in keys that
// is no wider than candidate's. It can be a rung an earlier ladder version
// generated, such as a w780 poster the backfill has not regenerated at w1000
// yet. It is empty when keys hold no such rung.
func widestPublishedWithin(key, candidate string, keys []string) string {
	limit, ok := imagesize.VariantWidthPx(keyVariant(candidate))
	if !ok {
		return ""
	}
	best, bestWidth := "", 0
	for _, published := range keys {
		variant := keyVariant(published)
		width, ok := imagesize.VariantWidthPx(variant)
		if !ok || width > limit || width <= bestWidth || variantKey(key, variant) != published {
			continue
		}
		best, bestWidth = published, width
	}
	return best
}

// clampToEstablishedRung rewrites a width rung wider than the type's medium
// rung down to medium, the widest rung artwork cached before revision
// manifests carries. The original and narrower rungs pass through.
func clampToEstablishedRung(imageType, key string) string {
	width, ok := imagesize.VariantWidthPx(keyVariant(key))
	if !ok {
		return key
	}
	medium := imagesize.Variant(imageType, imagesize.Medium)
	if mediumWidth, ok := imagesize.VariantWidthPx(medium); ok && width > mediumWidth {
		return variantKey(key, medium)
	}
	return key
}

func minURLExpiry(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
