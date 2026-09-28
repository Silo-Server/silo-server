package handlers

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type memberStateCatalog struct {
	filter catalog.AccessFilter
	err    error
}

func (c *memberStateCatalog) GetSearchItemsByIDsWithAccess(_ context.Context, _ []string, filter catalog.AccessFilter) ([]*models.MediaItem, error) {
	c.filter = filter
	return []*models.MediaItem{{ContentID: "movie"}, {ContentID: "episode"}}, c.err
}

func TestRoomMemberStateFiltersContentBeforeReadingMembers(t *testing.T) {
	service := watchtogether.NewService(new(roomSocketRepo), nil, nil, nil, nil, nil)
	t.Cleanup(service.Close)
	if _, _, err := service.Connect(t.Context(), "room", 8, "guest", memberStateConn{}); err != nil {
		t.Fatal(err)
	}
	provider := &memberStateProvider{store: new(memberStateStore)}
	lookup := new(memberStateCatalog)
	h := &WatchTogetherHandler{Service: service, MemberState: watchtogether.NewMemberStateReader(provider, nil, nil), MemberStateCatalog: lookup}
	filter := catalog.AccessFilter{UserID: 7, ProfileID: "restricted", AllowedLibraryIDs: []int{1}, DisabledLibraryIDs: []int{2}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}
	_, items, err := h.RoomMemberState(t.Context(), "room", 7, "restricted", []string{"hidden-movie", "episode", "hidden-episode", "movie", "episode", "missing"}, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ContentID != "episode" || items[1].ContentID != "movie" || !reflect.DeepEqual(lookup.filter, filter) {
		t.Fatalf("access filter = %+v; items = %+v", lookup.filter, items)
	}
	for _, item := range items {
		if len(item.Members) != 1 || item.Members[0].State != watchtogether.MemberWatchStateWatched || !item.Members[0].OnWatchlist {
			t.Fatalf("accessible watch state missing: %+v", item)
		}
	}
	if !reflect.DeepEqual(provider.store.ids, []string{"episode", "movie"}) {
		t.Fatalf("inaccessible IDs reached member store: %v", provider.store.ids)
	}
	lookup.err = errors.New("catalog unavailable")
	if _, _, err := h.RoomMemberState(t.Context(), "room", 7, "restricted", []string{"movie"}, filter); !errors.Is(err, lookup.err) {
		t.Fatalf("access failure did not fail closed: %v", err)
	}
	h.MemberStateCatalog = nil
	if _, _, err := h.RoomMemberState(t.Context(), "room", 7, "restricted", []string{"movie"}, filter); err == nil {
		t.Fatal("missing catalog did not fail closed")
	}
}

type memberStateConn struct{}

func (memberStateConn) WriteJSON(any) error { return nil }
func (memberStateConn) Close() error        { return nil }

type memberStateProvider struct {
	userstore.UserStoreProvider
	store *memberStateStore
}

func (p *memberStateProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

type memberStateStore struct {
	userstore.UserStore
	ids []string
}

func (s *memberStateStore) ListProgressByMediaItems(_ context.Context, _ string, ids []string) (map[string]userstore.WatchProgress, error) {
	s.ids = ids
	progress := make(map[string]userstore.WatchProgress, len(ids))
	for _, id := range ids {
		progress[id] = userstore.WatchProgress{MediaItemID: id, Completed: true}
	}
	return progress, nil
}

func (*memberStateStore) ListCompletedHistoryItems(context.Context, userstore.CompletedHistoryItemQuery) ([]userstore.CompletedHistoryItem, error) {
	return nil, nil
}

func (*memberStateStore) ListWatchlistByMediaItems(_ context.Context, _ string, ids []string) (map[string]bool, error) {
	watchlist := make(map[string]bool, len(ids))
	for _, id := range ids {
		watchlist[id] = true
	}
	return watchlist, nil
}

func (*memberStateStore) ListProgress(context.Context, string, string, int, int) ([]userstore.WatchProgress, error) {
	return nil, nil
}
