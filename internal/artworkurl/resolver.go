package artworkurl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkstore"
)

// Resolver turns a logical artwork key into a URL a client can fetch, hiding
// which backend holds the object.
//
// Every backend is delivered through the signed native artwork route. Delivery
// surfaces — the catalog image resolver, jellycompat, admin responses — ask
// for a URL and get one that works without testing for a configured bucket.
type Resolver struct {
	signer *Signer
}

// NewResolver builds a resolver that delivers artwork through signed native
// routes.
func NewResolver(signer *Signer) (*Resolver, error) {
	if signer == nil {
		return nil, errors.New("artworkurl: a signer is required to resolve artwork urls")
	}
	return &Resolver{signer: signer}, nil
}

// ResolveTargetURL is the target-aware owning API. It always returns Silo's
// target capability, including for S3 and source references.
func (r *Resolver) ResolveTargetURL(ctx context.Context, target Target, variant string) (artworkstore.ResolvedURL, error) {
	if r == nil {
		return artworkstore.ResolvedURL{}, errors.New("artworkurl: artwork url resolution is not configured")
	}
	// An empty reference means the catalog row has no artwork selected in this
	// slot. Minting a capability anyway would hand clients a URL that can only
	// serve the placeholder, where the API contract for absent artwork is an
	// empty URL field — so refuse here and let batch resolution omit the entry.
	// Lost artwork is different: its reference is still selected, so its
	// capability mints and delivery serves fallback bytes or the placeholder.
	if target.Reference == "" {
		return artworkstore.ResolvedURL{}, fmt.Errorf("%w: artwork target has no selected reference", artworkstore.ErrInvalidKey)
	}
	if err := target.Validate(); err != nil {
		return artworkstore.ResolvedURL{}, err
	}
	return r.signer.SignTarget(target, variant, time.Now())
}

func (r *Resolver) ResolveTargetURLs(ctx context.Context, targets []Target, variant string) map[string]artworkstore.ResolvedURL {
	resolved := make(map[string]artworkstore.ResolvedURL, len(targets))
	for _, target := range targets {
		value, err := r.ResolveTargetURL(ctx, target, variant)
		if err == nil && value.URL != "" {
			resolved[target.CacheKey()] = value
		}
	}
	return resolved
}

func (r *Resolver) ResolveTargetRequests(ctx context.Context, requests []TargetRequest) map[string]artworkstore.ResolvedURL {
	resolved := make(map[string]artworkstore.ResolvedURL, len(requests))
	for _, request := range requests {
		value, err := r.ResolveTargetURL(ctx, request.Target, request.Variant)
		if err == nil && value.URL != "" {
			resolved[request.CacheKey()] = value
		}
	}
	return resolved
}

// ResolveArtworkURL returns a fetchable URL for one direct-library reference.
func (r *Resolver) ResolveArtworkURL(ctx context.Context, key string) (artworkstore.ResolvedURL, error) {
	if r == nil {
		return artworkstore.ResolvedURL{}, errors.New("artworkurl: artwork url resolution is not configured")
	}
	if strings.HasPrefix(key, LibraryReferencePrefix) {
		return r.signer.SignLibraryReference(key, time.Now())
	}
	return artworkstore.ResolvedURL{}, fmt.Errorf("%w: artwork resolution requires a direct-library reference", artworkstore.ErrInvalidKey)
}

// ResolveArtworkURLs resolves a batch of logical keys. Keys that cannot be
// resolved are absent from the result rather than mapped to an empty URL, so a
// caller never publishes a broken reference; the reason is logged once per key.
func (r *Resolver) ResolveArtworkURLs(ctx context.Context, keys []string) map[string]artworkstore.ResolvedURL {
	resolved := make(map[string]artworkstore.ResolvedURL, len(keys))
	if r == nil {
		return resolved
	}
	for _, key := range keys {
		url, err := r.ResolveArtworkURL(ctx, key)
		if err != nil {
			if errors.Is(err, artworkstore.ErrInvalidKey) {
				// Not every stored image reference is a store key: bundled
				// asset paths and legacy values reach here too. They are not
				// an operational problem, only unresolvable.
				slog.DebugContext(ctx, "skipping unresolvable artwork reference",
					"component", "artwork", "key", key, "error", err)
				continue
			}
			slog.ErrorContext(ctx, "artwork url resolution failed",
				"component", "artwork", "key", key, "error", err)
			continue
		}
		if url.URL == "" {
			continue
		}
		resolved[key] = url
	}
	return resolved
}
