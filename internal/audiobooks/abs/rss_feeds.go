package abs

import (
	"context"
	"time"
)

// RSSFeedStore is the storage contract for the abs_rss_feeds table.
type RSSFeedStore interface {
	ListUserFeeds(ctx context.Context, userID, profileID string) ([]RSSFeed, error)
	GetFeed(ctx context.Context, id string) (RSSFeed, error)
	GetFeedBySlug(ctx context.Context, slug string) (RSSFeed, error)
	CreateFeed(ctx context.Context, f RSSFeed) error
	CloseFeed(ctx context.Context, id string) error
}

// RSSFeed mirrors an abs_rss_feeds row.
type RSSFeed struct {
	ID            string
	UserID        string
	ProfileID     string
	LibraryItemID string
	Slug          string
	Minified      bool
	CreatedAt     time.Time
	ClosedAt      *time.Time
}

// rssFeedToABS shapes a feed in the ABS wire format. `url` is built
// from the supplied base URL + slug.
func rssFeedToABS(f RSSFeed, baseURL string) map[string]any {
	url := baseURL + "/feed/" + f.Slug + ".xml"
	return map[string]any{
		"id":            f.ID,
		"userId":        f.UserID,
		"libraryItemId": f.LibraryItemID,
		"slug":          f.Slug,
		"minified":      f.Minified,
		"createdAt":     f.CreatedAt.UnixMilli(),
		"url":           url,
	}
}
