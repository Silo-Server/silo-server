package artworkurl

import (
	"context"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/netguard"
)

// originProbeKey names no object. FetchContext presigns it only to learn the
// origin the store's direct URLs use.
const originProbeKey = "origin-probe"

// FetchContext returns the context for the server's own request to an
// artwork URL it resolved, sent through a netguard transport. A direct URL to
// store's objects may reach the server's local network, where S3-compatible
// storage often runs. Any other URL reaches public addresses only: catalog
// artwork can still be a metadata provider's URL, kept when caching it
// failed or not yet cached, and a provider's URL is untrusted. See
// docs/architecture/outbound-address-guard.md.
func FetchContext(ctx context.Context, store blobstore.Store, rawURL string) context.Context {
	if isDirectStoreURL(ctx, store, rawURL) {
		return netguard.WithPrivateAccess(ctx)
	}
	return ctx
}

// isDirectStoreURL reports whether rawURL is under the scheme, host, and path
// prefix (bucket, key prefix, or read endpoint path) of the URLs store's
// DirectURL issues. The path must already be clean, so dot segments cannot
// step outside the prefix.
func isDirectStoreURL(ctx context.Context, store blobstore.Store, rawURL string) bool {
	direct, ok := store.(blobstore.DirectURLer)
	if !ok {
		return false
	}
	target, err := url.Parse(rawURL)
	if err != nil || target.Host == "" || target.Path == "" || path.Clean(target.Path) != target.Path {
		return false
	}
	probeURL, _, err := direct.DirectURL(ctx, originProbeKey, time.Minute, 0)
	if err != nil {
		return false
	}
	probe, err := url.Parse(probeURL)
	if err != nil || probe.Host == "" {
		return false
	}
	prefix, ok := strings.CutSuffix(probe.Path, originProbeKey)
	if !ok {
		return false
	}
	return equalASCIIFold(probe.Scheme, target.Scheme) && equalASCIIFold(probe.Host, target.Host) &&
		strings.HasPrefix(target.Path, prefix)
}

// equalASCIIFold compares a and b ignoring ASCII case only. Unicode folding
// (strings.EqualFold) would match host names that resolve as different IDNA
// names, such as σ and ς, so every non-ASCII byte must match exactly.
func equalASCIIFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
