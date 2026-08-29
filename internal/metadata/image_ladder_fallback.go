package metadata

import (
	"context"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
)

const (
	// existsCacheTTL is how long a confirmed-present object stays confirmed.
	// Cached artwork is immutable per revision, so a hit cannot go stale except
	// by deletion, which the reconciler already repairs.
	existsCacheTTL = 24 * time.Hour
	// missingExistsCacheTTL is how long a confirmed-absent object stays absent.
	// It is short because absence is the transient state: the ladder backfill is
	// generating the rung right now, and every viewer is being served a narrower
	// image until it lands.
	missingExistsCacheTTL = 15 * time.Minute
)

// s3ImageExistenceChecker is the existence half of the S3 surface. The presigner
// is *s3client.Client in production, which implements it; a deployment whose
// presigner does not simply skips the fallback and presigns what was asked for.
type s3ImageExistenceChecker interface {
	ObjectExists(ctx context.Context, bucket, key string) (bool, error)
}

// ladderTypesWithAddedRung lists the image types that gained a rung in the
// current artworkkey.LadderVersion. Artwork cached by an older version has no
// object at the new rung until the ladder backfill regenerates it, so these —
// and only these — are worth an existence check before presigning. Every other
// rung has been generated for as long as the artwork has existed.
//
// The rung itself is not spelled out: at this version each of these types gained
// its widest variant, so the check reads it back off the ladder and cannot drift
// from artworkkey.VariantWidths. Revisit both this set and that assumption
// whenever LadderVersion is bumped.
var ladderTypesWithAddedRung = map[string]bool{
	ImageCacheImagePoster: true,
	ImageCacheImageStill:  true,
	ImageCacheImageLogo:   true,
}

// needsExistenceCheck reports whether a cached artwork key names a rung that
// might not have been generated yet, along with the image type governing its
// ladder.
func needsExistenceCheck(key string) (imageType string, ok bool) {
	imageType = catalog.ImageTypeFromCachedPath(key)
	if imageType == "" || !ladderTypesWithAddedRung[imageType] {
		return "", false
	}
	return imageType, imagesize.Variant(imageType, imagesize.Large) == keyVariant(key)
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

// ladderExistenceConcurrency bounds how many artwork existence checks run at
// once for one batch. It keeps a large browse page off a serial chain of HEADs
// without letting a single request open a hundred connections to storage.
const ladderExistenceConcurrency = 12

// ladderKey is the outcome of walking the ladder for one requested key.
type ladderKey struct {
	key      string
	fellBack bool
}

// resolveLadderKeys walks the ladder for a whole batch with bounded concurrency,
// returning the key to presign for each entry. Entries needing no check (every
// rung but the newly-added one, which is the overwhelming majority) resolve to
// themselves without touching storage.
func (r *PluginImageResolver) resolveLadderKeys(
	ctx context.Context,
	checker s3ImageExistenceChecker,
	bucket string,
	entries []resolveEntry,
) map[string]ladderKey {
	resolved := make(map[string]ladderKey, len(entries))
	for _, entry := range entries {
		resolved[entry.originalPath] = ladderKey{key: entry.originalPath}
	}
	if checker == nil {
		return resolved
	}

	// Only the entries that can actually miss are worth a goroutine.
	pending := make([]string, 0, len(entries))
	for _, entry := range entries {
		if _, ok := needsExistenceCheck(entry.originalPath); ok {
			pending = append(pending, entry.originalPath)
		}
	}
	if len(pending) == 0 {
		return resolved
	}

	var (
		mu    sync.Mutex
		group errgroup.Group
	)
	group.SetLimit(ladderExistenceConcurrency)
	for _, path := range pending {
		group.Go(func() error {
			key, fellBack := r.resolveLadderKey(ctx, checker, bucket, path)
			mu.Lock()
			resolved[path] = ladderKey{key: key, fellBack: fellBack}
			mu.Unlock()
			return nil
		})
	}
	// resolveLadderKey never returns an error — a failed check degrades to
	// presigning what was asked for — so there is nothing to surface here.
	_ = group.Wait()
	return resolved
}

// resolveLadderKey returns the key to presign for a requested rung, walking down
// the ladder when the requested one has not been generated yet. The second
// return reports whether the answer is a fallback rather than what was asked
// for, which the caller uses to shorten how long it caches the resolved URL.
//
// An existence check that errors is not treated as absence: storage being
// briefly unreachable must not permanently downgrade everyone's artwork, so the
// requested key is presigned optimistically and the result is not cached.
func (r *PluginImageResolver) resolveLadderKey(ctx context.Context, checker s3ImageExistenceChecker, bucket, key string) (string, bool) {
	imageType, ok := needsExistenceCheck(key)
	if !ok {
		return key, false
	}

	candidate := key
	for {
		exists, err := r.objectExists(ctx, checker, bucket, candidate)
		if err != nil {
			return key, false
		}
		if exists {
			return candidate, candidate != key
		}
		next, hasNext := imagesize.NextLower(imageType, keyVariant(candidate))
		if !hasNext {
			// The original is the terminal fallback: it predates every rung, so
			// checking it would spend a request to learn nothing actionable.
			return variantKey(key, artworkkey.OriginalVariant), true
		}
		candidate = variantKey(key, next)
	}
}

// objectExists answers from the existence cache when it can. Present and absent
// are cached with different lifetimes; an error is not cached at all.
func (r *PluginImageResolver) objectExists(ctx context.Context, checker s3ImageExistenceChecker, bucket, key string) (bool, error) {
	cacheKey := bucket + "\x00" + key
	if r.existsCache != nil {
		if value, ok := r.existsCache.Get(cacheKey); ok {
			return value, nil
		}
	}

	exists, err := checker.ObjectExists(ctx, bucket, key)
	if err != nil {
		slog.DebugContext(ctx, "artwork existence check failed", "component", "metadata", "key", key, "error", err)
		return false, err
	}

	if r.existsCache != nil {
		ttl := missingExistsCacheTTL
		if exists {
			ttl = existsCacheTTL
		}
		r.existsCache.Set(cacheKey, exists, ttl)
	}
	return exists, nil
}

// fallbackURLExpiry shortens a presigned URL's advertised validity when it
// points at a fallback rung, so the resolved-URL cache releases it about as soon
// as the missing rung could have been backfilled. Without this a viewer could
// keep receiving the narrow image for a full day after the real one landed.
func fallbackURLExpiry(expiry time.Time, now time.Time) time.Time {
	clamped := now.Add(missingExistsCacheTTL + resolvedURLCacheSafetyMargin)
	if clamped.Before(expiry) {
		return clamped
	}
	return expiry
}
