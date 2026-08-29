// Package imagecache downloads images from URLs, generates sized variants,
// computes thumbhashes, and writes every variant to the canonical artwork
// store.
//
// Materialized artwork is content-addressed: the produced variant set decides
// the logical keys, so the store never learns which provider, item, library, or
// server the image came from, and re-encoding the same bytes converges on the
// same immutable revision instead of mutating one. See
// internal/artworkkey/portable.go for the format.
package imagecache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkadopt"
	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkmetrics"
	"github.com/Silo-Server/silo-server/internal/artworksource"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/imageutil"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

const (
	sourceGenerated = "generated"
	urlSchemeHTTP   = "http"
	urlSchemeHTTPS  = "https"
)

// ObjectStore is the subset of artworkstore.Store the image pipeline needs:
// write an immutable object, and ask whether one already holds exactly these
// bytes. Every artworkstore backend satisfies it, and the pipeline stays free
// of buckets, roots, prefixes, and URL policy.
type ObjectStore interface {
	WriteImmutable(ctx context.Context, key string, data []byte, meta artworkstore.ObjectMetadata) error
	Matches(ctx context.Context, key string, data []byte) (bool, error)
}

// ArtworkRevisionTracker persists the exact object manifest for an immutable
// revision before any object is uploaded.
type ArtworkRevisionTracker interface {
	TrackArtworkRevision(ctx context.Context, originalPath, imageType string, objectKeys []string) error
	RecordArtworkRevision(ctx context.Context, originalPath, sourceClass string, objects []artworkstore.ObjectInfo) error
}

// ImageURLResolver resolves plugin:// paths to HTTP URLs.
type ImageURLResolver interface {
	ResolveImageURL(ctx context.Context, path string, variant string) string
}

// CacheRequest describes a single image to cache. The identity fields describe
// what is being cached and travel into logs and error messages; they no longer
// decide where it is stored, because the portable layout addresses artwork by
// the bytes it produced.
type CacheRequest struct {
	SourceURL       string
	SourceReference string // stable provider/plugin path before URL resolution
	ProviderID      string
	ContentType     string // "movies" or "series"
	ContentID       string
	ImageType       metadata.ImageType
	SeasonNumber    *int
	EpisodeNumber   *int
	Language        string
	ImageResolver   ImageURLResolver // optional; used when SourceURL is a plugin:// path
	// KeyDiscriminator carries the local sidecar's content hash.
	//
	// It no longer affects the object key: content addressing already rotates
	// a sidecar's key whenever its bytes change, which is exactly what the
	// discriminator was for. Callers still compute it, and it stays here as
	// the caller's record of which sidecar revision it read.
	KeyDiscriminator     string
	GeneratorVersion     string
	InputObjectRevisions []string
}

// CacheResult is returned by Cache on success.
type CacheResult struct {
	BasePath         string // revision directory holding every variant and the manifest
	OriginalPath     string // exact immutable original-variant object key
	Revision         string // content revision shared by generated variants
	ManifestPath     string // completeness marker written after every variant
	VariantPaths     map[string]string
	Thumbhash        string // base64-encoded
	Ext              string // file extension including dot (e.g. ".jpg", ".png")
	UploadedVariants int
	ExistingVariants int
}

// Cacher downloads images and materializes them into the canonical artwork
// store.
type Cacher struct {
	store             ObjectStore
	revisionTracker   ArtworkRevisionTracker
	httpClient        *http.Client
	enforcePublicURLs bool
}

// New creates a Cacher that materializes artwork through the given store.
func New(store ObjectStore) *Cacher {
	return &Cacher{store: store, httpClient: artworksource.NewSecureHTTPClient(), enforcePublicURLs: true}
}

// SetArtworkRevisionTracker wires durable revision lifecycle tracking. The
// production server configures this whenever artwork storage is available.
func (c *Cacher) SetArtworkRevisionTracker(tracker ArtworkRevisionTracker) {
	if c != nil {
		c.revisionTracker = tracker
	}
}

func newWithHTTPClient(store ObjectStore, client *http.Client) *Cacher {
	if client == nil {
		client = http.DefaultClient
	}
	return &Cacher{store: store, httpClient: client}
}

// CacheImage implements metadata.ImageCacher using the internal Cache method.
func (c *Cacher) CacheImage(ctx context.Context, req metadata.CacheImageRequest) (*metadata.CacheImageResult, error) {
	result, err := c.Cache(ctx, CacheRequest{
		SourceURL:            req.SourceURL,
		SourceReference:      req.SourceReference,
		ProviderID:           req.ProviderID,
		ContentType:          req.ContentType,
		ContentID:            req.ContentID,
		ImageType:            req.ImageType,
		SeasonNumber:         req.SeasonNumber,
		EpisodeNumber:        req.EpisodeNumber,
		Language:             req.Language,
		GeneratorVersion:     req.GeneratorVersion,
		InputObjectRevisions: req.InputObjectRevisions,
	})
	if err != nil {
		return nil, err
	}
	return cacheImageResultFromCacheResult(result), nil
}

// CacheImageBytes implements metadata.ImageByteCacher using CacheBytes. Used
// by the image cache processor for file:// sources that it reads itself.
func (c *Cacher) CacheImageBytes(ctx context.Context, data []byte, req metadata.CacheImageRequest) (*metadata.CacheImageResult, error) {
	result, err := c.CacheBytes(ctx, data, CacheRequest{
		SourceURL:            req.SourceURL,
		ProviderID:           req.ProviderID,
		ContentType:          req.ContentType,
		ContentID:            req.ContentID,
		ImageType:            req.ImageType,
		SeasonNumber:         req.SeasonNumber,
		EpisodeNumber:        req.EpisodeNumber,
		Language:             req.Language,
		KeyDiscriminator:     req.KeyDiscriminator,
		GeneratorVersion:     req.GeneratorVersion,
		InputObjectRevisions: req.InputObjectRevisions,
	})
	if err != nil {
		return nil, err
	}
	return cacheImageResultFromCacheResult(result), nil
}

func cacheImageResultFromCacheResult(result *CacheResult) *metadata.CacheImageResult {
	return &metadata.CacheImageResult{
		BasePath:         result.BasePath,
		OriginalPath:     result.OriginalPath,
		Revision:         result.Revision,
		Thumbhash:        result.Thumbhash,
		Ext:              result.Ext,
		UploadedVariants: result.UploadedVariants,
		ExistingVariants: result.ExistingVariants,
	}
}

// CacheAudiobookCover is a thin convenience over CacheBytes specifically
// for the audiobook scanner. Avoids exporting the imagecache request
// struct to the scanner package (which would create an import cycle
// scanner -> imagecache -> metadata -> scanner).
func (c *Cacher) CacheAudiobookCover(ctx context.Context, data []byte, contentID string) (storedPath string, thumbhash string, err error) {
	res, err := c.CacheBytes(ctx, data, CacheRequest{
		ProviderID:  "local",
		ContentType: "audiobooks",
		ContentID:   contentID,
		ImageType:   metadata.ImagePoster,
	})
	if err != nil {
		return "", "", err
	}
	return res.OriginalPath, res.Thumbhash, nil
}

// CacheEbookCover stores an embedded ebook cover using the same poster
// variants as provider-hosted book artwork.
func (c *Cacher) CacheEbookCover(ctx context.Context, data []byte, contentID string) (storedPath string, thumbhash string, err error) {
	res, err := c.CacheBytes(ctx, data, CacheRequest{
		ProviderID:  "local",
		ContentType: "ebooks",
		ContentID:   contentID,
		ImageType:   metadata.ImagePoster,
	})
	if err != nil {
		return "", "", err
	}
	return res.OriginalPath, res.Thumbhash, nil
}

// validateCacheRequest checks the required identity fields and the
// episode/season invariant shared by Cache and CacheBytes.
//
// None of these fields reach the object key any more, but a request that
// cannot say what it is caching is a caller bug worth failing on rather than
// materializing anonymous artwork nothing will ever publish.
func validateCacheRequest(req CacheRequest) error {
	if strings.TrimSpace(req.ProviderID) == "" {
		return fmt.Errorf("imagecache: provider ID is required")
	}
	if strings.TrimSpace(req.ContentType) == "" {
		return fmt.Errorf("imagecache: content type is required")
	}
	if strings.TrimSpace(req.ContentID) == "" {
		return fmt.Errorf("imagecache: content ID is required")
	}
	if req.EpisodeNumber != nil && req.SeasonNumber == nil {
		return fmt.Errorf("imagecache: episode number requires a season number")
	}
	return nil
}

// CacheBytes performs the same variant generation, thumbhash, and storage as
// Cache but starts from raw image bytes already in hand. Used by the audiobook
// and ebook scanners to materialize embedded cover art without round-tripping
// through HTTP.
//
// The write order is load-bearing:
//
//  1. produce every variant, then address the revision from those bytes;
//  2. register the complete object set for garbage collection *before* the
//     first byte is written, so a crash mid-write leaves reclaimable objects
//     rather than orphans;
//  3. write the image objects;
//  4. write manifest.json last, once every image object is durable, so a
//     manifest's presence means the revision is complete.
//
// Publication — pointing a catalog row at OriginalPath — happens after this
// returns, in the caller that owns the target row.
func (c *Cacher) CacheBytes(ctx context.Context, data []byte, req CacheRequest) (*CacheResult, error) {
	class := artworkSourceClass(req)
	var fingerprint string
	if class == sourceGenerated {
		fingerprint, _ = artworkkey.GeneratedSourceFingerprint(req.GeneratorVersion, req.InputObjectRevisions)
	} else {
		fingerprint, _ = artworkkey.ByteSourceFingerprint(class, data)
	}
	return c.cacheBytes(ctx, data, req, fingerprint)
}

func (c *Cacher) cacheBytes(ctx context.Context, data []byte, req CacheRequest, fingerprint string) (*CacheResult, error) {
	if err := validateCacheRequest(req); err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("imagecache: image data is empty")
	}
	imageType := metadata.ImageTypeToString(req.ImageType)
	if adopted, ok := c.tryAdopt(ctx, fingerprint, imageType, artworkSourceClass(req)); ok {
		return adopted, nil
	}
	thumbhash, err := imageutil.Thumbhash(data)
	if err != nil {
		return nil, fmt.Errorf("imagecache: thumbhash: %w", err)
	}
	result, err := imageutil.GenerateVariants(data, artworkkey.VariantWidths(imageType))
	if err != nil {
		return nil, fmt.Errorf("imagecache: generate variants: %w", err)
	}
	revision, err := buildRevision(imageType, result)
	if err != nil {
		return nil, err
	}
	if err := c.trackRevision(ctx, imageType, revision); err != nil {
		return nil, err
	}

	writeStats, err := c.writeVariants(ctx, revision, result)
	if err != nil {
		return nil, err
	}
	manifestMediaType := artworkstore.MediaTypeForKey(artworkkey.ManifestName)
	if err := c.writeObject(ctx, revision.ManifestKey, revision.ManifestJSON, manifestMediaType); err != nil {
		return nil, err
	}
	if err := c.recordRevision(ctx, revision, result, artworkSourceClass(req)); err != nil {
		return nil, err
	}
	c.writeAdoptionIndex(ctx, fingerprint, revision)
	artworkmetrics.Materialization(artworkSourceClass(req), "materialized")
	return &CacheResult{
		BasePath:         revision.Directory,
		OriginalPath:     revision.OriginalKey,
		Revision:         revision.Revision,
		ManifestPath:     revision.ManifestKey,
		VariantPaths:     revision.VariantKeys,
		Thumbhash:        thumbhash,
		Ext:              revision.Ext,
		UploadedVariants: writeStats.uploaded,
		ExistingVariants: writeStats.existing,
	}, nil
}

// Cache downloads the image at req.SourceURL and stores it through the same
// variant, revision-tracking, and upload pipeline as CacheBytes.
func (c *Cacher) Cache(ctx context.Context, req CacheRequest) (*CacheResult, error) {
	if err := validateCacheRequest(req); err != nil {
		return nil, err
	}

	imageType := metadata.ImageTypeToString(req.ImageType)
	sourceReference := req.SourceReference
	if strings.TrimSpace(sourceReference) == "" {
		sourceReference = req.SourceURL
	}
	fingerprint := stablePluginSourceFingerprint(sourceReference)
	if adopted, ok := c.tryAdopt(ctx, fingerprint, imageType, artworkSourceClass(req)); ok {
		return adopted, nil
	}

	url := req.SourceURL

	// Resolve non-HTTP paths (e.g. plugin_id://path) via the resolver.
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		if req.ImageResolver == nil {
			return nil, fmt.Errorf("imagecache: non-HTTP URL %q requires ImageResolver", url)
		}
		url = req.ImageResolver.ResolveImageURL(ctx, url, "original")
		if url == "" {
			return nil, fmt.Errorf("imagecache: resolver returned empty URL for %q", req.SourceURL)
		}
	}

	data, err := c.downloadImage(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("imagecache: download %s: %w", url, err)
	}

	return c.cacheBytes(ctx, data, req, fingerprint)
}

func stablePluginSourceFingerprint(source string) string {
	parsed, err := url.Parse(strings.TrimSpace(source))
	if err != nil || parsed.Scheme == "" {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case urlSchemeHTTP, urlSchemeHTTPS, "file", "embedded", sourceGenerated, "upload", "library-artwork":
		return ""
	}
	fingerprint, _ := artworkkey.SourceFingerprint("provider", source)
	return fingerprint
}

func (c *Cacher) tryAdopt(ctx context.Context, fingerprint, imageType, sourceClass string) (*CacheResult, bool) {
	store, ok := c.store.(artworkadopt.Store)
	if !ok || fingerprint == "" {
		return nil, false
	}
	adopted, ok := artworkadopt.Try(ctx, store, fingerprint, imageType)
	if !ok {
		return nil, false
	}
	if c.revisionTracker != nil {
		if err := c.revisionTracker.TrackArtworkRevision(ctx, adopted.OriginalKey, imageType, adopted.Manifest.ObjectKeys()); err != nil {
			return nil, false
		}
		if err := c.revisionTracker.RecordArtworkRevision(ctx, adopted.OriginalKey, sourceClass, adopted.Objects); err != nil {
			return nil, false
		}
	}
	thumbhash, err := imageutil.Thumbhash(adopted.OriginalData)
	if err != nil {
		return nil, false
	}
	artworkmetrics.Materialization(sourceClass, "adopted")
	variantPaths := make(map[string]string, len(adopted.Manifest.Variants))
	ext := ""
	for _, variant := range adopted.Manifest.Variants {
		key := adopted.Manifest.Directory() + "/" + variant.Filename
		variantPaths[variant.Name] = key
		if variant.Name == artworkkey.OriginalVariant {
			if dot := strings.LastIndex(variant.Filename, "."); dot >= 0 {
				ext = variant.Filename[dot:]
			}
		}
	}
	return &CacheResult{
		BasePath: adopted.Manifest.Directory(), OriginalPath: adopted.OriginalKey,
		Revision: adopted.Manifest.Revision, ManifestPath: adopted.Manifest.Directory() + "/" + artworkkey.ManifestName,
		VariantPaths: variantPaths, Thumbhash: thumbhash, Ext: ext,
		ExistingVariants: len(adopted.Manifest.Variants),
	}, true
}

func (c *Cacher) writeAdoptionIndex(ctx context.Context, fingerprint string, revision *artworkkey.PortableRevision) {
	store, ok := c.store.(artworkadopt.Store)
	if !ok || fingerprint == "" {
		return
	}
	if err := artworkadopt.WriteIndex(ctx, store, fingerprint, revision.Manifest, revision.ManifestJSON); err != nil {
		slog.WarnContext(ctx, "artwork adoption index write failed", "component", "artwork", "error", err)
	}
}

// buildRevision addresses the produced variant set: the revision digest, every
// logical key, and the canonical manifest bytes.
func buildRevision(imageType string, result *imageutil.VariantResult) (*artworkkey.PortableRevision, error) {
	variants := make([]artworkkey.VariantBytes, 0, len(result.Variants))
	for _, variant := range result.Variants {
		variants = append(variants, artworkkey.VariantBytes{Name: variant.Key, Data: variant.Data})
	}
	mediaType := artworkstore.MediaTypeForKey(artworkkey.OriginalVariant + result.Ext)
	revision, err := artworkkey.BuildPortableRevision(artworkkey.RevisionInput{
		ImageType: imageType,
		MediaType: mediaType,
		Ext:       result.Ext,
		Variants:  variants,
	})
	if err != nil {
		return nil, fmt.Errorf("imagecache: address revision: %w", err)
	}
	return revision, nil
}

type writeVariantStats struct {
	uploaded int
	existing int
}

func (c *Cacher) writeVariants(ctx context.Context, revision *artworkkey.PortableRevision, result *imageutil.VariantResult) (writeVariantStats, error) {
	var wg sync.WaitGroup
	writeErrs := make([]error, len(result.Variants))
	stats := make([]writeVariantStats, len(result.Variants))
	for i, v := range result.Variants {
		wg.Add(1)
		go func(idx int, variant imageutil.Variant) {
			defer wg.Done()
			key := revision.VariantKeys[variant.Key]
			exists, err := c.store.Matches(ctx, key, variant.Data)
			if err != nil {
				writeErrs[idx] = fmt.Errorf("imagecache: check existing %s: %w", key, err)
				return
			}
			if exists {
				stats[idx].existing = 1
				artworkmetrics.Variant("matched", int64(len(variant.Data)))
				return
			}
			if err := c.writeObject(ctx, key, variant.Data, revision.MediaType); err != nil {
				writeErrs[idx] = err
				return
			}
			stats[idx].uploaded = 1
			artworkmetrics.Variant("written", int64(len(variant.Data)))
		}(i, v)
	}
	wg.Wait()
	var total writeVariantStats
	for _, err := range writeErrs {
		if err != nil {
			return total, err
		}
	}
	for _, s := range stats {
		total.uploaded += s.uploaded
		total.existing += s.existing
	}
	return total, nil
}

// trackRevision registers every object the revision will occupy, manifest
// included, before the first write. The tracker refuses paths containing
// "://", which portable keys never do.
func (c *Cacher) trackRevision(ctx context.Context, imageType string, revision *artworkkey.PortableRevision) error {
	if c == nil || c.revisionTracker == nil {
		return nil
	}
	if err := c.revisionTracker.TrackArtworkRevision(ctx, revision.OriginalKey, imageType, revision.ObjectKeys()); err != nil {
		return fmt.Errorf("imagecache: track artwork revision: %w", err)
	}
	return nil
}

func (c *Cacher) recordRevision(
	ctx context.Context,
	revision *artworkkey.PortableRevision,
	result *imageutil.VariantResult,
	sourceClass string,
) error {
	if c == nil || c.revisionTracker == nil {
		return nil
	}
	objects := make([]artworkstore.ObjectInfo, 0, len(result.Variants)+1)
	for _, variant := range result.Variants {
		objects = append(objects, artworkstore.ObjectInfo{
			Key:       revision.VariantKeys[variant.Key],
			SizeBytes: int64(len(variant.Data)),
			MediaType: revision.MediaType,
		})
	}
	objects = append(objects, artworkstore.ObjectInfo{
		Key:       revision.ManifestKey,
		SizeBytes: int64(len(revision.ManifestJSON)),
		MediaType: artworkstore.MediaTypeForKey(revision.ManifestKey),
	})
	if err := c.revisionTracker.RecordArtworkRevision(ctx, revision.OriginalKey, sourceClass, objects); err != nil {
		return fmt.Errorf("imagecache: record artwork inventory: %w", err)
	}
	return nil
}

func artworkSourceClass(req CacheRequest) string {
	source := strings.ToLower(strings.TrimSpace(req.SourceURL))
	switch {
	case strings.HasPrefix(source, "file://"):
		return "library_sidecar"
	case strings.HasPrefix(source, "embedded://") || strings.EqualFold(req.ProviderID, "local"):
		return "embedded"
	case strings.HasPrefix(source, "generated://"):
		return sourceGenerated
	case strings.HasPrefix(source, "plugin://"):
		return "plugin"
	case source != "":
		return "provider"
	default:
		return "unknown"
	}
}

// writeObject stores one immutable object, retrying transient backend
// failures. Deterministic rejections — a malformed key, or existing bytes that
// disagree with ours — are returned immediately: retrying cannot change them,
// and a content mismatch on a content-addressed key means the store is corrupt
// or someone else wrote there, which must surface rather than be papered over.
func (c *Cacher) writeObject(ctx context.Context, key string, data []byte, mediaType string) error {
	const maxAttempts = 3
	meta := artworkstore.ObjectMetadata{MediaType: mediaType}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err := c.store.WriteImmutable(ctx, key, data, meta)
		if err == nil {
			return nil
		}
		lastErr = err
		if errors.Is(err, artworkstore.ErrContentMismatch) ||
			errors.Is(err, artworkstore.ErrInvalidKey) ||
			errors.Is(err, artworkstore.ErrNotRegularFile) {
			break
		}
		if attempt == maxAttempts-1 {
			// Final attempt failed; return immediately without a pointless backoff.
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 500 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	return fmt.Errorf("imagecache: write %s: %w", key, lastErr)
}

// downloadImage fetches the image at the given URL, enforcing size, timeout,
// and public-network limits.
func (c *Cacher) downloadImage(ctx context.Context, rawURL string) ([]byte, error) {
	return artworksource.Fetch(ctx, c.httpClient, c.enforcePublicURLs, rawURL)
}
