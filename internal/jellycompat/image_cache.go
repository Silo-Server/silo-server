package jellycompat

import (
	"container/list"
	"strconv"
	"strings"
	"sync"
	"time"
)

// imageCacheExpirySafetyMargin is how long before a signed URL expires the
// cache stops serving it. A URL with less than twice this left uses half its
// remaining life instead: s3.metadata_presign_expiry accepts any positive
// duration, and a fixed margin would never cache a URL that lives five
// minutes or less, leaving the URL-derived tags /Search/Hints emits
// unresolvable for a sessionless image request.
const imageCacheExpirySafetyMargin = 5 * time.Minute

// imageCacheMaxEntries caps each of the cache's two maps. List responses
// remember up to three images per item, so the cap keeps the artwork of
// roughly the ten thousand most recently served items on each node.
const imageCacheMaxEntries = 32_768

type cachedImage struct {
	url       string
	expiresAt time.Time
}

// ImageCache keeps short-lived mappings from Jellyfin-style image requests to
// the underlying Silo image URLs. Both maps are bounded, and an entry is never
// served within imageCacheExpirySafetyMargin, or half the URL's remaining life
// if that is shorter, of its signed URL's expiry when the cache knows that
// expiry (passed in, or read from the URL).
type ImageCache struct {
	mu sync.RWMutex
	// byTag answers URL-derived tags, which /Search/Hints still emits even
	// when list and detail responses carry signed tags.
	byTag   imageCacheMap
	byRoute imageCacheMap
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
		byTag:   newImageCacheMap(imageCacheMaxEntries),
		byRoute: newImageCacheMap(imageCacheMaxEntries),
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
// underlying resolved URL expiry. A nil urlExpiresAt falls back to the expiry
// the signed URL itself carries, if any.
func (c *ImageCache) RememberSizedUntil(routeID, imageType, imageURL, size string, urlExpiresAt *time.Time) {
	if c == nil || routeID == "" || imageType == "" || imageURL == "" {
		return
	}

	now := c.now()
	expiresAt := now.Add(c.ttl)
	if urlExpiresAt == nil {
		if signedExpiry, ok := signedImageURLExpiry(imageURL); ok {
			urlExpiresAt = &signedExpiry
		}
	}
	if urlExpiresAt != nil {
		remaining := urlExpiresAt.Sub(now)
		if remaining <= 0 {
			return
		}
		capped := urlExpiresAt.Add(-min(imageCacheExpirySafetyMargin, remaining/2))
		if capped.Before(expiresAt) {
			expiresAt = capped
		}
	}

	entry := cachedImage{
		url:       imageURL,
		expiresAt: expiresAt,
	}
	tag := tagValue(imageURL)
	routeKey := routeImageKey(routeID, imageType, size)

	c.mu.Lock()
	defer c.mu.Unlock()

	if tag != "" {
		c.byTag.put(tag, entry)
	}
	c.byRoute.put(routeKey, entry)
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

	if tag = canonicalCompatImageTag(tag); tag != "" {
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
	return c.lookupTag(canonicalCompatImageTag(tag))
}

// lookupTag resolves a tag without size partitioning. Tags are sha1 of the
// presigned URL: for S3-cached paths the size variant is embedded in the URL
// (so different sizes produce different tags), and for HTTP-passthrough URLs
// the same URL is reused across sizes (so the cached entry must be reachable
// regardless of which size bucket the lookup asks about).
func (c *ImageCache) lookupTag(tag string) (string, bool) {
	return c.lookup(&c.byTag, tag)
}

func (c *ImageCache) lookupRoute(key string) (string, bool) {
	return c.lookup(&c.byRoute, key)
}

// lookup only reads, so concurrent image requests share the read lock. An
// expired entry stays until it is overwritten or evicted.
func (c *ImageCache) lookup(entries *imageCacheMap, key string) (string, bool) {
	now := c.now()
	c.mu.RLock()
	entry, ok := entries.get(key)
	c.mu.RUnlock()
	if !ok || !entry.expiresAt.After(now) {
		return "", false
	}
	return entry.url, true
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

// artworkCapabilityPath is the route of Silo artwork capability URLs, the
// only URLs whose "exp" query parameter the cache trusts.
const artworkCapabilityPath = "/api/v2/artwork/"

// signedImageURLExpiry reads the expiry a signed image URL carries in its
// query: Silo artwork capability URLs sign an "exp" Unix time, and S3 SigV4
// presigned URLs carry X-Amz-Date plus X-Amz-Expires. List and detail
// responses only hold the URL string, so this is how their entries learn the
// real expiry. "exp" is read only on artwork capability paths, because a
// passthrough or plugin URL may use the name for something else.
func signedImageURLExpiry(imageURL string) (time.Time, bool) {
	path, query, ok := strings.Cut(imageURL, "?")
	if !ok {
		return time.Time{}, false
	}
	query, _, _ = strings.Cut(query, "#")
	var exp, amzDate, amzExpires string
	for query != "" {
		var param string
		param, query, _ = strings.Cut(query, "&")
		key, value, _ := strings.Cut(param, "=")
		switch key {
		case "exp":
			exp = value
		case "X-Amz-Date":
			amzDate = value
		case "X-Amz-Expires":
			amzExpires = value
		}
	}
	if strings.Contains(path, artworkCapabilityPath) {
		if unix, err := strconv.ParseInt(exp, 10, 64); err == nil {
			return time.Unix(unix, 0), true
		}
	}
	signedAt, dateErr := time.Parse("20060102T150405Z", amzDate)
	seconds, expiresErr := strconv.ParseInt(amzExpires, 10, 64)
	if dateErr == nil && expiresErr == nil {
		return signedAt.Add(time.Duration(seconds) * time.Second), true
	}
	return time.Time{}, false
}

type imageCacheSlot struct {
	image cachedImage
	order *list.Element
}

// imageCacheMap holds at most limit entries and, when full, evicts the one
// written least recently. Reads do not reorder entries, so lookups need only
// the owning ImageCache's read lock. List, detail and resolved image responses
// rewrite the entries they serve, which keeps write order close to use order.
type imageCacheMap struct {
	limit   int
	order   *list.List // of string keys, most recently written first
	entries map[string]imageCacheSlot
}

func newImageCacheMap(limit int) imageCacheMap {
	return imageCacheMap{limit: limit, order: list.New(), entries: make(map[string]imageCacheSlot)}
}

// put requires the owning ImageCache's write lock.
func (m *imageCacheMap) put(key string, image cachedImage) {
	if slot, ok := m.entries[key]; ok {
		m.order.MoveToFront(slot.order)
		m.entries[key] = imageCacheSlot{image: image, order: slot.order}
		return
	}
	m.entries[key] = imageCacheSlot{image: image, order: m.order.PushFront(key)}
	if m.order.Len() > m.limit {
		oldest, _ := m.order.Remove(m.order.Back()).(string)
		delete(m.entries, oldest)
	}
}

// get requires at least the owning ImageCache's read lock.
func (m *imageCacheMap) get(key string) (cachedImage, bool) {
	slot, ok := m.entries[key]
	return slot.image, ok
}
