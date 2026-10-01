package jellycompat

import (
	"fmt"
	"strconv"
	"testing"
	"time"
)

func TestImageCacheLookupSizedUsesSizeBucket(t *testing.T) {
	now := fixedNow()
	cache := NewImageCache(time.Hour, func() time.Time { return now })

	cache.RememberSized("item-1", "Primary", "https://example.com/small.jpg", compatCardImageSize)

	got, ok := cache.LookupSized("item-1", "Primary", "", compatCardImageSize)
	if !ok || got != "https://example.com/small.jpg" {
		t.Fatalf("LookupSized(small) = (%q, %v), want small image", got, ok)
	}

	if _, ok := cache.LookupSized("item-1", "Primary", "", "original"); ok {
		t.Fatal("LookupSized(original) unexpectedly returned small image")
	}
}

func TestImageCacheLookupSizedUsesTagWithinMatchingSizeBucket(t *testing.T) {
	now := fixedNow()
	cache := NewImageCache(time.Hour, func() time.Time { return now })

	smallURL := "https://example.com/small.jpg"
	largeURL := "https://example.com/large.jpg"
	cache.RememberSized("item-1", "Primary", smallURL, compatCardImageSize)
	cache.RememberSized("item-1", "Primary", largeURL, "original")

	got, ok := cache.LookupSized("item-1", "Primary", tagValue(largeURL), "original")
	if !ok || got != largeURL {
		t.Fatalf("LookupSized(tag=large, original) = (%q, %v), want large image", got, ok)
	}
}

// TestImageCacheLookupSizedReturnsHTTPPassthroughAcrossSizes locks in the
// behavior PR #28's review feedback discussed: HTTP-passthrough URLs (e.g.
// direct TMDB image URLs) are the same string regardless of requested size,
// so their tag is identical across sizes. The list path seeds these at
// compatCardImageSize, but Jellyfin-web requests them at "medium" / "original"
// using the same tag — the lookup must succeed.
func TestImageCacheLookupSizedReturnsHTTPPassthroughAcrossSizes(t *testing.T) {
	now := fixedNow()
	cache := NewImageCache(time.Hour, func() time.Time { return now })

	httpURL := "https://image.tmdb.org/t/p/original/poster.jpg"
	cache.RememberSized("item-1", "Primary", httpURL, compatCardImageSize)

	for _, size := range []string{compatCardImageSize, "medium", "original"} {
		got, ok := cache.LookupSized("item-1", "Primary", tagValue(httpURL), size)
		if !ok || got != httpURL {
			t.Fatalf("LookupSized(tag, size=%q) = (%q, %v), want %q", size, got, ok, httpURL)
		}
	}
}

func TestImageCacheRememberSizedUntilCapsRouteExpiry(t *testing.T) {
	now := fixedNow()
	cache := NewImageCache(time.Hour, func() time.Time { return now })
	urlExpiresAt := now.Add(10 * time.Minute)

	cache.RememberSizedUntil("item-1", "Primary", "https://example.com/presigned.jpg", compatCardImageSize, &urlExpiresAt)

	now = now.Add(4 * time.Minute)
	if got, ok := cache.LookupSized("item-1", "Primary", "", compatCardImageSize); !ok || got == "" {
		t.Fatalf("LookupSized before capped expiry = (%q, %v), want hit", got, ok)
	}

	now = now.Add(2 * time.Minute)
	if got, ok := cache.LookupSized("item-1", "Primary", "", compatCardImageSize); ok {
		t.Fatalf("LookupSized after capped expiry = (%q, %v), want miss", got, ok)
	}
}

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

// TestImageCacheKeepsShortLivedSignedURLs covers a short
// s3.metadata_presign_expiry, which accepts any positive duration. A URL that
// lives five minutes or less must still be cached, because the URL-derived
// tags /Search/Hints emits resolve only through the cache. The margin shrinks
// to half the URL's remaining life, and the entry is never served inside it.
func TestImageCacheKeepsShortLivedSignedURLs(t *testing.T) {
	start := fixedNow()
	for _, lifetime := range []time.Duration{time.Minute, 5 * time.Minute} {
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

func TestSignedImageURLExpiry(t *testing.T) {
	signedAt := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		url    string
		want   time.Time
		wantOK bool
	}{
		{name: "artwork capability", url: "/api/v2/artwork/a/poster.webp?exp=1790000000&sig=abc", want: time.Unix(1790000000, 0), wantOK: true},
		{name: "s3 sigv4", url: "https://s3.example.test/b/k.webp?X-Amz-Date=20260923T120000Z&X-Amz-Expires=14400&X-Amz-Signature=abc", want: signedAt.Add(4 * time.Hour), wantOK: true},
		{name: "unsigned passthrough", url: "https://image.tmdb.org/t/p/w342/poster.jpg"},
		{name: "unrelated query", url: "https://cdn.example.test/p.jpg?width=300"},
		{name: "s3 missing expires", url: "https://s3.example.test/k.webp?X-Amz-Date=20260923T120000Z"},
		{name: "malformed exp", url: "/api/v2/artwork/p.webp?exp=soon"},
		{name: "exp outside artwork path", url: "https://cdn.example.test/p.jpg?exp=1"},
		{name: "absolute artwork URL", url: "https://silo.example.test/api/v2/artwork/a/poster.webp?exp=1790000000&sig=abc", want: time.Unix(1790000000, 0), wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := signedImageURLExpiry(tt.url)
			if ok != tt.wantOK || !got.Equal(tt.want) {
				t.Fatalf("signedImageURLExpiry(%q) = (%v, %v), want (%v, %v)", tt.url, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// BenchmarkImageCacheRememberLookup measures one list-path remember plus one
// route lookup of a signed artwork URL.
func BenchmarkImageCacheRememberLookup(b *testing.B) {
	cache := NewImageCache(time.Hour, time.Now)
	routes := make([]string, 4096)
	urls := make([]string, len(routes))
	exp := time.Now().Add(4 * time.Hour).Unix()
	for i := range routes {
		routes[i] = fmt.Sprintf("route-%d", i)
		urls[i] = fmt.Sprintf("/api/v2/artwork/tmdb/movies/%d/poster/w342.abc.webp?exp=%d&sig=abcdefghijklmnop", i, exp)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		j := i % len(routes)
		cache.RememberSized(routes[j], "Primary", urls[j], compatCardImageSize)
		cache.LookupSized(routes[j], "Primary", "", compatCardImageSize)
	}
}

// BenchmarkImageCacheLookupParallel measures concurrent untagged route
// lookups, which share the cache's read lock.
func BenchmarkImageCacheLookupParallel(b *testing.B) {
	cache := NewImageCache(time.Hour, time.Now)
	routes := make([]string, 4096)
	exp := time.Now().Add(4 * time.Hour).Unix()
	for i := range routes {
		routes[i] = fmt.Sprintf("route-%d", i)
		cache.RememberSized(routes[i], "Primary", fmt.Sprintf("/api/v2/artwork/tmdb/movies/%d/poster/w342.abc.webp?exp=%d&sig=abcdefghijklmnop", i, exp), compatCardImageSize)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			cache.LookupSized(routes[i%len(routes)], "Primary", "", compatCardImageSize)
			i++
		}
	})
}
