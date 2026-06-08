package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

type ABSRSSFeedStore struct {
	Pool *pgxpool.Pool
}

var _ abs.RSSFeedStore = (*ABSRSSFeedStore)(nil)

func (s *ABSRSSFeedStore) ListUserFeeds(ctx context.Context, userID, profileID string) ([]abs.RSSFeed, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("abs_rss_feed_store: invalid user id %q: %w", userID, err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, user_id, profile_id, library_item_id, slug, minified, created_at, closed_at
		FROM abs_rss_feeds
		WHERE user_id = $1
		  AND COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
		      = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		  AND closed_at IS NULL
		ORDER BY created_at DESC`,
		uid, profileArg(profileID),
	)
	if err != nil {
		return nil, fmt.Errorf("abs_rss_feed_store: list: %w", err)
	}
	defer rows.Close()
	out := make([]abs.RSSFeed, 0)
	for rows.Next() {
		var f abs.RSSFeed
		var uidScan int
		var profileScan *string
		if err := rows.Scan(&f.ID, &uidScan, &profileScan, &f.LibraryItemID, &f.Slug, &f.Minified, &f.CreatedAt, &f.ClosedAt); err != nil {
			return nil, fmt.Errorf("abs_rss_feed_store: list scan: %w", err)
		}
		f.UserID = strconv.Itoa(uidScan)
		if profileScan != nil {
			f.ProfileID = *profileScan
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abs_rss_feed_store: list rows: %w", err)
	}
	return out, nil
}

func (s *ABSRSSFeedStore) GetFeed(ctx context.Context, id string) (abs.RSSFeed, error) {
	return s.getFeedRow(ctx, "id = $1", id)
}

func (s *ABSRSSFeedStore) GetFeedBySlug(ctx context.Context, slug string) (abs.RSSFeed, error) {
	return s.getFeedRow(ctx, "slug = $1 AND closed_at IS NULL", slug)
}

func (s *ABSRSSFeedStore) getFeedRow(ctx context.Context, where string, arg string) (abs.RSSFeed, error) {
	var f abs.RSSFeed
	var uidScan int
	var profileScan *string
	row := s.Pool.QueryRow(ctx, `
		SELECT id, user_id, profile_id, library_item_id, slug, minified, created_at, closed_at
		FROM abs_rss_feeds WHERE `+where, arg)
	if err := row.Scan(&f.ID, &uidScan, &profileScan, &f.LibraryItemID, &f.Slug, &f.Minified, &f.CreatedAt, &f.ClosedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return abs.RSSFeed{}, abs.ErrNotFound
		}
		return abs.RSSFeed{}, fmt.Errorf("abs_rss_feed_store: get: %w", err)
	}
	f.UserID = strconv.Itoa(uidScan)
	if profileScan != nil {
		f.ProfileID = *profileScan
	}
	return f, nil
}

func (s *ABSRSSFeedStore) CreateFeed(ctx context.Context, f abs.RSSFeed) error {
	uid, err := strconv.Atoi(f.UserID)
	if err != nil {
		return fmt.Errorf("abs_rss_feed_store: invalid user id %q: %w", f.UserID, err)
	}
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO abs_rss_feeds (id, user_id, profile_id, library_item_id, slug, minified)
		VALUES ($1, $2, $3::uuid, $4, $5, $6)`,
		f.ID, uid, profileArg(f.ProfileID), f.LibraryItemID, f.Slug, f.Minified,
	); err != nil {
		return fmt.Errorf("abs_rss_feed_store: create: %w", err)
	}
	return nil
}

func (s *ABSRSSFeedStore) CloseFeed(ctx context.Context, id string) error {
	if _, err := s.Pool.Exec(ctx, `UPDATE abs_rss_feeds SET closed_at = now() WHERE id = $1 AND closed_at IS NULL`, id); err != nil {
		return fmt.Errorf("abs_rss_feed_store: close: %w", err)
	}
	return nil
}
