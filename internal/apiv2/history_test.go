package apiv2

import (
	"context"
	"net/http"
	"sort"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// fakeHistory stands in for handlers.PersonalDataHandler: a keyset page over
// rows kept in (watched_at DESC, id DESC) order, cards rendered for every
// entry whose id it knows, and the removal command recorded as the seam
// received it.
type fakeHistory struct {
	entries []userstore.WatchHistoryEntry
	cards   map[string]handlers.CollectionItemView
	removed [][]handlers.HistoryRemovalTarget
	pages   []*userstore.HistoryKey
	err     error
}

func historyRows() []userstore.WatchHistoryEntry {
	return []userstore.WatchHistoryEntry{
		{ID: "h3", ProfileID: "p-owner", MediaItemID: "episode:heat-s01e02", WatchedAt: "2026-01-03T00:00:00Z", DurationSeconds: 2600, Completed: true, Source: userstore.WatchHistorySourcePlayback},
		{ID: "h2", ProfileID: "p-owner", MediaItemID: "movie:heat-1995", WatchedAt: "2026-01-02T03:04:05Z", DurationSeconds: 5400, Completed: true, Source: userstore.WatchHistorySourceManual},
		{ID: "h1", ProfileID: "p-owner", MediaItemID: "movie:hidden", WatchedAt: "2026-01-01T00:00:00Z", DurationSeconds: 100, Completed: false},
	}
}

func (f *fakeHistory) HistoryPage(_ context.Context, _ int, profileID string, after *userstore.HistoryKey, limit int) ([]userstore.WatchHistoryEntry, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.pages = append(f.pages, after)
	sorted := append([]userstore.WatchHistoryEntry(nil), f.entries...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].WatchedAt != sorted[j].WatchedAt {
			return sorted[i].WatchedAt > sorted[j].WatchedAt
		}
		return sorted[i].ID > sorted[j].ID
	})
	var rows []userstore.WatchHistoryEntry
	for _, e := range sorted {
		if e.ProfileID != profileID {
			continue
		}
		if after != nil && (e.WatchedAt > after.WatchedAt || (e.WatchedAt == after.WatchedAt && e.ID >= after.ID)) {
			continue
		}
		rows = append(rows, e)
		if len(rows) == limit {
			break
		}
	}
	return rows, nil
}

func (f *fakeHistory) HistoryPageCards(_ context.Context, _ handlers.SectionViewer, entries []userstore.WatchHistoryEntry) ([]handlers.HistoryCardView, error) {
	latest := make(map[string]userstore.WatchHistoryEntry)
	for _, e := range f.entries {
		if card, ok := f.cards[e.MediaItemID]; ok {
			old := latest[card.ContentID]
			if e.WatchedAt > old.WatchedAt || (e.WatchedAt == old.WatchedAt && e.ID > old.ID) {
				latest[card.ContentID] = e
			}
		}
	}
	var out []handlers.HistoryCardView
	for _, e := range entries {
		if card, ok := f.cards[e.MediaItemID]; ok && latest[card.ContentID].ID == e.ID {
			out = append(out, handlers.HistoryCardView{Item: card, Entry: e})
		}
	}
	return out, nil
}

func (f *fakeHistory) RemoveHistory(_ context.Context, _ int, _ string, _ catalogpkg.AccessFilter, targets []handlers.HistoryRemovalTarget) error {
	if f.err != nil {
		return f.err
	}
	for _, t := range targets {
		if t.ContentID == "missing" {
			return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "History target not found"}
		}
	}
	f.removed = append(f.removed, targets)
	return nil
}

func newFakeHistory() *fakeHistory {
	return &fakeHistory{
		entries: historyRows(),
		cards: map[string]handlers.CollectionItemView{
			"episode:heat-s01e02": {ContentID: "series:heat", Type: "series", Title: "Heat: The Series", Genres: []string{"Crime"}, Status: "matched"},
			"movie:heat-1995":     {ContentID: "movie:heat-1995", Type: "movie", Title: "Heat", Year: 1995, Genres: []string{"Crime"}, Status: "matched"},
		},
	}
}

type historyPage struct {
	Items []struct {
		ContentID string `json:"content_id"`
		Type      string `json:"type"`
		Watch     struct {
			MediaItemID string  `json:"media_item_id"`
			WatchedAt   string  `json:"watched_at"`
			Duration    float64 `json:"duration_seconds"`
			Completed   bool    `json:"completed"`
			Source      string  `json:"source"`
		} `json:"watch"`
	} `json:"items"`
	Page struct {
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	} `json:"page"`
}
