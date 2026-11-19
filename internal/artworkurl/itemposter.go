package artworkurl

import (
	"context"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
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

// PosterPresigner resolves a stored artwork path to the URL clients load. It
// is *catalog.DetailService.
type PosterPresigner interface {
	PresignURL(ctx context.Context, path string, variant string) string
}

// ItemPosters applies the admin poster rule: on the admin surfaces a
// monitoring tool reads, a poster URL is always one this server issued, and
// empty only when the item has no poster. Cached art resolves to its stored
// object's URL, as everywhere else. Art that is not cached yet (a provider's
// URL, or a plugin path whose plugin answers with one) gets a signed link to
// GET /api/v2/item-posters/{content_id}, which serves the item's current
// poster when it is requested. A tool on a network that cannot reach the
// providers' image hosts can load either.
type ItemPosters struct {
	signer  *Signer
	presign PosterPresigner
}

// NewItemPosters builds the rule with links valid for ttl (the artwork
// lifetime, metadata_presign_expiry).
func NewItemPosters(jwtSecret string, ttl time.Duration, presign PosterPresigner) *ItemPosters {
	return &ItemPosters{
		signer:  newSigner(jwtSecret, itemPosterDomain, itemPosterLabel, ItemPosterRoute, ttl),
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
	if IsStoredArtworkPath(posterPath) {
		if url := p.Presign(ctx, StoredPosterVariant(posterPath)); url != "" {
			return url
		}
	}
	return p.Link(ownerID, time.Now())
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

// Presign resolves a stored path the way URL does.
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
