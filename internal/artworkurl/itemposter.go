package artworkurl

import (
	"context"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// The item poster capability signs a catalog item's content ID, never a URL:
// the route looks up the item's current poster itself, so a link holder cannot
// choose what the server fetches.
const (
	itemPosterDomain = "silo-item-poster-url-v1"
	itemPosterLabel  = "item-poster-v1"
	// ItemPosterRoute is the route an item poster link names, followed by the
	// content ID.
	ItemPosterRoute = "/api/v2/item-posters/"
	// itemPosterVariant is the stored rung an admin poster names: the card
	// size the session row has always used.
	itemPosterVariant = "w300"
)

// PosterPresigner resolves a plugin image path (a "tmdb://..." path a plugin
// answers for) to the URL it names. It is *catalog.DetailService.
type PosterPresigner interface {
	PresignURL(ctx context.Context, path string, variant string) string
}

// ItemPosters applies the admin poster rule: on the admin surfaces a
// monitoring tool reads, a poster URL is always a path on this server, and
// empty only when the item has no poster. Cached art resolves to a signed
// GET /api/v2/artwork/{key} path on every storage backend, which streams the
// stored object through this server even when catalog reads presign object
// storage URLs. Art that is not cached yet (a provider's URL, or a plugin path
// whose plugin answers with one) gets a signed link to
// GET /api/v2/item-posters/{content_id}, which serves the item's current
// poster when it is requested. A tool that may contact only this Silo server
// can load either.
type ItemPosters struct {
	signer  *Signer
	artwork *Signer
	presign PosterPresigner
}

// NewItemPosters builds the rule with links valid for ttl (the artwork
// lifetime, metadata_presign_expiry). artwork is the server's artwork signer;
// without one (no artwork store), cached art gets an item poster link too.
func NewItemPosters(jwtSecret string, ttl time.Duration, presign PosterPresigner, artwork *Signer) *ItemPosters {
	return &ItemPosters{
		signer:  newSigner(jwtSecret, itemPosterDomain, itemPosterLabel, ItemPosterRoute, ttl),
		artwork: artwork,
		presign: presign,
	}
}

// URL returns the poster URL for the item whose content ID is ownerID and
// whose stored poster path is posterPath. For an episode the owner is its
// series. It returns "" only when posterPath names no poster.
func (p *ItemPosters) URL(ctx context.Context, ownerID, posterPath string) string {
	if p == nil || posterPath == "" || posterPath == "-" {
		return ""
	}
	now := time.Now()
	if url := p.storedURL(posterPath, now); url != "" {
		return url
	}
	return p.Link(ownerID, now)
}

// storedURL returns the signed same-server artwork path of a stored poster's
// card rung issued at now, or "" when posterPath is not a stored key the
// artwork route serves or there is no artwork signer.
func (p *ItemPosters) storedURL(posterPath string, now time.Time) string {
	if p.artwork == nil || !IsStoredArtworkPath(posterPath) {
		return ""
	}
	// Validate the stored path before deriving its rung: the derivation
	// cleans the path, which would hide a malformed row.
	key := StoredPosterVariant(posterPath)
	if blobstore.ValidateKey(posterPath) != nil || blobstore.ValidateKey(key) != nil {
		return ""
	}
	url, _ := p.artwork.Sign(key, now)
	return url
}

// IsSameServerPath reports whether raw is a root-relative path on this server
// ("/...") rather than a scheme-relative ("//host/...") or off-server URL.
func IsSameServerPath(raw string) bool {
	return strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//")
}

// Link signs a getItemPoster link for contentID issued at now, or returns ""
// for an ID the route cannot carry.
func (p *ItemPosters) Link(contentID string, now time.Time) string {
	if p == nil || contentID == "" || strings.Contains(contentID, "/") {
		return ""
	}
	url, _ := p.signer.Sign(contentID, now)
	return url
}

// Verify checks an item poster link's expiry and signature for contentID.
func (p *ItemPosters) Verify(contentID string, exp int64, sig string, now time.Time) error {
	if p == nil {
		return ErrBadSignature
	}
	return p.signer.Verify(contentID, exp, sig, now)
}

// Presign resolves a plugin image path through the catalog's resolver.
func (p *ItemPosters) Presign(ctx context.Context, path string) string {
	if p == nil || p.presign == nil {
		return ""
	}
	return p.presign.PresignURL(ctx, path, "card")
}

// IsStoredArtworkPath reports whether path is a key in Silo's artwork store,
// as opposed to a URL or a plugin-prefixed path a plugin resolves.
func IsStoredArtworkPath(path string) bool {
	return path != "" && path != "-" && !strings.Contains(path, "://") && !strings.HasPrefix(path, "/")
}

// StoredPosterVariant returns the card rung of a stored poster key.
func StoredPosterVariant(path string) string {
	return artworkkey.Variant(path, itemPosterVariant)
}
