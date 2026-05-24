// Package imagecache downloads images from URLs, generates sized variants,
// computes thumbhashes, and uploads all variants to S3.
package imagecache

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/imageutil"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

const (
	maxDownloadBytes = 10 * 1024 * 1024 // 10 MB
	downloadTimeout  = 30 * time.Second
)

// ObjectPutter is the S3 interface required by Cacher.
type ObjectPutter interface {
	PutObject(ctx context.Context, bucket, key string, data []byte) error
	Bucket() string
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
	ImageResolver ImageURLResolver // optional; used when SourceURL is a plugin:// path
}

// CacheResult is returned by Cache on success.
type CacheResult struct {
	BasePath  string // S3 key prefix, e.g. "tmdb/movies/550/poster"
	Thumbhash string // base64-encoded
	Ext       string // file extension including dot (e.g. ".jpg", ".png")
}

// Cacher downloads and stores image variants to S3.
type Cacher struct {
	s3 ObjectPutter
}

// New creates a new Cacher backed by the given ObjectPutter.
func New(s3 ObjectPutter) *Cacher {
	return &Cacher{s3: s3}
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
	})
	if err != nil {
		return nil, err
	}
	return &metadata.CacheImageResult{
		BasePath:  result.BasePath,
		Thumbhash: result.Thumbhash,
		Ext:       result.Ext,
	}, nil
}

// CacheAudiobookCover is a thin convenience over CacheBytes specifically
// for the audiobook scanner. Avoids exporting the imagecache request
// struct to the scanner package (which would create an import cycle
// scanner -> imagecache -> metadata -> scanner). Stores under
// "local/audiobooks/{contentID}/poster/...".
func (c *Cacher) CacheAudiobookCover(ctx context.Context, data []byte, contentID string) (basePath string, ext string, thumbhash string, err error) {
	res, err := c.CacheBytes(ctx, data, CacheRequest{
		ProviderID:  "local",
		ContentType: "audiobooks",
		ContentID:   contentID,
		ImageType:   metadata.ImagePoster,
	})
	if err != nil {
		return "", "", "", err
	}
	return res.BasePath, res.Ext, res.Thumbhash, nil
}

// CacheBytes performs the same variant generation, thumbhash, and S3 upload as
// Cache but starts from raw image bytes already in hand. Used by the
// audiobook scanner to push embedded M4B cover art into S3 without round-
// tripping through HTTP.
func (c *Cacher) CacheBytes(ctx context.Context, data []byte, req CacheRequest) (*CacheResult, error) {
	if strings.TrimSpace(req.ProviderID) == "" {
		return nil, fmt.Errorf("imagecache: provider ID is required")
	}
	if strings.TrimSpace(req.ContentType) == "" {
		return nil, fmt.Errorf("imagecache: content type is required")
	}
	if strings.TrimSpace(req.ContentID) == "" {
		return nil, fmt.Errorf("imagecache: content ID is required")
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
	basePath := buildBasePath(req.ProviderID, req.ContentType, req.ContentID, req.ImageType, req.SeasonNumber, req.EpisodeNumber)
	bucket := c.s3.Bucket()
	var wg sync.WaitGroup
	uploadErrs := make([]error, len(result.Variants))
	for i, v := range result.Variants {
		wg.Add(1)
		go func(idx int, variant imageutil.Variant) {
			defer wg.Done()
			key := basePath + "/" + variant.Key + result.Ext
			if err := c.s3.PutObject(ctx, bucket, key, variant.Data); err != nil {
				uploadErrs[idx] = fmt.Errorf("imagecache: upload %s: %w", key, err)
			}
		}(i, v)
	}
	wg.Wait()
	for _, err := range uploadErrs {
		if err != nil {
			return nil, err
		}
	}
	return &CacheResult{BasePath: basePath, Thumbhash: thumbhash, Ext: result.Ext}, nil
}

// Cache downloads the image at req.SourceURL, generates variants, computes a
// thumbhash, uploads all variants to S3, and returns the base path and thumbhash.
func (c *Cacher) Cache(ctx context.Context, req CacheRequest) (*CacheResult, error) {
	if strings.TrimSpace(req.ProviderID) == "" {
		return nil, fmt.Errorf("imagecache: provider ID is required")
	}
	if strings.TrimSpace(req.ContentType) == "" {
		return nil, fmt.Errorf("imagecache: content type is required")
	}
	if strings.TrimSpace(req.ContentID) == "" {
		return nil, fmt.Errorf("imagecache: content ID is required")
	}
	if req.EpisodeNumber != nil && req.SeasonNumber == nil {
		return nil, fmt.Errorf("imagecache: episode number requires a season number")
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

	data, err := downloadImage(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("imagecache: download %s: %w", url, err)
	}

	// Compute thumbhash from the original downloaded data (JPEG/PNG) before
	// converting to WebP, since Go's image.Decode doesn't support WebP.
	thumbhash, err := imageutil.Thumbhash(data)
	if err != nil {
		return nil, fmt.Errorf("imagecache: thumbhash: %w", err)
	}

	widths := variantWidths(req.ImageType)

	result, err := imageutil.GenerateVariants(data, widths)
	if err != nil {
		return nil, fmt.Errorf("imagecache: generate variants: %w", err)
	}

	basePath := buildBasePath(req.ProviderID, req.ContentType, req.ContentID, req.ImageType, req.SeasonNumber, req.EpisodeNumber)
	bucket := c.s3.Bucket()

	// Upload all variants concurrently.
	var wg sync.WaitGroup
	uploadErrs := make([]error, len(result.Variants))
	for i, v := range result.Variants {
		wg.Add(1)
		go func(idx int, variant imageutil.Variant) {
			defer wg.Done()
			key := basePath + "/" + variant.Key + result.Ext
			if err := c.s3.PutObject(ctx, bucket, key, variant.Data); err != nil {
				uploadErrs[idx] = fmt.Errorf("imagecache: upload %s: %w", key, err)
			}
		}(i, v)
	}
	wg.Wait()

	for _, err := range uploadErrs {
		if err != nil {
			return nil, err
		}
	}

	return &CacheResult{
		BasePath:  basePath,
		Thumbhash: thumbhash,
		Ext:       result.Ext,
	}, nil
}

// variantWidths returns the resize widths for the given image type.
func variantWidths(t metadata.ImageType) []int {
	switch t {
	case metadata.ImagePoster:
		return []int{500, 300}
	case metadata.ImageBackdrop:
		return []int{1920, 1280, 300}
	case metadata.ImageLogo:
		return []int{500}
	case metadata.ImageStill:
		return []int{500, 300}
	default:
		return []int{500, 300}
	}
}

// buildBasePath constructs the S3 key prefix for a given image. Season
// posters and episode stills nest under their parent series so a single
// DeletePrefix on the series prefix cascades to all child images.
//
//	item-level:   {provider}/{type}/{id}/{imageType}
//	season:       {provider}/{type}/{id}/seasons/{n}/{imageType}
//	episode:      {provider}/{type}/{id}/seasons/{n}/episodes/{m}/{imageType}
func buildBasePath(providerID, contentType, contentID string, t metadata.ImageType, seasonNumber, episodeNumber *int) string {
	imageTypeName := imageTypeName(t)
	base := fmt.Sprintf("%s/%s/%s", providerID, contentType, contentID)
	if seasonNumber != nil {
		base = fmt.Sprintf("%s/seasons/%d", base, *seasonNumber)
		if episodeNumber != nil {
			base = fmt.Sprintf("%s/episodes/%d", base, *episodeNumber)
		}
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
	default:
		return "unknown"
	}
}

// downloadImage fetches the image at the given URL, enforcing size and timeout limits.
func downloadImage(ctx context.Context, rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
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
