package metadata

import (
	"context"
	"errors"
	"log/slog"
	"path"
	"strings"
	"sync"
	"sync/atomic"
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
	// externalAvailabilityCacheTTL is shorter because a public/token delivery
	// path can stop serving an object independently of the backing S3 API.
	externalAvailabilityCacheTTL = 15 * time.Minute
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

// s3ImageDeliveryChecker verifies newly added ladder rungs through the
// separately authenticated URL path used by clients. External delivery is not
// equivalent to storage existence: an S3 HEAD can succeed while the public
// endpoint still returns 404.
type s3ImageDeliveryChecker interface {
	ObjectAvailable(ctx context.Context, bucket, key string) (bool, error)
	UsesExternalDelivery() bool
}

type imageAvailabilityCheck func(ctx context.Context, bucket, key string) (bool, error)

type imageAvailabilityPolicy struct {
	check           imageAvailabilityCheck
	presentTTL      time.Duration
	fallbackOnError bool
}

func availabilityPolicy(presigner s3ImagePresigner) imageAvailabilityPolicy {
	if checker, ok := presigner.(s3ImageDeliveryChecker); ok && checker.UsesExternalDelivery() {
		return imageAvailabilityPolicy{
			check:           checker.ObjectAvailable,
			presentTTL:      externalAvailabilityCacheTTL,
			fallbackOnError: true,
		}
	}
	if checker, ok := presigner.(s3ImageExistenceChecker); ok {
		return imageAvailabilityPolicy{
			check:      checker.ObjectExists,
			presentTTL: existsCacheTTL,
		}
	}
	return imageAvailabilityPolicy{}
}

var errExternalDeliveryBatchUnavailable = errors.New("external artwork delivery unavailable for batch")

// withBatchFailureCircuit stops a delivery outage from consuming one probe
// timeout per worker wave. Checks already in flight may finish, but after the
// first error no later entry in this batch reaches the external endpoint.
func (p imageAvailabilityPolicy) withBatchFailureCircuit() imageAvailabilityPolicy {
	if !p.fallbackOnError || p.check == nil {
		return p
	}

	check := p.check
	var failed atomic.Bool
	p.check = func(ctx context.Context, bucket, key string) (bool, error) {
		if failed.Load() {
			return false, errExternalDeliveryBatchUnavailable
		}
		exists, err := check(ctx, bucket, key)
		if err != nil && !failed.CompareAndSwap(false, true) {
			return false, errExternalDeliveryBatchUnavailable
		}
		return exists, err
	}
	return p
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
	ImageCacheImagePoster:   true,
	ImageCacheImageStill:    true,
	ImageCacheImageLogo:     true,
	ImageCacheImageBackdrop: true,
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

// ladderExistenceConcurrency bounds how many artwork availability checks run at
// once for one batch. It keeps a large browse page off a serial chain of probes
// without letting one request open a hundred storage or delivery connections.
const ladderExistenceConcurrency = 12

// ladderKey is the outcome of walking the ladder for one requested key.
type ladderKey struct {
	key             string
	fellBack        bool
	revalidateAfter time.Duration
}

// resolveLadderKeys walks the ladder for a whole batch with bounded concurrency,
// returning the key to presign for each entry. Entries needing no check (every
// rung but the newly-added one, which is the overwhelming majority) resolve to
// themselves without touching storage.
func (r *PluginImageResolver) resolveLadderKeys(
	ctx context.Context,
	policy imageAvailabilityPolicy,
	bucket string,
	entries []resolveEntry,
) map[string]ladderKey {
	resolved := make(map[string]ladderKey, len(entries))
	for _, entry := range entries {
		resolved[entry.originalPath] = ladderKey{key: entry.originalPath}
	}
	if policy.check == nil {
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
	policy = policy.withBatchFailureCircuit()

	var (
		mu    sync.Mutex
		group errgroup.Group
	)
	group.SetLimit(ladderExistenceConcurrency)
	for _, path := range pending {
		group.Go(func() error {
			key, fellBack := r.resolveLadderKey(ctx, policy, bucket, path)
			mu.Lock()
			resolved[path] = ladderKey{
				key:             key,
				fellBack:        fellBack,
				revalidateAfter: policy.presentTTL,
			}
			mu.Unlock()
			return nil
		})
	}
	// resolveLadderKey never returns an error — a failed check degrades according
	// to the selected storage or external-delivery policy.
	_ = group.Wait()
	return resolved
}

// resolveLadderKey returns the key to presign for a requested rung, walking down
// the ladder when the requested one has not been generated yet. The second
// return reports whether the answer is a fallback rather than what was asked
// for, which the caller uses to shorten how long it caches the resolved URL.
//
// A storage-only check that errors is not treated as absence: storage being
// briefly unreachable must not permanently downgrade everyone's artwork. An
// external-delivery error does fall back because it cannot prove that the URL
// handed to a client will work. Errors are never cached in either mode.
func (r *PluginImageResolver) resolveLadderKey(ctx context.Context, policy imageAvailabilityPolicy, bucket, key string) (string, bool) {
	imageType, ok := needsExistenceCheck(key)
	if !ok {
		return key, false
	}

	candidate := key
	for {
		exists, err := r.objectExists(ctx, policy.check, policy.presentTTL, bucket, candidate)
		if err != nil {
			if !policy.fallbackOnError {
				slog.DebugContext(ctx, "artwork existence check failed", "component", "metadata", "key", candidate, "error", err)
				return key, false
			}
			if !errors.Is(err, errExternalDeliveryBatchUnavailable) {
				slog.WarnContext(ctx, "external artwork delivery check failed; using lower rung", "component", "metadata", "key", candidate, "error", err)
			}
			next, hasNext := imagesize.NextLower(imageType, keyVariant(candidate))
			if !hasNext {
				return variantKey(key, artworkkey.OriginalVariant), true
			}
			// A delivery-wide error is likely to repeat for every candidate. The
			// lower rungs predate the current ladder version, so select the next
			// established one without multiplying timeout or rate-limit load.
			return variantKey(key, next), true
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

// objectExists answers from the availability cache when it can. Present and
// absent answers are cached with different lifetimes; an error is not cached.
func (r *PluginImageResolver) objectExists(
	ctx context.Context,
	check imageAvailabilityCheck,
	presentTTL time.Duration,
	bucket string,
	key string,
) (bool, error) {
	cacheKey := bucket + "\x00" + key
	if r.existsCache != nil {
		if value, ok := r.existsCache.Get(cacheKey); ok {
			return value, nil
		}
	}

	exists, err := check(ctx, bucket, key)
	if err != nil {
		return false, err
	}

	if r.existsCache != nil {
		ttl := missingExistsCacheTTL
		if exists {
			ttl = presentTTL
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
	return revalidatedURLExpiry(expiry, now, missingExistsCacheTTL)
}

// revalidatedURLExpiry makes the resolved-URL cache release an externally
// verified URL when its delivery-path availability is due to be checked again.
func revalidatedURLExpiry(expiry time.Time, now time.Time, ttl time.Duration) time.Time {
	clamped := now.Add(ttl + resolvedURLCacheSafetyMargin)
	if clamped.Before(expiry) {
		return clamped
	}
	return expiry
}
