package watchtogether

import (
	"context"
	"errors"
	"testing"
	"time"

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

func TestMemberStateClassifiesInProgressWatchedUnseen(t *testing.T) {
	provider := &memberProvider{stores: map[int]*memberStore{
		1: {progress: map[string]userstore.WatchProgress{"dune": inProgress("dune", 1200, 9960, "2026-04-02T00:00:00Z")}, watchlist: []userstore.WatchlistEntry{{MediaItemID: "arrival"}}},
		2: {progress: map[string]userstore.WatchProgress{"dune": {MediaItemID: "dune", Completed: true}}, completed: map[string]bool{"arrival": true}},
		3: {},
	}}
	reader := NewMemberStateReader(provider, episodeIndex{}, nil)

	items, err := reader.MemberState(context.Background(), pickerMembers(), []string{"dune", "arrival", "dune", " "})
	if err != nil {
		t.Fatalf("MemberState() error = %v", err)
	}
	if len(items) != 2 || items[0].ContentID != "dune" || items[1].ContentID != "arrival" {
		t.Fatalf("items = %+v, want dune then arrival (deduped, blanks dropped)", items)
	}
	dune := items[0].Members
	if len(dune) != 3 {
		t.Fatalf("dune members = %d", len(dune))
	}
	if dune[0].State != MemberWatchStateInProgress || dune[0].PositionSeconds == nil || *dune[0].PositionSeconds != 1200 || dune[0].DurationSeconds == nil || *dune[0].DurationSeconds != 9960 {
		t.Fatalf("nathan dune = %+v", dune[0])
	}
	if dune[1].State != MemberWatchStateWatched || dune[1].PositionSeconds != nil {
		t.Fatalf("maya dune = %+v", dune[1])
	}
	if dune[2].State != MemberWatchStateUnseen {
		t.Fatalf("theo dune = %+v", dune[2])
	}
	arrival := items[1].Members
	if !arrival[0].OnWatchlist || arrival[0].State != MemberWatchStateUnseen {
		t.Fatalf("nathan arrival = %+v", arrival[0])
	}
	if arrival[1].State != MemberWatchStateWatched || arrival[1].OnWatchlist {
		t.Fatalf("maya arrival (completed history) = %+v", arrival[1])
	}
}

func TestMemberStateSeriesUsesInProgressEpisodes(t *testing.T) {
	provider := &memberProvider{stores: map[int]*memberStore{
		1: {inOrder: []userstore.WatchProgress{inProgress("sev-s2e3", 600, 3000, "2026-04-02T00:00:00Z")}},
		2: {},
		3: {},
	}}
	reader := NewMemberStateReader(provider, episodeIndex{"sev-s2e3": severanceE3}, nil)
	items, err := reader.MemberState(context.Background(), pickerMembers(), []string{"severance"})
	if err != nil {
		t.Fatal(err)
	}
	if got := items[0].Members[0]; got.State != MemberWatchStateInProgress || got.PositionSeconds == nil || *got.PositionSeconds != 600 {
		t.Fatalf("series state from episode progress = %+v", got)
	}
	if got := items[0].Members[1]; got.State != MemberWatchStateUnseen {
		t.Fatalf("maya series = %+v", got)
	}
}

func TestMemberStateReadsOnlyTheGivenMembers(t *testing.T) {
	provider := &memberProvider{stores: map[int]*memberStore{1: {}}}
	reader := NewMemberStateReader(provider, nil, nil)
	items, err := reader.MemberState(context.Background(), pickerMembers()[:1], []string{"dune"})
	if err != nil || len(items) != 1 || len(items[0].Members) != 1 {
		t.Fatalf("items = %+v, err = %v", items, err)
	}
	// A member whose store is unknown surfaces as an error rather than a
	// silent omission.
	if _, err := reader.MemberState(context.Background(), pickerMembers(), []string{"dune"}); err == nil {
		t.Fatal("missing store was swallowed")
	}
}

func TestPickerContinueTogetherRequiresTwoMembers(t *testing.T) {
	provider := &memberProvider{stores: map[int]*memberStore{
		1: {inOrder: []userstore.WatchProgress{inProgress("dune", 100, 9000, "2026-04-03T00:00:00Z"), inProgress("arrival", 50, 7000, "2026-04-01T00:00:00Z")}},
		2: {inOrder: []userstore.WatchProgress{inProgress("dune", 900, 9000, "2026-04-02T00:00:00Z")}},
		3: {inOrder: []userstore.WatchProgress{inProgress("heat", 10, 100, "2026-04-04T00:00:00Z")}},
	}}
	reader := NewMemberStateReader(provider, episodeIndex{}, nil)
	rows, err := reader.Picker(context.Background(), pickerMembers())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.ContinueTogether) != 1 || rows.ContinueTogether[0].ContentID != "dune" {
		t.Fatalf("continue together = %+v, want only dune", rows.ContinueTogether)
	}
	members := rows.ContinueTogether[0].Members
	if len(members) != 2 || members[0].DisplayName != "Nathan" || members[1].DisplayName != "Maya" || *members[1].PositionSeconds != 900 {
		t.Fatalf("dune members = %+v", members)
	}
	if rows.ContinueTogether[0].NextUp != nil {
		t.Fatal("a movie has no next-up")
	}
	if len(rows.WatchlistUnion) != 0 {
		t.Fatalf("watchlist union = %+v", rows.WatchlistUnion)
	}
}

func TestPickerCollapsesEpisodesToSeriesWithModalNextUp(t *testing.T) {
	provider := &memberProvider{stores: map[int]*memberStore{
		1: {inOrder: []userstore.WatchProgress{inProgress("sev-s2e4", 300, 3300, "2026-04-05T00:00:00Z")}},
		2: {inOrder: []userstore.WatchProgress{inProgress("sev-s2e4", 100, 3300, "2026-04-04T00:00:00Z")}},
		3: {inOrder: []userstore.WatchProgress{inProgress("bear-s3e1", 100, 2000, "2026-04-06T00:00:00Z")}},
	}}
	episodes := episodeIndex{"sev-s2e4": severanceE4, "sev-s2e3": severanceE3, "bear-s3e1": bearS3E1}
	nextUp := nextUpIndex{buildMemberKey(3, "theo") + ":severance": {ContentID: "sev-s2e3", SeriesID: "severance", SeasonNumber: 2, EpisodeNumber: 3}}
	reader := NewMemberStateReader(provider, episodes, nextUp)
	rows, err := reader.Picker(context.Background(), pickerMembers())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.ContinueTogether) != 1 || rows.ContinueTogether[0].ContentID != "severance" {
		t.Fatalf("continue together = %+v, want the series, not its episodes", rows.ContinueTogether)
	}
	next := rows.ContinueTogether[0].NextUp
	if next == nil || next.ContentID != "sev-s2e4" || next.MemberCount != 2 || next.Title != "Woe's Hollow" || next.SeasonNumber != 2 || next.EpisodeNumber != 4 {
		t.Fatalf("next up = %+v, want S2E4 for 2 of 3 (Theo's next-up E3 is outvoted)", next)
	}
}

func TestPickerWatchlistUnionOrdersByOverlapThenRecency(t *testing.T) {
	provider := &memberProvider{stores: map[int]*memberStore{
		1: {watchlist: []userstore.WatchlistEntry{{MediaItemID: "arrival", AddedAt: "2026-03-01T00:00:00Z"}, {MediaItemID: "heat", AddedAt: "2026-04-01T00:00:00Z"}}},
		2: {watchlist: []userstore.WatchlistEntry{{MediaItemID: "arrival", AddedAt: "2026-02-01T00:00:00Z"}, {MediaItemID: "alien", AddedAt: "2026-04-09T00:00:00Z"}}},
		3: {},
	}}
	reader := NewMemberStateReader(provider, episodeIndex{}, nil)
	rows, err := reader.Picker(context.Background(), pickerMembers())
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(rows.WatchlistUnion))
	for _, row := range rows.WatchlistUnion {
		got = append(got, row.ContentID)
	}
	if len(got) != 3 || got[0] != "arrival" || got[1] != "alien" || got[2] != "heat" {
		t.Fatalf("watchlist union order = %v, want arrival (2 members), alien (newest), heat", got)
	}
	if len(rows.WatchlistUnion[0].Members) != 2 || rows.WatchlistUnion[1].Members[0].DisplayName != "Maya" {
		t.Fatalf("who tags = %+v", rows.WatchlistUnion)
	}
}

func TestPickerNamesUnwatchedModalNextUp(t *testing.T) {
	members := append(pickerMembers(), MemberSummary{UserID: 4, ProfileID: "guest", DisplayName: "Guest", Connected: true})
	provider := &memberProvider{stores: map[int]*memberStore{
		1: {inOrder: []userstore.WatchProgress{inProgress("sev-s2e4", 300, 3300, "2026-04-05T00:00:00Z")}},
		2: {inOrder: []userstore.WatchProgress{inProgress("sev-s2e4", 100, 3300, "2026-04-04T00:00:00Z")}},
		3: {},
		4: {},
	}}
	episodes := episodeIndex{"sev-s2e4": severanceE4, "sev-s2e3": severanceE3}
	result := catalog.NextUpResult{ContentID: "sev-s2e3", SeriesID: "severance", SeasonNumber: 2, EpisodeNumber: 3}
	nextUp := nextUpIndex{buildMemberKey(3, "theo") + ":severance": result, buildMemberKey(4, "guest") + ":severance": result}
	rows, err := NewMemberStateReader(provider, episodes, nextUp).Picker(t.Context(), members)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.ContinueTogether) != 1 {
		t.Fatalf("continue together = %+v", rows.ContinueTogether)
	}
	next := rows.ContinueTogether[0].NextUp
	if next == nil || next.ContentID != "sev-s2e3" || next.Title != severanceE3.Title || next.MemberCount != 2 {
		t.Fatalf("unwatched next up = %+v, want the earlier tied episode with its title", next)
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

func TestResolvePickerDropsItemsTheCallerCannotSee(t *testing.T) {
	rows := PickerRows{
		ContinueTogether: []PickerEntry{{ContentID: "dune"}, {ContentID: "hidden"}},
		WatchlistUnion:   []PickerEntry{{ContentID: "arrival"}},
	}
	details := detailIndex{"dune": {ContentID: "dune", Title: "Dune: Part Two"}, "arrival": {ContentID: "arrival", Title: "Arrival"}}
	view, err := ResolvePicker(context.Background(), details, catalog.AccessFilter{}, pickerMembers(), rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.ContinueTogether) != 1 || view.ContinueTogether[0].Item.Title != "Dune: Part Two" {
		t.Fatalf("continue together = %+v, want the hidden item dropped", view.ContinueTogether)
	}
	if len(view.WatchlistUnion) != 1 || view.WatchlistUnion[0].Item.ContentID != "arrival" {
		t.Fatalf("watchlist union = %+v", view.WatchlistUnion)
	}
}

func TestConnectedMembersOnlyListsThisNode(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	service, repo, _, _ := stagedServiceForTest(t, now, lobbyRoom(now), "movie-1")
	// A member entry without a connection (disconnected, not yet reaped) is
	// not a connected member.
	service.rooms[repo.room.ID].members[buildMemberKey(9, "ghost")] = &memberState{userID: 9, profileID: "ghost"}
	members, err := service.ConnectedMembers(context.Background(), repo.room.ID, 8, "guest")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || !members[0].IsHost || !members[1].IsSelf {
		t.Fatalf("members = %+v, want host first then self, no ghost", members)
	}
	repo.room.Phase = RoomPhaseEnded
	service.rooms[repo.room.ID].room.Phase = RoomPhaseEnded
	if _, err := service.ConnectedMembers(context.Background(), repo.room.ID, 8, "guest"); !errors.Is(err, ErrRoomClosed) {
		t.Fatalf("ended room: %v", err)
	}
}
