package jellycompat

import (
	"fmt"
	"strconv"
	"testing"
	"time"
)

func TestImageCacheEvictsLeastRecentlyWrittenAtCapacity(t *testing.T) {
	now := fixedNow()
	cache := NewImageCache(87600*time.Hour, func() time.Time { return now })
	remember := func(i int) {
		cache.RememberSized(fmt.Sprintf("item-%d", i), "Primary", fmt.Sprintf("https://image.example.test/%d.jpg", i), compatCardImageSize)
	}

	inserted := 2 * imageCacheMaxEntries
	for i := range inserted {
		remember(i)
		if i == imageCacheMaxEntries {
			// item-1 and item-2 are now the oldest entries. A list response
			// that serves item-1 again rewrites it, so it survives the second
			// half; a lookup of item-2 does not reorder it.
			remember(1)
			if _, ok := cache.LookupSized("item-2", "Primary", "", compatCardImageSize); !ok {
				t.Fatal("item-2 was evicted before the cache reached capacity")
			}
		}
	}

	if got := len(cache.byRoute.entries); got != imageCacheMaxEntries {
		t.Fatalf("route entries after %d inserts = %d, want %d", inserted, got, imageCacheMaxEntries)
	}
	if got := len(cache.byTag.entries); got != imageCacheMaxEntries {
		t.Fatalf("tag entries after %d inserts = %d, want %d", inserted, got, imageCacheMaxEntries)
	}
	if _, ok := cache.LookupSized("item-2", "Primary", "", compatCardImageSize); ok {
		t.Fatal("least recently written item-2 was not evicted")
	}
	if _, ok := cache.LookupSized("item-1", "Primary", "", compatCardImageSize); !ok {
		t.Fatal("rewritten item-1 was evicted")
	}
	if _, ok := cache.LookupTag(tagValue("https://image.example.test/1.jpg")); !ok {
		t.Fatal("rewritten item-1 tag entry was evicted")
	}
	last := inserted - 1
	if _, ok := cache.LookupTag(tagValue(fmt.Sprintf("https://image.example.test/%d.jpg", last))); !ok {
		t.Fatal("newest tag entry was evicted")
	}
}

// TestImageCacheNeverServesSignedURLNearExpiry covers list and detail paths,
// which remember signed URLs without passing their expiry. The cache must
// read it from the URL instead of keeping the entry for the full cache TTL.
func TestImageCacheNeverServesSignedURLNearExpiry(t *testing.T) {
	start := fixedNow()
	urlExpiresAt := start.Add(10 * time.Minute)
	signedURLs := map[string]string{
		"artwork": "/api/v2/artwork/tmdb/movies/1/poster/w342.abc.webp?exp=" + strconv.FormatInt(urlExpiresAt.Unix(), 10) + "&sig=x",
		"s3": "https://bucket.s3.example.test/poster.webp?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Date=" +
			start.Format("20060102T150405Z") + "&X-Amz-Expires=600&X-Amz-Signature=y",
	}
	for name, signedURL := range signedURLs {
		t.Run(name, func(t *testing.T) {
			now := start
			cache := NewImageCache(87600*time.Hour, func() time.Time { return now })
			cache.RememberSized("item-1", "Primary", signedURL, compatCardImageSize)

			now = start.Add(4 * time.Minute)
			if got, ok := cache.LookupSized("item-1", "Primary", "", compatCardImageSize); !ok || got != signedURL {
				t.Fatalf("route lookup 6m before URL expiry = (%q, %v), want hit", got, ok)
			}

			now = start.Add(6 * time.Minute)
			if got, ok := cache.LookupSized("item-1", "Primary", "", compatCardImageSize); ok {
				t.Fatalf("route lookup 4m before URL expiry = (%q, %v), want miss", got, ok)
			}
			if got, ok := cache.LookupTag(tagValue(signedURL)); ok {
				t.Fatalf("tag lookup 4m before URL expiry = (%q, %v), want miss", got, ok)
			}
		})
	}
}

func TestImageCacheSkipsExpiredSignedURL(t *testing.T) {
	now := fixedNow()
	for _, urlExpiresAt := range []time.Time{now, now.Add(-time.Minute)} {
		cache := NewImageCache(time.Hour, func() time.Time { return now })
		signedURL := "/api/v2/artwork/poster.webp?exp=" + strconv.FormatInt(urlExpiresAt.Unix(), 10) + "&sig=x"

		cache.RememberSized("item-1", "Primary", signedURL, compatCardImageSize)

		if got, ok := cache.LookupSized("item-1", "Primary", "", compatCardImageSize); ok {
			t.Fatalf("LookupSized = (%q, %v), want a URL expiring at %s never cached", got, ok, urlExpiresAt)
		}
		if got := len(cache.byTag.entries) + len(cache.byRoute.entries); got != 0 {
			t.Fatalf("cache holds %d entries for a URL expiring at %s, want 0", got, urlExpiresAt)
		}
	}
}

// TestImageCacheKeepsShortLivedSignedURLs covers a short
// s3.metadata_presign_expiry, which accepts any positive duration. A URL that
// lives five minutes or less must still be cached, because the URL-derived
// tags /Search/Hints emits resolve only through the cache. The margin shrinks
// to half the URL's remaining life, and the entry is never served inside it.
func TestImageCacheKeepsShortLivedSignedURLs(t *testing.T) {
	start := fixedNow()
	lifetime := time.Minute
	urlExpiresAt := start.Add(lifetime)
	stopServing := urlExpiresAt.Add(-lifetime / 2)
	signedURLs := map[string]string{
		"artwork": "/api/v2/artwork/tmdb/movies/1/poster/w342.abc.webp?exp=" + strconv.FormatInt(urlExpiresAt.Unix(), 10) + "&sig=x",
		"s3": "https://bucket.s3.example.test/poster.webp?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Date=" +
			start.Format("20060102T150405Z") + "&X-Amz-Expires=" + strconv.Itoa(int(lifetime.Seconds())) + "&X-Amz-Signature=y",
	}
	for name, signedURL := range signedURLs {
		remember := map[string]func(*ImageCache){
			"expiry read from URL": func(c *ImageCache) {
				c.RememberSized("item-1", "Primary", signedURL, compatCardImageSize)
			},
			"expiry passed in": func(c *ImageCache) {
				c.RememberSizedUntil("item-1", "Primary", signedURL, compatCardImageSize, &urlExpiresAt)
			},
		}
		for how, rememberURL := range remember {
			t.Run(fmt.Sprintf("%s/%s/%s", lifetime, name, how), func(t *testing.T) {
				now := start
				cache := NewImageCache(87600*time.Hour, func() time.Time { return now })
				rememberURL(cache)

				for _, at := range []time.Time{start, stopServing.Add(-time.Second)} {
					now = at
					if got, ok := cache.LookupTag(tagValue(signedURL)); !ok || got != signedURL {
						t.Fatalf("tag lookup %s before URL expiry = (%q, %v), want hit", urlExpiresAt.Sub(now), got, ok)
					}
					if got, ok := cache.LookupSized("item-1", "Primary", "", compatCardImageSize); !ok || got != signedURL {
						t.Fatalf("route lookup %s before URL expiry = (%q, %v), want hit", urlExpiresAt.Sub(now), got, ok)
					}
				}

				now = stopServing
				if got, ok := cache.LookupTag(tagValue(signedURL)); ok {
					t.Fatalf("tag lookup %s before URL expiry = (%q, %v), want miss", urlExpiresAt.Sub(now), got, ok)
				}
				if got, ok := cache.LookupSized("item-1", "Primary", "", compatCardImageSize); ok {
					t.Fatalf("route lookup %s before URL expiry = (%q, %v), want miss", urlExpiresAt.Sub(now), got, ok)
				}
			})
		}
	}
}
