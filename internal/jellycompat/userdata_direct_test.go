package jellycompat

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

type compatTestUserStoreProvider struct {
	store userstore.UserStore
}

func (p compatTestUserStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (p compatTestUserStoreProvider) Close() error {
	return nil
}

func TestDirectUserDataServiceProgressUsesCompletedHistory(t *testing.T) {
	store := newJellycompatUserStore(t)
	addCompletedHistoryForJellycompatTest(t, store, "movie-history-only")
	service := &directUserDataService{storeProvider: compatTestUserStoreProvider{store: store}}
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}

	progress, err := service.GetProgress(context.Background(), session, "movie-history-only")
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	if progress == nil || !progress.Completed {
		t.Fatalf("GetProgress = %+v, want synthetic completed progress", progress)
	}

	progressMap, err := service.ListProgressByMediaItems(context.Background(), session, []string{"movie-history-only"})
	if err != nil {
		t.Fatalf("ListProgressByMediaItems: %v", err)
	}
	if progressMap["movie-history-only"] == nil || !progressMap["movie-history-only"].Completed {
		t.Fatalf("ListProgressByMediaItems = %+v, want completed history overlay", progressMap)
	}
}

// Filters=IsPlayed lists read completed progress pages; each row must carry
// the stored play date, not the time the row was last written (#2885).
func TestDirectUserDataServiceCompletedListsReportPlayDate(t *testing.T) {
	store := newJellycompatUserStore(t)
	provider := compatTestUserStoreProvider{store: store}
	service := &directUserDataService{storeProvider: provider, watchState: watchstate.NewService(provider)}
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	played := time.Date(2019, 3, 14, 20, 15, 0, 0, time.UTC)
	if err := service.MarkPlayedBatchAt(t.Context(), session, []string{"movie-1"}, played, false); err != nil {
		t.Fatal(err)
	}

	lists := map[string]func() ([]upstreamProgress, error){
		"ListProgress": func() ([]upstreamProgress, error) {
			return service.ListProgress(t.Context(), session, "completed", 10, 0)
		},
		"ListProgressFiltered": func() ([]upstreamProgress, error) {
			return service.ListProgressFiltered(t.Context(), session, "completed", []string{"movie"}, nil, 10, 0)
		},
	}
	for name, list := range lists {
		t.Run(name, func(t *testing.T) {
			entries, err := list()
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("entries = %+v, want one", entries)
			}
			got, err := time.Parse(time.RFC3339Nano, entries[0].UpdatedAt)
			if err != nil || !got.Equal(played) {
				t.Fatalf("UpdatedAt = %q, want play date %s", entries[0].UpdatedAt, played.Format(time.RFC3339))
			}
		})
	}
}

// SortBy=DatePlayed orders the played list by play date, not by the order the
// rows were written, in both directions and across pages (#2978).
func TestItemsPlayedListSortsByPlayDate(t *testing.T) {
	store := newJellycompatUserStore(t)
	provider := compatTestUserStoreProvider{store: store}
	service := &directUserDataService{storeProvider: provider, watchState: watchstate.NewService(provider)}
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	// Marked out of play order. The rows may share a write second, so the
	// write order is whatever ListProgress returns; the two play-date orders
	// are opposite, so at most one of them can match it by chance.
	for _, mark := range []struct {
		id   string
		date time.Time
	}{
		{"movie-2020", time.Date(2020, 6, 1, 12, 30, 0, 0, time.UTC)},
		{"movie-2025", time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"movie-2015", time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		if err := service.MarkPlayedBatchAt(t.Context(), session, []string{mark.id}, mark.date, false); err != nil {
			t.Fatal(err)
		}
	}
	idsOf := func(rows []upstreamProgress, err error) []string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.MediaItemID)
		}
		return out
	}
	h := &ItemsHandler{userData: service}
	ids := func(query itemsQuery, limit, offset int) []string {
		t.Helper()
		return idsOf(h.fetchProgressRows(t.Context(), session, progressStatusCompleted, query, nil, nil, false, limit, offset))
	}
	byDate := func(order string) itemsQuery {
		return itemsQuery{sortExplicit: true, sortByKey: sortKeyDatePlayed, order: order}
	}
	writeOrder := idsOf(service.ListProgress(t.Context(), session, progressStatusCompleted, 10, 0))
	for _, tc := range []struct {
		name          string
		query         itemsQuery
		limit, offset int
		want          []string
	}{
		{"ascending", byDate(sortOrderAscending), 10, 0, []string{"movie-2015", "movie-2020", "movie-2025"}},
		{"descending", byDate(catalog.BrowseOrderDescending), 10, 0, []string{"movie-2025", "movie-2020", "movie-2015"}},
		{"second page", byDate(sortOrderAscending), 2, 2, []string{"movie-2025"}},
		{"no sort keeps write order", itemsQuery{}, 10, 0, writeOrder},
		{"another sort keeps write order", itemsQuery{sortExplicit: true, sortByKey: "datecreated", order: sortOrderAscending}, 10, 0, writeOrder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ids(tc.query, tc.limit, tc.offset); !slices.Equal(got, tc.want) {
				t.Fatalf("rows = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBrowseItemsPlayedFilterUsesCompletedHistory(t *testing.T) {
	store := newJellycompatUserStore(t)
	addCompletedHistoryForJellycompatTest(t, store, "movie-history-only")
	browse := &stubBrowseSource{
		items: []*models.MediaItem{
			{ContentID: "movie-history-only", Type: "movie", Title: "Imported"},
			{ContentID: "movie-unplayed", Type: "movie", Title: "Unplayed"},
		},
		total: 2,
	}
	service := newDirectContentServiceForTest(browse, compatTestUserStoreProvider{store: store})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}

	playedParams := url.Values{}
	playedParams.Set("is_played", "true")
	played, err := service.BrowseItems(context.Background(), session, playedParams)
	if err != nil {
		t.Fatalf("BrowseItems played: %v", err)
	}
	if len(played.Items) != 1 || played.Items[0].ContentID != "movie-history-only" {
		t.Fatalf("played filter items = %+v, want history-only movie", played.Items)
	}

	unplayedParams := url.Values{}
	unplayedParams.Set("is_played", "false")
	unplayed, err := service.BrowseItems(context.Background(), session, unplayedParams)
	if err != nil {
		t.Fatalf("BrowseItems unplayed: %v", err)
	}
	if len(unplayed.Items) != 1 || unplayed.Items[0].ContentID != "movie-unplayed" {
		t.Fatalf("unplayed filter items = %+v, want only unplayed movie", unplayed.Items)
	}
}

func newJellycompatUserStore(t *testing.T) userstore.UserStore {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	store := userdb.NewSQLiteUserStore(db)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-1", Name: "Profile"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	return store
}

func addCompletedHistoryForJellycompatTest(t *testing.T, store userstore.UserStore, mediaItemID string) {
	t.Helper()
	if err := store.AddHistory(context.Background(), userstore.WatchHistoryEntry{
		ProfileID:       "profile-1",
		MediaItemID:     mediaItemID,
		WatchedAt:       "2026-05-04T12:00:00Z",
		DurationSeconds: 7200,
		Completed:       true,
		Source:          userstore.WatchHistorySourceTrakt,
	}); err != nil {
		t.Fatalf("AddHistory: %v", err)
	}
}

// ListResumableProgress keeps the ids with a resume point, newest first, as
// ListProgress("in_progress") orders them.
func TestDirectUserDataServiceListResumableProgress(t *testing.T) {
	store := newJellycompatUserStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	for _, row := range []struct {
		id        string
		position  float64
		completed bool
		at        time.Time
	}{
		{"ep-old", 10, false, now.Add(-2 * time.Hour)},
		{"ep-new", 20, false, now.Add(-time.Hour)},
		{"ep-done", 0, true, now},
		{"other", 30, false, now},
	} {
		if err := store.SetProgressAt(t.Context(), "profile-1", row.id, row.position, 100, row.completed, row.at); err != nil {
			t.Fatal(err)
		}
	}
	service := &directUserDataService{storeProvider: compatTestUserStoreProvider{store: store}}
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	entries, err := service.ListResumableProgress(t.Context(), session, []string{"ep-old", "ep-new", "ep-done", "ep-none"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range entries {
		got = append(got, entry.MediaItemID)
	}
	if fmt.Sprint(got) != "[ep-new ep-old]" {
		t.Fatalf("resumable = %v, want [ep-new ep-old]", got)
	}
}
