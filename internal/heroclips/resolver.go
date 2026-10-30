package heroclips

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
)

// ClipPathSource reads which items have a published clip, and where it lives.
type ClipPathSource interface {
	ClipPathsFor(ctx context.Context, contentIDs []string) (map[string]string, error)
}

// ClipURLTTL is how long a clip's URL has to stay good. A hero clip plays for
// minutes and a media element re-requests as it plays, so the URL outlives the
// artwork lifetime a poster is served with.
const ClipURLTTL = 12 * time.Hour

// URLResolver turns published clips into URLs a client can fetch, through the
// same signing the item's artwork goes through.
type URLResolver struct {
	Clips    ClipPathSource
	Resolver artworkurl.Resolver
	TTL      time.Duration
}

// HeroVideoURLs returns the URL of each item's clip, keyed by content id, for
// the items that have one. An item without a clip is simply absent, which is
// how the hero knows to keep its backdrop.
//
// A failure is the caller's to log and ignore: a hero without a clip is the
// ordinary case, not an error.
func (r URLResolver) HeroVideoURLs(ctx context.Context, contentIDs []string) (map[string]string, error) {
	if r.Clips == nil || r.Resolver == nil || len(contentIDs) == 0 {
		return nil, nil
	}

	paths, err := r.Clips.ClipPathsFor(ctx, contentIDs)
	if err != nil {
		return nil, err
	}

	ttl := r.TTL
	if ttl <= 0 {
		ttl = ClipURLTTL
	}

	urls := make(map[string]string, len(paths))
	for contentID, path := range paths {
		resolved, ok := artworkurl.ResolveURLFor(ctx, r.Resolver, path, ttl)
		if !ok || resolved.URL == "" {
			// One unsignable path must not cost the row its other clips: the
			// item keeps its backdrop like any title without a clip.
			continue
		}
		urls[contentID] = resolved.URL
	}

	return urls, nil
}
