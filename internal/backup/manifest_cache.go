package backup

import (
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// maxCachedManifestBytes bounds the manifests the listing cache holds, by
// their stored size: a manifest may be up to 16 MiB.
const maxCachedManifestBytes = 32 << 20

// manifestCache holds the manifests the backup listing read and checked, so
// a page that polls while a backup runs lists the destination without
// reading every manifest again. A manifest is never rewritten once its
// backup completes; an entry stands for the object at its location while
// the listing reports the same size, modification time, and ETag, and is
// read again otherwise. Only reads that succeeded are kept: a manifest that
// failed to read or verify is read again on the next listing. Entries of
// folders a listing no longer sees are dropped.
type manifestCache struct {
	mu      sync.Mutex
	entries map[string]cachedManifest
	bytes   int64
}

type cachedManifest struct {
	// scope is the location of the server folder it was listed under.
	scope    string
	size     int64
	modTime  time.Time
	etag     string
	manifest *Manifest
}

// get returns the manifest cached for the object at location, if the
// listing still reports it unchanged.
func (c *manifestCache) get(location string, obj blobstore.ObjectInfo) (*Manifest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[location]
	if !ok || e.size != obj.Size || !e.modTime.Equal(obj.ModTime) || e.etag != obj.ETag {
		return nil, false
	}
	return e.manifest, true
}

// put caches m, read from the object at location, unless that would hold
// more than maxCachedManifestBytes.
func (c *manifestCache) put(scope, location string, obj blobstore.ObjectInfo, m *Manifest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]cachedManifest{}
	}
	if old, ok := c.entries[location]; ok {
		c.bytes -= old.size
		delete(c.entries, location)
	}
	if obj.Size < 0 || c.bytes+obj.Size > maxCachedManifestBytes {
		return
	}
	c.entries[location] = cachedManifest{scope: scope, size: obj.Size, modTime: obj.ModTime, etag: obj.ETag, manifest: m}
	c.bytes += obj.Size
}

// keepOnly drops the entries listed under scope whose location seen does
// not hold: their folders are gone, or their manifests were not listed.
func (c *manifestCache) keepOnly(scope string, seen map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for location, e := range c.entries {
		if e.scope == scope && !seen[location] {
			c.bytes -= e.size
			delete(c.entries, location)
		}
	}
}

// cacheLocation names key at destination t for the cache: the
// destination's kind and store, and where key is in it.
func cacheLocation(t target, key string) string {
	store := ""
	if d, ok := t.Destination.(StoreDestination); ok {
		store = blobstore.Location(d.Store)
	}
	return t.Name() + "|" + store + "|" + t.Location(key)
}
