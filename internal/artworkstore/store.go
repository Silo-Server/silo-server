// Package artworkstore owns the storage-neutral contract for canonical artwork
// objects and the confined local filesystem implementation of it.
//
// Domain code (the image pipeline, the cache-job processor, revision GC, the
// reconciler) addresses objects by *logical key* only: a backend-independent,
// scheme-free relative path such as
// artwork/v1/objects/poster/ab/<revision>/w500.webp. A logical key never
// carries bucket identity, an adapter key prefix, a local mount path, or server
// identity, so copying a logical tree between roots, buckets, or backends
// preserves every catalog reference. Backend-private concerns — bucket,
// endpoint, credentials, physical prefix, URL-auth policy, filesystem root —
// stay inside the adapter that owns them.
//
// Objects are immutable: different bytes belong under a different key. Writes
// are therefore idempotent, and a pre-existing object counts as success only
// when its stored bytes hash to the same digest as the input.
package artworkstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"
)

// Store is the contract the artwork pipeline and lifecycle code depend on. It
// is deliberately small: everything an adapter needs beyond this (bucket names,
// prefix sweeps, public-URL policy) stays private to that adapter or lives on an
// optional interface.
type Store interface {
	// WriteImmutable stores data at key. Writing the same key twice with the
	// same bytes succeeds; writing different bytes to an existing key returns
	// ErrContentMismatch and leaves the stored object untouched.
	WriteImmutable(ctx context.Context, key string, data []byte, metadata ObjectMetadata) error

	// Open returns the object's metadata plus a streaming body. The caller
	// must close the returned Object. Missing objects return ErrNotFound.
	Open(ctx context.Context, key string) (*Object, error)

	// Stat returns object metadata without opening the body. Missing objects
	// return ErrNotFound.
	Stat(ctx context.Context, key string) (ObjectInfo, error)

	// Matches reports whether an object already exists at key with exactly the
	// given bytes. A missing object reports false with a nil error so callers
	// can treat "absent" and "stale" identically.
	Matches(ctx context.Context, key string, data []byte) (bool, error)

	// DeleteObjects removes every key and returns how many are now gone.
	// Already-absent keys count as deleted, matching the S3 batch-delete
	// semantics the revision GC checks against.
	DeleteObjects(ctx context.Context, keys []string) (int, error)

	// Probe verifies the store is present and writable. Startup and readiness
	// call it; a failure is an operational error, never a reason to fall back
	// to another backend.
	Probe(ctx context.Context) error

	// ListPage returns at most limit objects after cursor in lexical key order.
	// The opaque cursor is the last logical key returned; done reports EOF.
	ListPage(ctx context.Context, prefix, cursor string, limit int) ([]ObjectInfo, string, bool, error)

	// DeletePrefixMaintenance is reserved for inventory-proven cleanup of
	// legacy per-item directories. It rejects portable artwork/v1 prefixes;
	// ordinary lifecycle code must use DeleteObjects with exact keys.
	DeletePrefixMaintenance(ctx context.Context, prefix string) (int, error)
}

// CapacityProvider is implemented when the backend has a meaningful bounded
// local capacity. Object stores deliberately omit it because bucket quotas are
// provider-specific and generally unavailable from the S3 API.
type CapacityProvider interface {
	FreeSpaceBytes(ctx context.Context) (int64, error)
}

// ResolvedURL is a fetchable URL and the instant it stops working. ExpiresAt is
// nil for URLs that do not expire.
type ResolvedURL struct {
	URL       string
	ExpiresAt *time.Time
}

// ObjectMetadata carries write-time attributes a backend may persist alongside
// the bytes. It never carries credentials, source URLs, or catalog identity.
type ObjectMetadata struct {
	// MediaType is the media type of data, for example "image/webp". When
	// empty the store derives one from the key extension. The filesystem
	// store stores no sidecar metadata and always derives on read, so keys
	// must keep a truthful extension.
	MediaType string
}

// ObjectInfo describes a stored object without its bytes.
type ObjectInfo struct {
	// Key is the logical key that was requested.
	Key string
	// SizeBytes is the exact stored length.
	SizeBytes int64
	// MediaType is the media type to serve the object with, or empty when
	// the key extension is unknown.
	MediaType string
	// ETag is an HTTP entity tag *including* its quotes, ready to be written
	// to the response header verbatim. It is a strong validator: it changes
	// whenever the stored bytes could have changed.
	ETag string
	// ModTime is the store's last-modified timestamp, or the zero time when
	// the backend does not report one. It is store-copy local and is not part
	// of object identity.
	ModTime time.Time
}

// Object is an open store object. Close releases the body.
type Object struct {
	Info ObjectInfo
	Body io.ReadCloser
}

// Close releases the object body.
func (o *Object) Close() error {
	if o == nil || o.Body == nil {
		return nil
	}
	return o.Body.Close()
}

// ReadSeeker returns the body as an io.ReadSeeker when the backend supports
// seeking, which is what delivery needs to answer a range request with
// http.ServeContent. The filesystem store always does; a remote backend may
// not, and its caller streams the body instead.
func (o *Object) ReadSeeker() (io.ReadSeeker, bool) {
	if o == nil || o.Body == nil {
		return nil, false
	}
	seeker, ok := o.Body.(io.ReadSeeker)
	return seeker, ok
}

var (
	// ErrNotFound reports that no object exists at the key. Delivery treats
	// this as a clean miss rather than an error page.
	ErrNotFound = errors.New("artworkstore: object not found")

	// ErrInvalidKey reports a key that violates the logical-key grammar. It is
	// returned before any filesystem or network access happens.
	ErrInvalidKey = errors.New("artworkstore: invalid object key")

	// ErrContentMismatch reports an attempt to write different bytes to an
	// existing immutable key. The stored object is left untouched.
	ErrContentMismatch = errors.New("artworkstore: object already exists with different content")

	// ErrNotRegularFile reports a store entry that is not a plain object — a
	// symlink, directory, or device node where an object belongs. It means the
	// store is corrupt or was written by something other than Silo, so it is
	// surfaced instead of being silently followed or overwritten.
	ErrNotRegularFile = errors.New("artworkstore: store entry is not a regular file")
)

// Media types the artwork pipeline produces. Encoded output is WebP today;
// the others cover legacy objects and the upload surfaces.
const (
	mediaTypeWebP = "image/webp"
	mediaTypeJPEG = "image/jpeg"
)

// MediaTypeForKey derives the media type to serve a key with from its
// extension. The table is explicit rather than system-dependent so every node
// serves the same object identically. Unknown extensions return an empty
// string; the caller decides whether to omit the header or fall back to
// application/octet-stream.
func MediaTypeForKey(key string) string {
	switch strings.ToLower(path.Ext(key)) {
	case ".webp":
		return mediaTypeWebP
	case ".jpg", ".jpeg":
		return mediaTypeJPEG
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".avif":
		return "image/avif"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		// Branding favicons are stored unconverted so browsers that reject a
		// WebP favicon keep working.
		return "image/vnd.microsoft.icon"
	case ".json":
		return "application/json"
	default:
		return ""
	}
}

// hashBytes returns the lowercase hex SHA-256 of data.
func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hashReader streams r into SHA-256 so object comparison never buffers a whole
// stored object in memory.
func hashReader(r io.Reader) (string, error) {
	hasher := sha256.New()
	if _, err := io.Copy(hasher, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func validateLegacyMaintenancePrefix(prefix string) error {
	if prefix == "" || !strings.HasSuffix(prefix, "/") || strings.HasPrefix(prefix, "artwork/v1/") {
		return fmt.Errorf("%w: invalid legacy maintenance prefix %q", ErrInvalidKey, prefix)
	}
	return ValidateKey(prefix + "maintenance-probe.webp")
}

// entityTag builds a strong HTTP entity tag, quotes included. Stored objects
// are immutable, but legacy mutable keys (pre-existing per-row upload keys)
// still exist, so size and modification time participate alongside the key.
func entityTag(key string, size int64, mod time.Time) string {
	hasher := sha256.New()
	hasher.Write([]byte(key))
	hasher.Write([]byte{0})
	hasher.Write([]byte(strconv.FormatInt(size, 10)))
	hasher.Write([]byte{0})
	hasher.Write([]byte(strconv.FormatInt(mod.UTC().UnixNano(), 10)))
	return `"` + hex.EncodeToString(hasher.Sum(nil))[:32] + `"`
}
