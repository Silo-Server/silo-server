// Package imagecache downloads images from URLs, generates sized variants,
// computes thumbhashes, and uploads all variants to S3.
package imagecache

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/imageutil"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

const (
	maxDownloadBytes = 25 * 1024 * 1024 // allow oversized provider originals; cached variants are dimension-capped
	downloadTimeout  = 30 * time.Second
)

// ObjectPutter is the S3 interface required by Cacher.
type ObjectPutter interface {
	PutObject(ctx context.Context, bucket, key string, data []byte) error
	Bucket() string
}

type objectMatcher interface {
	ObjectMatches(ctx context.Context, bucket, key string, data []byte) (bool, error)
}

// ArtworkRevisionTracker persists the exact object manifest for an immutable
// revision before any object is uploaded.
type ArtworkRevisionTracker interface {
	TrackArtworkRevision(ctx context.Context, originalPath, imageType string, objectKeys []string) error
}

// ImageURLResolver resolves plugin:// paths to HTTP URLs.
type ImageURLResolver interface {
	ResolveImageURL(ctx context.Context, path string, variant string) string
}

// CacheRequest describes a single image to cache. For season posters and
// episode stills, ContentID is the parent series's provider ID and the
// SeasonNumber / EpisodeNumber fields scope the S3 key so siblings do not
// collide. Both pointers are nil for item-level images.
type CacheRequest struct {
	SourceURL     string
	ProviderID    string
	ContentType   string // "movies" or "series"
	ContentID     string
	ImageType     metadata.ImageType
	SeasonNumber  *int
	EpisodeNumber *int
	Language      string
	ImageResolver ImageURLResolver // optional; used when SourceURL is a plugin:// path
	// KeyDiscriminator, when set, is inserted into the S3 key between the
	// content ID and the image type (local sidecar art uses the file's 8-hex
	// content hash) so re-cached art rotates to a fresh key.
	KeyDiscriminator string
}

// CacheResult is returned by Cache on success.
type CacheResult struct {
	BasePath         string // S3 key prefix, e.g. "tmdb/movies/550/poster"
	OriginalPath     string // exact immutable original-variant object key
	Revision         string // content revision shared by generated variants
	VariantPaths     map[string]string
	Thumbhash        string // base64-encoded
	Ext              string // file extension including dot (e.g. ".jpg", ".png")
	UploadedVariants int
	ExistingVariants int
}

// Cacher downloads and stores image variants to S3.
type Cacher struct {
	s3                ObjectPutter
	revisionTracker   ArtworkRevisionTracker
	httpClient        *http.Client
	enforcePublicURLs bool
}

// New creates a new Cacher backed by the given ObjectPutter.
func New(s3 ObjectPutter) *Cacher {
	return &Cacher{s3: s3, httpClient: newSecureHTTPClient(), enforcePublicURLs: true}
}

// SetArtworkRevisionTracker wires durable revision lifecycle tracking. The
// production server configures this whenever object storage is available.
func (c *Cacher) SetArtworkRevisionTracker(tracker ArtworkRevisionTracker) {
	if c != nil {
		c.revisionTracker = tracker
	}
}

func newWithHTTPClient(s3 ObjectPutter, client *http.Client) *Cacher {
	if client == nil {
		client = http.DefaultClient
	}
	return &Cacher{s3: s3, httpClient: client}
}

// CacheImage implements metadata.ImageCacher using the internal Cache method.
func (c *Cacher) CacheImage(ctx context.Context, req metadata.CacheImageRequest) (*metadata.CacheImageResult, error) {
	result, err := c.Cache(ctx, CacheRequest{
		SourceURL:     req.SourceURL,
		ProviderID:    req.ProviderID,
		ContentType:   req.ContentType,
		ContentID:     req.ContentID,
		ImageType:     req.ImageType,
		SeasonNumber:  req.SeasonNumber,
		EpisodeNumber: req.EpisodeNumber,
		Language:      req.Language,
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
		ProviderID:       req.ProviderID,
		ContentType:      req.ContentType,
		ContentID:        req.ContentID,
		ImageType:        req.ImageType,
		SeasonNumber:     req.SeasonNumber,
		EpisodeNumber:    req.EpisodeNumber,
		Language:         req.Language,
		KeyDiscriminator: req.KeyDiscriminator,
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
// scanner -> imagecache -> metadata -> scanner). Stores under
// "local/audiobooks/{contentID}/poster/...".
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

// CacheEbookCover stores an embedded ebook cover under
// "local/ebooks/{contentID}/poster/..." using the same poster variants as
// provider-hosted book artwork.
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
// episode/season invariant shared by Cache and CacheBytes. Keeping it in one
// place prevents the season/episode guard from drifting between the two paths:
// buildBasePath only appends the episode segment inside the SeasonNumber branch,
// so an episode without a season would silently collide distinct episodes' art
// under the same S3 key.
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

// CacheBytes performs the same variant generation, thumbhash, and S3 upload as
// Cache but starts from raw image bytes already in hand. Used by the
// audiobook scanner to push embedded M4B cover art into S3 without round-
// tripping through HTTP.
func (c *Cacher) CacheBytes(ctx context.Context, data []byte, req CacheRequest) (*CacheResult, error) {
	if err := validateCacheRequest(req); err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("imagecache: image data is empty")
	}
	thumbhash, err := imageutil.Thumbhash(data)
	if err != nil {
		return nil, fmt.Errorf("imagecache: thumbhash: %w", err)
	}
	widths := variantWidths(req.ImageType)
	result, err := imageutil.GenerateVariants(data, widths)
	if err != nil {
		return nil, fmt.Errorf("imagecache: generate variants: %w", err)
	}
	basePath := buildBasePath(req)
	bucket := c.s3.Bucket()
	revision := variantRevision(result)
	variantPaths := buildVariantPaths(basePath, revision, result)
	if err := c.trackRevision(ctx, req.ImageType, variantPaths); err != nil {
		return nil, err
	}

	uploadStats, err := c.uploadVariants(ctx, bucket, result, variantPaths)
	if err != nil {
		return nil, err
	}
	return &CacheResult{
		BasePath:         basePath,
		OriginalPath:     variantPaths[artworkkey.OriginalVariant],
		Revision:         revision,
		VariantPaths:     variantPaths,
		Thumbhash:        thumbhash,
		Ext:              result.Ext,
		UploadedVariants: uploadStats.uploaded,
		ExistingVariants: uploadStats.existing,
	}, nil
}

// Cache downloads the image at req.SourceURL and stores it through the same
// variant, revision-tracking, and upload pipeline as CacheBytes.
func (c *Cacher) Cache(ctx context.Context, req CacheRequest) (*CacheResult, error) {
	if err := validateCacheRequest(req); err != nil {
		return nil, err
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

	return c.CacheBytes(ctx, data, req)
}

// variantWidths returns the resize widths for the given image type. The
// ladder itself is owned by artworkkey so key expansion and GC manifests can
// never drift from what is generated here.
func variantWidths(t metadata.ImageType) []int {
	return artworkkey.VariantWidths(metadata.ImageTypeToString(t))
}

type uploadVariantStats struct {
	uploaded int
	existing int
}

func (c *Cacher) uploadVariants(ctx context.Context, bucket string, result *imageutil.VariantResult, variantPaths map[string]string) (uploadVariantStats, error) {
	var wg sync.WaitGroup
	uploadErrs := make([]error, len(result.Variants))
	stats := make([]uploadVariantStats, len(result.Variants))
	for i, v := range result.Variants {
		wg.Add(1)
		go func(idx int, variant imageutil.Variant) {
			defer wg.Done()
			key := variantPaths[variant.Key]
			if exists, err := objectMatches(ctx, c.s3, bucket, key, variant.Data); err != nil {
				uploadErrs[idx] = fmt.Errorf("imagecache: check existing %s: %w", key, err)
				return
			} else if exists {
				stats[idx].existing = 1
				return
			}
			if err := putObjectWithRetry(ctx, c.s3, bucket, key, variant.Data); err != nil {
				uploadErrs[idx] = fmt.Errorf("imagecache: upload %s: %w", key, err)
				return
			}
			stats[idx].uploaded = 1
		}(i, v)
	}
	wg.Wait()
	var total uploadVariantStats
	for _, err := range uploadErrs {
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

func variantRevision(result *imageutil.VariantResult) string {
	h := sha256.New()
	_, _ = io.WriteString(h, "silo-artwork-v1\x00")
	_, _ = io.WriteString(h, result.Ext)
	_, _ = h.Write([]byte{0})
	variants := append([]imageutil.Variant(nil), result.Variants...)
	sort.Slice(variants, func(i, j int) bool { return variants[i].Key < variants[j].Key })
	var size [8]byte
	for _, variant := range variants {
		_, _ = io.WriteString(h, variant.Key)
		_, _ = h.Write([]byte{0})
		binary.BigEndian.PutUint64(size[:], uint64(len(variant.Data)))
		_, _ = h.Write(size[:])
		_, _ = h.Write(variant.Data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func buildVariantPaths(basePath, revision string, result *imageutil.VariantResult) map[string]string {
	paths := make(map[string]string, len(result.Variants))
	for _, variant := range result.Variants {
		paths[variant.Key] = artworkkey.Build(basePath, variant.Key, revision, result.Ext)
	}
	return paths
}

func (c *Cacher) trackRevision(ctx context.Context, imageType metadata.ImageType, variantPaths map[string]string) error {
	if c == nil || c.revisionTracker == nil {
		return nil
	}
	originalPath := variantPaths[artworkkey.OriginalVariant]
	keys := make([]string, 0, len(variantPaths))
	for _, key := range variantPaths {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if err := c.revisionTracker.TrackArtworkRevision(ctx, originalPath, metadata.ImageTypeToString(imageType), keys); err != nil {
		return fmt.Errorf("imagecache: track artwork revision: %w", err)
	}
	return nil
}

// objectMatches reports whether the object at key already holds exactly data.
// Backends that cannot verify content report false so the immutable object is
// rewritten; bare existence must never be accepted as a content match.
func objectMatches(ctx context.Context, putter ObjectPutter, bucket, key string, data []byte) (bool, error) {
	matcher, ok := putter.(objectMatcher)
	if !ok {
		return false, nil
	}
	return matcher.ObjectMatches(ctx, bucket, key, data)
}

func putObjectWithRetry(ctx context.Context, putter ObjectPutter, bucket, key string, data []byte) error {
	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := putter.PutObject(ctx, bucket, key, data); err != nil {
			lastErr = err
			if attempt == maxAttempts-1 {
				// Final attempt failed; return immediately without a pointless backoff.
				break
			}
			timer := time.NewTimer(time.Duration(attempt+1) * 500 * time.Millisecond)
			select {
			case <-timer.C:
				continue
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}
		return nil
	}
	return lastErr
}

// buildBasePath constructs the S3 key prefix for a given image. Season
// posters and episode stills nest under their parent series so a single
// DeletePrefix on the series prefix cascades to all child images.
//
//	item-level:        {provider}/{type}/{id}/{imageType}
//	localized item:   {provider}/{type}/{id}/localizations/{lang}/{imageType}
//	season:           {provider}/{type}/{id}/seasons/{n}/{imageType}
//	localized season: {provider}/{type}/{id}/localizations/{lang}/seasons/{n}/{imageType}
//	episode:          {provider}/{type}/{id}/seasons/{n}/episodes/{m}/{imageType}
//
// A non-empty KeyDiscriminator (local sidecar content hash) is inserted
// immediately before the image type so the variant's parent directory stays
// the image type segment (the imageTypeFromCachedPath contract).
func buildBasePath(req CacheRequest) string {
	imageTypeName := imageTypeName(req.ImageType)
	base := fmt.Sprintf("%s/%s/%s", req.ProviderID, req.ContentType, req.ContentID)
	if lang := normalizeImageLanguage(req.Language); lang != "" {
		base = fmt.Sprintf("%s/localizations/%s", base, lang)
	}
	if req.SeasonNumber != nil {
		base = fmt.Sprintf("%s/seasons/%d", base, *req.SeasonNumber)
		if req.EpisodeNumber != nil {
			base = fmt.Sprintf("%s/episodes/%d", base, *req.EpisodeNumber)
		}
	}
	if discriminator := strings.TrimSpace(req.KeyDiscriminator); discriminator != "" {
		base = base + "/" + discriminator
	}
	return base + "/" + imageTypeName
}

// imageTypeName returns the lowercase string name for an ImageType.
func imageTypeName(t metadata.ImageType) string {
	switch t {
	case metadata.ImagePoster:
		return "poster"
	case metadata.ImageBackdrop:
		return "backdrop"
	case metadata.ImageLogo:
		return "logo"
	case metadata.ImageStill:
		return "still"
	case metadata.ImageProfile:
		return "profile"
	default:
		return "unknown"
	}
}

func normalizeImageLanguage(language string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	if language == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range language {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// downloadImage fetches the image at the given URL, enforcing size, timeout,
// and public-network limits.
func (c *Cacher) downloadImage(ctx context.Context, rawURL string) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse URL: %w", err)
	}
	if c.enforcePublicURLs {
		if err := validatePublicImageURL(parsed); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	client := c.httpClient
	if client == nil {
		client = newSecureHTTPClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, maxDownloadBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if int64(len(data)) > maxDownloadBytes {
		return nil, fmt.Errorf("image exceeds %d byte limit", maxDownloadBytes)
	}

	return data, nil
}

func newSecureHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         secureImageDialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return http.ErrUseLastResponse
			}
			return validatePublicImageURL(req.URL)
		},
	}
}

func validatePublicImageURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("empty URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL host is required")
	}
	if addr, err := netip.ParseAddr(host); err == nil && !isPublicAddr(addr) {
		return fmt.Errorf("private image host %q is not allowed", host)
	}
	return nil
}

func secureImageDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addr, err := resolvePublicAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: downloadTimeout}
	return dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
}

func resolvePublicAddr(ctx context.Context, host string) (netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		if isPublicAddr(addr) {
			return addr, nil
		}
		return netip.Addr{}, fmt.Errorf("private image host %q is not allowed", host)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("resolve image host %q: %w", host, err)
	}
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip.IP)
		if ok && isPublicAddr(addr) {
			return addr, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("image host %q did not resolve to a public address", host)
}

func isPublicAddr(addr netip.Addr) bool {
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	return addr.IsGlobalUnicast() &&
		!addr.IsPrivate() &&
		!addr.IsLoopback() &&
		!addr.IsLinkLocalUnicast() &&
		!addr.IsLinkLocalMulticast() &&
		!addr.IsMulticast() &&
		!addr.IsUnspecified()
}
