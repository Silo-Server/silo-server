package handlers

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// pagedProgressStore is a user store serving entries, already in listing
// order, by offset page; the profile holds no favorites or other flags.
type pagedProgressStore struct {
	userstore.UserStore
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

func (s *pagedProgressStore) ItemUserFlags(context.Context, string, []string, []string) (map[string]userstore.ItemUserFlags, bool, error) {
	return nil, true, nil
}

func (s *pagedProgressStore) ForUser(context.Context, int) (userstore.UserStore, error) {
	return s, nil
}

func (s *pagedProgressStore) Close() error { return nil }

// droppedPrefixFetcher serves nextUp as the Next Up row and treats every
// entry whose ID starts with "dropped" as a dropped series' episode.
type droppedPrefixFetcher struct {
	nextUp []sections.SectionItemMeta
}

func (f droppedPrefixFetcher) FetchNextUpItems(context.Context, int, string, *int, []int, catalog.AccessFilter, int) ([]*models.MediaItem, map[string]sections.SectionItemMeta, error) {
	items := make([]*models.MediaItem, 0, len(f.nextUp))
	meta := make(map[string]sections.SectionItemMeta, len(f.nextUp))
	for _, m := range f.nextUp {
		id := *m.SeriesID
		items = append(items, &models.MediaItem{ContentID: id})
		meta[id] = m
	}
	return items, meta, nil
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

// Watch Tonight answers the same cards on every read. Continue Watching and
// Next Up titles the recommendations do not hold share one score per source,
// so they go most recent activity first, then by content ID.
func TestWatchTonightOrdersTiedCardsByActivity(t *testing.T) {
	at := time.Date(2026, 11, 1, 20, 0, 0, 0, time.UTC)
	progress := func(id string, hoursAgo int) userstore.WatchProgress {
		updated := at.Add(-time.Duration(hoursAgo) * time.Hour)
		return userstore.WatchProgress{MediaItemID: id, UpdatedAt: updated.Format(time.RFC3339), UpdatedAtTime: updated}
	}
	nextUp := func(id string, hoursAgo int) sections.SectionItemMeta {
		// The fake keys the item by SeriesID; the handler reads no other field.
		return sections.SectionItemMeta{SeriesID: &id, ItemSource: "next_up", SortTimestamp: at.Add(-time.Duration(hoursAgo) * time.Hour)}
	}
	for _, tc := range []struct {
		name     string
		progress []userstore.WatchProgress
		nextUp   []sections.SectionItemMeta
		want     []string
	}{
		{
			name: "Continue Watching by most recent progress, then content ID",
			// The store orders by progress time alone, so tied rows come
			// back in either order.
			progress: []userstore.WatchProgress{progress("cw-new", 0), progress("cw-b", 1), progress("cw-a", 1), progress("cw-old", 3)},
			nextUp:   []sections.SectionItemMeta{nextUp("nu", 0)},
			want:     []string{"cw-new", "cw-a", "cw-b", "cw-old", "nu"},
		},
		{
			name:     "Next Up after Continue Watching by most recent activity",
			progress: []userstore.WatchProgress{progress("cw", 9)},
			nextUp:   []sections.SectionItemMeta{nextUp("nu-mid", 2), nextUp("nu-new", 1), nextUp("nu-old", 5), nextUp("nu-b", 6), nextUp("nu-a", 6)},
			want:     []string{"cw", "nu-new", "nu-mid", "nu-old", "nu-a"},
		},
		{
			name: "a store keeping only whole-second progress times",
			progress: []userstore.WatchProgress{
				{MediaItemID: "cw-a", UpdatedAt: at.Add(-time.Hour).Format(time.RFC3339)},
				{MediaItemID: "cw-b", UpdatedAt: at.Format(time.RFC3339)},
			},
			want: []string{"cw-b", "cw-a"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &pagedProgressStore{entries: tc.progress}
			var items []*models.MediaItem
			for _, p := range tc.progress {
				items = append(items, &models.MediaItem{ContentID: p.MediaItemID, Type: "movie"})
			}
			for _, m := range tc.nextUp {
				items = append(items, &models.MediaItem{ContentID: *m.SeriesID, Type: "episode"})
			}
			h := &RecommendationsHandler{
				storeProvider:       store,
				Fetcher:             stubDiscoverFetcher{items: items},
				WatchTonightFetcher: droppedPrefixFetcher{nextUp: tc.nextUp},
			}
			// The cards pass through a map, whose order changes per run.
			for range 20 {
				view, err := h.WatchTonight(t.Context(), 7, "p1", catalog.AccessFilter{}, watchTonightLimit)
				if err != nil {
					t.Fatal(err)
				}
				got := make([]string, len(view.Items))
				for i, item := range view.Items {
					got[i] = item.ContentID
				}
				if !slices.Equal(got, tc.want) {
					t.Fatalf("cards = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
