package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// markItemRepo answers the items it holds; anything else is not found.
type markItemRepo struct {
	items map[string]*models.MediaItem
}

func (r markItemRepo) GetByID(_ context.Context, id string) (*models.MediaItem, error) {
	if item, ok := r.items[id]; ok {
		return item, nil
	}
	return nil, catalog.ErrItemNotFound
}

func (r markItemRepo) GetByIDs(_ context.Context, ids []string) ([]*models.MediaItem, error) {
	var out []*models.MediaItem
	for _, id := range ids {
		if item, ok := r.items[id]; ok {
			out = append(out, item)
		}
	}
	return out, nil
}

func (r markItemRepo) EnsureAccessible(_ context.Context, id string, _ catalog.AccessFilter) error {
	if _, ok := r.items[id]; ok {
		return nil
	}
	return catalog.ErrItemNotFound
}

func (r markItemRepo) EnsureAccessibleIDs(ctx context.Context, ids []string, filter catalog.AccessFilter) (map[string]bool, error) {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = r.EnsureAccessible(ctx, id, filter) == nil
	}
	return out, nil
}

// failingItemRepo is a markItemRepo whose access check or item lookup fails
// with the given error instead of answering.
type failingItemRepo struct {
	markItemRepo
	accessErr, getErr error
}

func (r failingItemRepo) EnsureAccessible(ctx context.Context, id string, filter catalog.AccessFilter) error {
	if r.accessErr != nil {
		return r.accessErr
	}
	return r.markItemRepo.EnsureAccessible(ctx, id, filter)
}

func (r failingItemRepo) GetByID(ctx context.Context, id string) (*models.MediaItem, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.markItemRepo.GetByID(ctx, id)
}

// Marking takes movies and series only, and tells recommendations once per
// change: a repeated mark or unmark changes nothing and notifies nobody.
func TestMarkNotInterested(t *testing.T) {
	store := newHouseholdTestStore(t)
	h := NewPersonalDataHandler(testUserStoreProvider{store: store}, markItemRepo{items: map[string]*models.MediaItem{
		"movie:heat":      {ContentID: "movie:heat", Type: "movie"},
		"series:show":     {ContentID: "series:show", Type: "series"},
		"season:show-1":   {ContentID: "season:show-1", Type: "season"},
		"audiobook:dune":  {ContentID: "audiobook:dune", Type: "audiobook"},
		"episode:show-11": {ContentID: "episode:show-11", Type: "episode"},
	}})
	notifier := &countingSignalsNotifier{}
	h.SetSignalsChangedNotifier(notifier)
	viewer := PersonalListViewer{UserID: 1, ProfileID: "p1"}
	ctx := context.Background()

	for _, id := range []string{"movie:heat", "series:show"} {
		for range 2 {
			if err := h.MarkNotInterested(ctx, viewer, id); err != nil {
				t.Fatalf("mark %s: %v", id, err)
			}
		}
	}
	if notifier.calls != 2 {
		t.Fatalf("notifications = %d, want one per new mark", notifier.calls)
	}
	for _, id := range []string{"season:show-1", "audiobook:dune", "episode:show-11"} {
		var apiErr *APIError
		if err := h.MarkNotInterested(ctx, viewer, id); !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnprocessableEntity {
			t.Fatalf("mark %s = %v, want 422", id, err)
		}
	}
	var apiErr *APIError
	if err := h.MarkNotInterested(ctx, viewer, "movie:missing"); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("mark of an unknown item = %v, want 404", err)
	}

	entry, found, err := h.GetNotInterested(ctx, viewer, "movie:heat")
	if err != nil || !found || entry.MediaItemID != "movie:heat" {
		t.Fatalf("get = %+v, %v, %v", entry, found, err)
	}
	// Another profile on the account has no marks.
	if _, found, _ := h.GetNotInterested(ctx, PersonalListViewer{UserID: 1, ProfileID: "p2"}, "movie:heat"); found {
		t.Fatal("another profile sees p1's mark")
	}

	for range 2 {
		if err := h.UnmarkNotInterested(ctx, viewer, "movie:heat"); err != nil {
			t.Fatalf("unmark: %v", err)
		}
	}
	if notifier.calls != 3 {
		t.Fatalf("notifications = %d after unmark, want 3", notifier.calls)
	}
}

// A catalog query that fails is a server error, not a missing item: a client
// told that an item it can see is gone drops it. The not-found answer stays a
// 404 (TestMarkNotInterested), as does an ID Postgres cannot take as text,
// which names no item and is never queried.
func TestPersonalListLookupFailureIsServerError(t *testing.T) {
	failure := fmt.Errorf("checking item access: %w", context.DeadlineExceeded)
	items := markItemRepo{items: map[string]*models.MediaItem{"movie:heat": {ContentID: "movie:heat", Type: "movie"}}}
	viewer := PersonalListViewer{UserID: 1, ProfileID: "p1"}
	ctx := context.Background()
	getFavorite := func(id string) func(*PersonalDataHandler) error {
		return func(h *PersonalDataHandler) error {
			_, _, err := h.GetFavorite(ctx, viewer, id)
			return err
		}
	}
	for _, tc := range []struct {
		name string
		repo failingItemRepo
		call func(*PersonalDataHandler) error
		want int
	}{
		{"access check", failingItemRepo{markItemRepo: items, accessErr: failure}, getFavorite("movie:heat"), http.StatusInternalServerError},
		{"item lookup", failingItemRepo{markItemRepo: items, getErr: failure}, func(h *PersonalDataHandler) error {
			return h.MarkNotInterested(ctx, viewer, "movie:heat")
		}, http.StatusInternalServerError},
		{"malformed id", failingItemRepo{markItemRepo: items, accessErr: failure}, getFavorite("movie:\x00heat"), http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewPersonalDataHandler(testUserStoreProvider{store: newHouseholdTestStore(t)}, tc.repo)
			var apiErr *APIError
			if err := tc.call(h); !errors.As(err, &apiErr) || apiErr.Status != tc.want {
				t.Fatalf("err = %v, want status %d", err, tc.want)
			}
		})
	}
}

// idsFailingItemRepo is a markItemRepo whose batch access check fails.
type idsFailingItemRepo struct {
	markItemRepo
}

func (idsFailingItemRepo) EnsureAccessibleIDs(context.Context, []string, catalog.AccessFilter) (map[string]bool, error) {
	return nil, context.DeadlineExceeded
}

// A personal list page answers only entries the viewer may see, reading past
// runs of hidden entries longer than one store batch. The v2 listings build
// their cursor from the last entry of a page and their has_more from a probe
// entry, so neither can name a hidden title, and hidden entries never show
// up as an empty page.
func TestPersonalListPagesSkipHiddenEntries(t *testing.T) {
	db, err := userdb.NewUserDB(filepath.Join(t.TempDir(), "user.db"), 1)
	if err != nil {
		t.Fatalf("open user database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := userdb.NewSQLiteUserStore(db.DB)

	// Newest first: one visible entry, 60 hidden, one visible, 3 hidden, one
	// visible. Only the visible items are in the item repository.
	items := markItemRepo{items: map[string]*models.MediaItem{}}
	var ids []string
	add := func(id string, visible bool) {
		ids = append(ids, id)
		if visible {
			items.items[id] = &models.MediaItem{ContentID: id, Type: "movie"}
		}
	}
	add("movie:visible-1", true)
	for i := range 60 {
		add(fmt.Sprintf("movie:hidden-a%02d", i), false)
	}
	add("movie:visible-2", true)
	for i := range 3 {
		add(fmt.Sprintf("movie:hidden-b%d", i), false)
	}
	add("movie:visible-3", true)
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, table := range []string{"favorites", "watchlist", "not_interested"} {
		for i, id := range ids {
			if _, err := db.DB.Exec(`INSERT INTO `+table+` (profile_id, media_item_id, added_at) VALUES ('p1', ?, ?)`,
				id, start.Add(-time.Duration(i)*time.Minute).Format(time.RFC3339)); err != nil {
				t.Fatalf("seed %s: %v", table, err)
			}
		}
	}

	viewer := PersonalListViewer{UserID: 1, ProfileID: "p1"}
	ctx := context.Background()
	type page struct {
		entries []userstore.ListKey
		cards   int
	}
	lists := map[string]func(h *PersonalDataHandler, after *userstore.ListKey, limit int) (page, error){
		"favorites": func(h *PersonalDataHandler, after *userstore.ListKey, limit int) (page, error) {
			entries, cards, err := h.ListFavoritesPage(ctx, viewer, after, limit)
			out := page{cards: len(cards)}
			for _, e := range entries {
				out.entries = append(out.entries, userstore.ListKey{AddedAt: e.AddedAt, MediaItemID: e.MediaItemID})
			}
			return out, err
		},
		"watchlist": func(h *PersonalDataHandler, after *userstore.ListKey, limit int) (page, error) {
			entries, cards, err := h.ListWatchlistPage(ctx, viewer, after, limit)
			out := page{cards: len(cards)}
			for _, e := range entries {
				out.entries = append(out.entries, userstore.ListKey{AddedAt: e.AddedAt, MediaItemID: e.MediaItemID})
			}
			return out, err
		},
		"not interested": func(h *PersonalDataHandler, after *userstore.ListKey, limit int) (page, error) {
			entries, cards, err := h.ListNotInterestedPage(ctx, viewer, after, limit)
			out := page{cards: len(cards)}
			for _, e := range entries {
				out.entries = append(out.entries, userstore.ListKey{AddedAt: e.AddedAt, MediaItemID: e.MediaItemID})
			}
			return out, err
		},
	}
	h := NewPersonalDataHandler(testUserStoreProvider{store: store}, items)
	for name, list := range lists {
		var got []string
		var after *userstore.ListKey
		for range 5 {
			p, err := list(h, after, 2)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if p.cards != len(p.entries) {
				t.Fatalf("%s: %d entries but %d cards", name, len(p.entries), p.cards)
			}
			for _, e := range p.entries {
				got = append(got, e.MediaItemID)
			}
			if len(p.entries) < 2 {
				break
			}
			last := p.entries[len(p.entries)-1]
			after = &last
		}
		if strings.Join(got, ",") != "movie:visible-1,movie:visible-2,movie:visible-3" {
			t.Errorf("%s: pages answered %v", name, got)
		}

		// A failed access check is a server error, not an empty page.
		var apiErr *APIError
		if _, err := list(NewPersonalDataHandler(testUserStoreProvider{store: store}, idsFailingItemRepo{items}), nil, 2); !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
			t.Errorf("%s: failed access check = %v, want 500", name, err)
		}
	}
}

// Favorites and the watchlist can hold episodes, and an episode gets a card
// when its series is visible. The list pages keep such entries by the same
// rule, against the real catalog and with both user stores (SQLite reads
// and checks in batches, PostgreSQL filters in one query): under a TV-only
// restriction the episode and its series are answered and the movie is
// skipped, and under a movies-only restriction only the movie is.
func TestPersonalListPagesKeepVisibleEpisodesDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	prefix := fmt.Sprintf("list-episodes-%d", time.Now().UnixNano())
	series, episode, movie := prefix+"-series", prefix+"-s1e1", prefix+"-movie"
	var tv, movies int
	for _, lib := range []struct {
		kind string
		id   *int
	}{{"series", &tv}, {"movies", &movies}} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ($1, $2, true) RETURNING id`, lib.kind, prefix+"-"+lib.kind).Scan(lib.id); err != nil {
			t.Fatalf("seed %s folder: %v", lib.kind, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM episodes WHERE content_id = $1`, episode)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{series, movie})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{tv, movies})
	})
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'series', 'Series'), ($2, 'movie', 'Movie')`, []any{series, movie}},
		{`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2), ($3, $4)`, []any{series, tv, movie, movies}},
		{`INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES ($1, $2, 1, 1, 'Pilot')`, []any{episode, series}},
	} {
		if _, err := pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed catalog: %v", err)
		}
	}

	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entries := []string{episode, movie, series} // newest first

	// SQLite user store: the lists are read and checked in batches.
	db, err := userdb.NewUserDB(filepath.Join(t.TempDir(), "user.db"), 1)
	if err != nil {
		t.Fatalf("open user database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, table := range []string{"favorites", "watchlist"} {
		for i, id := range entries {
			if _, err := db.DB.Exec(`INSERT INTO `+table+` (profile_id, media_item_id, added_at) VALUES ('p1', ?, ?)`,
				id, start.Add(-time.Duration(i)*time.Minute).Format(time.RFC3339)); err != nil {
				t.Fatalf("seed %s: %v", table, err)
			}
		}
	}

	// PostgreSQL user store: one query applies the access filter.
	var pgUser int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix).Scan(&pgUser); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, pgUser) })
	for _, table := range []string{"user_favorites", "user_watchlist"} {
		for i, id := range entries {
			if _, err := pool.Exec(ctx, `INSERT INTO `+table+` (user_id, profile_id, media_item_id, added_at) VALUES ($1, 'p1', $2, $3)`,
				pgUser, id, start.Add(-time.Duration(i)*time.Minute)); err != nil {
				t.Fatalf("seed %s: %v", table, err)
			}
		}
	}
	pgStore, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, pgUser)
	if err != nil {
		t.Fatalf("open postgres user store: %v", err)
	}

	for _, backend := range []struct {
		name   string
		userID int
		store  userstore.UserStore
	}{
		{"sqlite", 1, userdb.NewSQLiteUserStore(db.DB)},
		{"postgres", pgUser, pgStore},
	} {
		h := NewPersonalDataHandler(testUserStoreProvider{store: backend.store}, catalog.NewItemRepository(pool))
		h.SetEpisodeRepo(catalog.NewEpisodeRepository(pool))
		for _, tc := range []struct {
			name    string
			allowed []int
			want    string
		}{
			{"TV only", []int{tv}, episode + "," + series},
			{"movies only", []int{movies}, movie},
		} {
			viewer := PersonalListViewer{UserID: backend.userID, ProfileID: "p1", Access: catalog.AccessFilter{UserID: backend.userID, ProfileID: "p1", AllowedLibraryIDs: tc.allowed}}
			favorites, favCards, err := h.ListFavoritesPage(ctx, viewer, nil, 10)
			if err != nil {
				t.Fatalf("%s %s favorites: %v", backend.name, tc.name, err)
			}
			watchlist, watchCards, err := h.ListWatchlistPage(ctx, viewer, nil, 10)
			if err != nil {
				t.Fatalf("%s %s watchlist: %v", backend.name, tc.name, err)
			}
			var favIDs, watchIDs []string
			for _, e := range favorites {
				favIDs = append(favIDs, e.MediaItemID)
			}
			for _, e := range watchlist {
				watchIDs = append(watchIDs, e.MediaItemID)
			}
			if strings.Join(favIDs, ",") != tc.want || len(favCards) != len(favorites) {
				t.Errorf("%s %s favorites: entries %v, %d cards; want %s", backend.name, tc.name, favIDs, len(favCards), tc.want)
			}
			if strings.Join(watchIDs, ",") != tc.want || len(watchCards) != len(watchlist) {
				t.Errorf("%s %s watchlist: entries %v, %d cards; want %s", backend.name, tc.name, watchIDs, len(watchCards), tc.want)
			}
		}
	}
}
