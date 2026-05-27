package jellycompat

import (
	"strings"
	"sync"
	"time"
)

const imageCacheExpirySafetyMargin = 5 * time.Minute

type cachedImage struct {
	url       string
	expiresAt time.Time
}

// ImageCache keeps short-lived mappings from Jellyfin-style image requests to
// the underlying Silo image URLs.
type ImageCache struct {
	mu      sync.RWMutex
	byTag   map[string]cachedImage
	byRoute map[string]cachedImage
	ttl     time.Duration
	now     func() time.Time
}

// NewImageCache creates a new cache for compat image lookups.
func NewImageCache(ttl time.Duration, now func() time.Time) *ImageCache {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	if now == nil {
		now = time.Now
	}
	return &ImageCache{
		byTag:   make(map[string]cachedImage),
		byRoute: make(map[string]cachedImage),
		ttl:     ttl,
		now:     now,
	}
}

// Remember stores a Jellyfin image route mapping using the default compat size bucket.
func (c *ImageCache) Remember(routeID, imageType, imageURL string) {
	c.RememberSized(routeID, imageType, imageURL, "")
}

// RememberSized stores a Jellyfin image route mapping for a specific size bucket.
func (c *ImageCache) RememberSized(routeID, imageType, imageURL, size string) {
	c.RememberSizedUntil(routeID, imageType, imageURL, size, nil)
}

// RememberSizedUntil stores a Jellyfin image route mapping, capped by the
// underlying resolved URL expiry when that expiry is known.
func (c *ImageCache) RememberSizedUntil(routeID, imageType, imageURL, size string, urlExpiresAt *time.Time) {
	if c == nil || routeID == "" || imageType == "" || imageURL == "" {
		return
	}

	now := c.now()
	expiresAt := now.Add(c.ttl)
	if urlExpiresAt != nil {
		capped := urlExpiresAt.Add(-imageCacheExpirySafetyMargin)
		if !capped.After(now) {
			return
		}
		if capped.Before(expiresAt) {
			expiresAt = capped
		}
	}

	entry := cachedImage{
		url:       imageURL,
		expiresAt: expiresAt,
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if tag := tagValue(imageURL); tag != "" {
		c.byTag[tag] = entry
	}
	c.byRoute[routeImageKey(routeID, imageType, size)] = entry
}

// Lookup returns a cached image URL by tag or route using the default compat size bucket.
func (c *ImageCache) Lookup(routeID, imageType, tag string) (string, bool) {
	return c.LookupSized(routeID, imageType, tag, "")
}

// LookupSized returns a cached image URL by tag or route for a specific size bucket.
func (c *ImageCache) LookupSized(routeID, imageType, tag, size string) (string, bool) {
	if c == nil {
		return "", false
	}

	if tag = strings.TrimSpace(tag); tag != "" {
		return c.LookupTag(tag)
	}

	if routeID == "" || imageType == "" {
		return "", false
	}
	return c.lookupRoute(routeImageKey(routeID, imageType, size))
}

// LookupTag resolves a cached image URL only by its legacy URL-derived tag.
func (c *ImageCache) LookupTag(tag string) (string, bool) {
	if c == nil {
		return "", false
	}
	return c.lookupTag(strings.TrimSpace(tag))
}

// lookupTag resolves a tag without size partitioning. Tags are sha1 of the
// presigned URL: for S3-cached paths the size variant is embedded in the URL
// (so different sizes produce different tags), and for HTTP-passthrough URLs
// the same URL is reused across sizes (so the cached entry must be reachable
// regardless of which size bucket the lookup asks about).
func (c *ImageCache) lookupTag(tag string) (string, bool) {
	c.mu.RLock()
	entry, ok := c.byTag[tag]
	c.mu.RUnlock()
	if !ok {
		return "", false
	}
	if c.isExpired(entry) {
		c.mu.Lock()
		delete(c.byTag, tag)
		c.mu.Unlock()
		return "", false
	}
	return entry.url, true
}

func (c *ImageCache) lookupRoute(key string) (string, bool) {
	c.mu.RLock()
	entry, ok := c.byRoute[key]
	c.mu.RUnlock()
	if !ok {
		return "", false
	}
	if c.isExpired(entry) {
		c.mu.Lock()
		delete(c.byRoute, key)
		c.mu.Unlock()
		return "", false
	}
	return entry.url, true
}

func (c *ImageCache) isExpired(entry cachedImage) bool {
	return !entry.expiresAt.IsZero() && !entry.expiresAt.After(c.now())
}

func routeImageKey(routeID, imageType, size string) string {
	return routeID + "|" + strings.ToLower(strings.TrimSpace(imageType)) + "|" + normalizeImageCacheSize(size)
}

func normalizeImageCacheSize(size string) string {
	normalized := strings.ToLower(strings.TrimSpace(size))
	if normalized == "" {
		return compatCardImageSize
	}
	return normalized
}
