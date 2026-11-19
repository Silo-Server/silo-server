package handlers

import (
	"context"
	"fmt"
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
	tv, movies := seedListEpisodeCatalog(t, pool, series, episode, movie)
	// Newest first, on Favorites and the watchlist of both user stores.
	sqliteStore, pgUser, pgStore := seedListEntries(t, pool, prefix, []string{episode, movie, series})

	for _, backend := range []struct {
		name   string
		userID int
		store  userstore.UserStore
	}{
		{"sqlite", 1, sqliteStore},
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

// seedListEpisodeCatalog adds a TV library holding a series with one
// episode, and a movie library holding a movie, and answers the two library
// IDs.
func seedListEpisodeCatalog(t *testing.T, pool *pgxpool.Pool, series, episode, movie string) (tv, movies int) {
	t.Helper()
	ctx := context.Background()
	for _, lib := range []struct {
		kind string
		id   *int
	}{{"series", &tv}, {"movies", &movies}} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ($1, $2, true) RETURNING id`, lib.kind, series+"-"+lib.kind).Scan(lib.id); err != nil {
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
	return tv, movies
}

// seedListEntries puts the items, newest first, on profile p1's Favorites
// and watchlist in a SQLite user store and in the PostgreSQL user store of
// a new account, and answers both stores and that account's ID.
func seedListEntries(t *testing.T, pool *pgxpool.Pool, username string, ids []string) (sqliteStore userstore.UserStore, pgUser int, pgStore userstore.UserStore) {
	t.Helper()
	ctx := context.Background()
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	db, err := userdb.NewUserDB(filepath.Join(t.TempDir(), "user.db"), 1)
	if err != nil {
		t.Fatalf("open user database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, table := range []string{"favorites", "watchlist"} {
		for i, id := range ids {
			if _, err := db.DB.Exec(`INSERT INTO `+table+` (profile_id, media_item_id, added_at) VALUES ('p1', ?, ?)`,
				id, start.Add(-time.Duration(i)*time.Minute).Format(time.RFC3339)); err != nil {
				t.Fatalf("seed %s: %v", table, err)
			}
		}
	}

	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, username).Scan(&pgUser); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, pgUser) })
	for _, table := range []string{"user_favorites", "user_watchlist"} {
		for i, id := range ids {
			if _, err := pool.Exec(ctx, `INSERT INTO `+table+` (user_id, profile_id, media_item_id, added_at) VALUES ($1, 'p1', $2, $3)`,
				pgUser, id, start.Add(-time.Duration(i)*time.Minute)); err != nil {
				t.Fatalf("seed %s: %v", table, err)
			}
		}
	}
	pgStore, err = pgstore.NewPostgresProvider(pool).ForUser(ctx, pgUser)
	if err != nil {
		t.Fatalf("open postgres user store: %v", err)
	}
	return userdb.NewSQLiteUserStore(db.DB), pgUser, pgStore
}
