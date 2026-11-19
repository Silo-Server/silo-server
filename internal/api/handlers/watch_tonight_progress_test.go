package handlers

import (
	"context"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type pagedProgressStore struct {
	entries []userstore.WatchProgress
	reads   int
}

func (s *pagedProgressStore) ListProgress(_ context.Context, _, _ string, limit, offset int) ([]userstore.WatchProgress, error) {
	s.reads++
	if offset >= len(s.entries) {
		return nil, nil
	}
	return s.entries[offset:min(offset+limit, len(s.entries))], nil
}

// droppedPrefixFetcher treats every entry whose ID starts with "dropped" as a
// dropped series' episode.
type droppedPrefixFetcher struct{}

func (droppedPrefixFetcher) FetchNextUpItems(context.Context, int, string, *int, []int, catalog.AccessFilter, int) ([]*models.MediaItem, map[string]sections.SectionItemMeta, error) {
	return nil, nil, nil
}

func (droppedPrefixFetcher) FilterDroppedProgress(_ context.Context, _ int, _ string, entries []userstore.WatchProgress) ([]userstore.WatchProgress, error) {
	var kept []userstore.WatchProgress
	for _, entry := range entries {
		if !strings.HasPrefix(entry.MediaItemID, "dropped") {
			kept = append(kept, entry)
		}
	}
	return kept, nil
}

func progressIDs(entries []userstore.WatchProgress) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.MediaItemID)
	}
	return ids
}
