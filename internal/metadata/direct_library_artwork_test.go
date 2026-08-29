package metadata

import (
	"testing"
	"time"
)

// A direct-library fingerprint is keyed on metadata only, so an expiry is the
// only thing that ever re-hashes a file whose bytes changed while its path,
// size, and mtime stayed put.
func TestDirectLibraryFingerprintCacheEntriesExpire(t *testing.T) {
	resolver := &DirectLibraryArtworkResolver{fingerprints: make(map[string]directLibraryFingerprintEntry)}
	const key = "/library/Movie/poster.jpg\x001700000000000000000\x0012345"

	resolver.storeFingerprint(key, "first")
	if got, ok := resolver.cachedFingerprint(key); !ok || got != "first" {
		t.Fatalf("fresh cache lookup = %q, %v; want %q, true", got, ok, "first")
	}

	resolver.mu.Lock()
	entry := resolver.fingerprints[key]
	entry.hashedAt = entry.hashedAt.Add(-directLibraryFingerprintTTL - time.Second)
	resolver.fingerprints[key] = entry
	resolver.mu.Unlock()

	if got, ok := resolver.cachedFingerprint(key); ok {
		t.Fatalf("expired cache lookup = %q, true; want a miss", got)
	}
	resolver.mu.Lock()
	_, retained := resolver.fingerprints[key]
	resolver.mu.Unlock()
	if retained {
		t.Fatal("expired entry was retained in the cache")
	}

	resolver.storeFingerprint(key, "second")
	if got, ok := resolver.cachedFingerprint(key); !ok || got != "second" {
		t.Fatalf("re-hashed cache lookup = %q, %v; want %q, true", got, ok, "second")
	}
}
