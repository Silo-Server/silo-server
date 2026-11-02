package catalog

import (
	"context"
	"maps"
	"slices"
	"strings"
)

// ArtworkPresigner resolves artwork keys in one batch, asking metadata plugins
// for the variant hint.
type ArtworkPresigner interface {
	PresignURLsWithExpiry(ctx context.Context, paths []string, variant string) map[string]ResolvedImageURL
}

// ArtworkBatch collects one response's artwork for batch resolution when its
// slots can ask plugins for different sizes, such as a card poster beside a
// hero backdrop. A cached key or an http(s) URL resolves the same whatever the
// hint, so those share one batch; a plugin-hosted path is resolved once for
// each hint a slot asks for it at.
type ArtworkBatch struct {
	keys   []string
	plugin map[string][]string // hint -> plugin-hosted paths
	seen   map[artworkBatchEntry]struct{}
}

type artworkBatchEntry struct{ path, hint string }

// Add queues path for a slot that asks plugins for hint. Empty paths and the
// "-" placeholder are skipped.
func (b *ArtworkBatch) Add(path, hint string) {
	if path == "" || path == "-" {
		return
	}
	if !isPluginArtworkPath(path) {
		hint = ""
	}
	entry := artworkBatchEntry{path: path, hint: hint}
	if _, ok := b.seen[entry]; ok {
		return
	}
	if b.seen == nil {
		b.seen = make(map[artworkBatchEntry]struct{})
		b.plugin = make(map[string][]string)
	}
	b.seen[entry] = struct{}{}
	if hint == "" {
		b.keys = append(b.keys, path)
		return
	}
	b.plugin[hint] = append(b.plugin[hint], path)
}

// Resolve resolves everything added: cached keys and URLs in one batch at
// keyHint, which only names the resolver's URL cache entry for them, and
// plugin-hosted paths in one batch per hint.
func (b *ArtworkBatch) Resolve(ctx context.Context, presigner ArtworkPresigner, keyHint string) ArtworkURLs {
	urls := ArtworkURLs{plugin: make(map[string]map[string]ResolvedImageURL, len(b.plugin))}
	if len(b.keys) > 0 {
		urls.keys = presigner.PresignURLsWithExpiry(ctx, b.keys, keyHint)
	}
	for _, hint := range slices.Sorted(maps.Keys(b.plugin)) {
		urls.plugin[hint] = presigner.PresignURLsWithExpiry(ctx, b.plugin[hint], hint)
	}
	return urls
}

// ArtworkURLs are an ArtworkBatch's resolved URLs.
type ArtworkURLs struct {
	keys   map[string]ResolvedImageURL
	plugin map[string]map[string]ResolvedImageURL
}

// Get is the URL for path as added for a slot at hint.
func (u ArtworkURLs) Get(path, hint string) ResolvedImageURL {
	if isPluginArtworkPath(path) {
		return u.plugin[hint][path]
	}
	return u.keys[path]
}

// isPluginArtworkPath reports whether a metadata plugin hosts path, so the
// variant hint picks the image it returns.
func isPluginArtworkPath(path string) bool {
	return strings.Contains(path, "://") && !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://")
}
