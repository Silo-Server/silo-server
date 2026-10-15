package metadata

import (
	"context"
	"fmt"
	"strings"
)

// matchIgnored reports whether an admin marked contentID as intentionally
// unmatched (media_items.match_ignored_at), for an item a provider does not
// list. While the item is still provisional, automatic matching leaves it
// alone the way it leaves an unmatched split target alone: Identify still
// works, and the item upsert clears the mark once the item is matched.
func (s *MetadataService) matchIgnored(ctx context.Context, contentID string) (bool, error) {
	contentID = strings.TrimSpace(contentID)
	if s == nil || s.dbPool == nil || contentID == "" {
		return false, nil
	}
	var ignored bool
	if err := s.dbPool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM media_items
			WHERE content_id = $1
			  AND match_ignored_at IS NOT NULL
			  AND lower(trim(status)) IN ('pending', 'unmatched', 'ambiguous')
		)
	`, contentID).Scan(&ignored); err != nil {
		return false, fmt.Errorf("checking match ignore for %s: %w", contentID, err)
	}
	return ignored, nil
}

// pinnedUnmatched reports whether an admin left contentID unmatched on
// purpose, by ignoring it or by an unmatched split, so automatic matching
// must leave it alone.
func (s *MetadataService) pinnedUnmatched(ctx context.Context, contentID string) (bool, error) {
	if ignored, err := s.matchIgnored(ctx, contentID); err != nil || ignored {
		return ignored, err
	}
	return s.pinnedUnmatchedBySplit(ctx, contentID)
}
