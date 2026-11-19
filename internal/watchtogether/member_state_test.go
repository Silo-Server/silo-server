package watchtogether

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// memberStore is the slice of userstore.UserStore the reader touches. The
// embedded interface panics on anything else, which is the point: a reader
// that starts reading more of a member's history fails loudly here.
type memberStore struct {
	userstore.UserStore
	progress  map[string]userstore.WatchProgress // by media item id
	completed map[string]bool
	watchlist []userstore.WatchlistEntry
	inOrder   []userstore.WatchProgress // in_progress page, newest first
}

func (s *memberStore) ListProgressByMediaItems(_ context.Context, _ string, ids []string) (map[string]userstore.WatchProgress, error) {
	out := map[string]userstore.WatchProgress{}
	for _, id := range ids {
		if row, ok := s.progress[id]; ok {
			out[id] = row
		}
	}
	return out, nil
}

func (s *memberStore) ListCompletedHistoryItems(_ context.Context, q userstore.CompletedHistoryItemQuery) ([]userstore.CompletedHistoryItem, error) {
	out := []userstore.CompletedHistoryItem{}
	for _, id := range q.MediaItemIDs {
		if s.completed[id] {
			out = append(out, userstore.CompletedHistoryItem{MediaItemID: id, WatchedAt: "2026-04-01T00:00:00Z"})
		}
	}
	return out, nil
}

func (s *memberStore) ListWatchlistByMediaItems(_ context.Context, _ string, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, entry := range s.watchlist {
		for _, id := range ids {
			if entry.MediaItemID == id {
				out[id] = true
			}
		}
	}
	return out, nil
}

func (s *memberStore) ListProgress(_ context.Context, _ string, status string, limit, offset int) ([]userstore.WatchProgress, error) {
	if status != "in_progress" || offset != 0 {
		return nil, errors.New("unexpected progress page")
	}
	rows := s.inOrder
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (s *memberStore) ListWatchlist(_ context.Context, _ string, limit, _ int) ([]userstore.WatchlistEntry, error) {
	rows := s.watchlist
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

type memberProvider struct {
	stores map[int]*memberStore
}

func (p *memberProvider) ForUser(_ context.Context, userID int) (userstore.UserStore, error) {
	store := p.stores[userID]
	if store == nil {
		return nil, errors.New("unknown user")
	}
	return store, nil
}
func (p *memberProvider) Close() error { return nil }

type episodeIndex map[string]*models.Episode

func (e episodeIndex) GetByIDs(_ context.Context, ids []string) ([]*models.Episode, error) {
	out := []*models.Episode{}
	for _, id := range ids {
		if ep := e[id]; ep != nil {
			out = append(out, ep)
		}
	}
	return out, nil
}

type nextUpIndex map[string]catalog.NextUpResult // "user:profile:series" → result

func (n nextUpIndex) ListNextUp(_ context.Context, q catalog.NextUpQuery) ([]catalog.NextUpResult, error) {
	if res, ok := n[buildMemberKey(q.UserID, q.ProfileID)+":"+q.SeriesID]; ok {
		return []catalog.NextUpResult{res}, nil
	}
	return nil, nil
}

func inProgress(id string, position, duration float64, updatedAt string) userstore.WatchProgress {
	return userstore.WatchProgress{MediaItemID: id, PositionSeconds: position, DurationSeconds: duration, UpdatedAt: updatedAt}
}

var (
	severanceE3 = &models.Episode{ContentID: "sev-s2e3", SeriesID: "severance", SeasonNumber: 2, EpisodeNumber: 3, Title: "Who Is Alive?"}
	severanceE4 = &models.Episode{ContentID: "sev-s2e4", SeriesID: "severance", SeasonNumber: 2, EpisodeNumber: 4, Title: "Woe's Hollow"}
	bearS3E1    = &models.Episode{ContentID: "bear-s3e1", SeriesID: "the-bear", SeasonNumber: 3, EpisodeNumber: 1, Title: "Tomorrow"}
)

func pickerMembers() []MemberSummary {
	return []MemberSummary{
		{UserID: 1, ProfileID: "nathan", DisplayName: "Nathan", IsHost: true, Connected: true},
		{UserID: 2, ProfileID: "maya", DisplayName: "Maya", Connected: true},
		{UserID: 3, ProfileID: "theo", DisplayName: "Theo", Connected: true},
	}
}

func TestPickerWatchlistCollapsesEpisodesAndCountsEachMemberOnce(t *testing.T) {
	provider := &memberProvider{stores: map[int]*memberStore{
		1: {watchlist: []userstore.WatchlistEntry{
			{MediaItemID: "sev-s2e3", AddedAt: "2026-04-05T00:00:00Z"},
			{MediaItemID: "sev-s2e4", AddedAt: "2026-04-04T00:00:00Z"},
			{MediaItemID: "severance", AddedAt: "2026-04-03T00:00:00Z"},
		}},
		2: {watchlist: []userstore.WatchlistEntry{{MediaItemID: "sev-s2e4", AddedAt: "2026-04-04T00:00:00Z"}}},
		3: {watchlist: []userstore.WatchlistEntry{{MediaItemID: "arrival", AddedAt: "2026-04-06T00:00:00Z"}}},
	}}
	reader := NewMemberStateReader(provider, episodeIndex{"sev-s2e3": severanceE3, "sev-s2e4": severanceE4}, nil)
	rows, err := reader.Picker(t.Context(), pickerMembers())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.WatchlistUnion) != 2 || rows.WatchlistUnion[0].ContentID != "severance" || len(rows.WatchlistUnion[0].Members) != 2 {
		t.Fatalf("watchlist union = %+v, want the series with two distinct members then arrival", rows.WatchlistUnion)
	}
	details := detailIndex{"severance": {ContentID: "severance", Title: "Severance"}, "arrival": {ContentID: "arrival", Title: "Arrival"}}
	view, err := ResolvePicker(t.Context(), details, catalog.AccessFilter{}, pickerMembers(), rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.WatchlistUnion) != 2 || view.WatchlistUnion[0].Item.ContentID != "severance" {
		t.Fatalf("watchlisted episodes disappeared during card lookup: %+v", view.WatchlistUnion)
	}
}

type detailIndex map[string]*catalog.ItemDetail

func (d detailIndex) GetItemCardsByIDs(_ context.Context, ids []string, _ catalog.AccessFilter) (map[string]*catalog.ItemDetail, error) {
	out := map[string]*catalog.ItemDetail{}
	for _, id := range ids {
		if item := d[id]; item != nil {
			out[id] = item
		}
	}
	return out, nil
}
