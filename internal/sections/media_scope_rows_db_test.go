package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// seeAllSection answers the catalog's section lookup with one fixed row, as
// the page resolver does for a stored row.
type seeAllSection struct {
	sectionType SectionType
	config      json.RawMessage
}

func (s seeAllSection) ResolveCatalogSection(context.Context, catalog.CatalogRequest) (catalog.SectionDefinition, error) {
	return catalog.SectionDefinition{SectionType: string(s.sectionType), Title: "Row", ItemLimit: 100, Config: s.config}, nil
}

// A row scoped to a media type shows the titles its See All page lists, even
// when its library also holds titles of other types. Video keeps to movies
// and series, episode reads the episode catalog, and the book scopes keep to
// their one type. A favorites row keeps to the same titles among its entries,
// and an episode it lists counts only in a library that holds the episode.
func TestMediaScopeRowsMatchSeeAllPostgres(t *testing.T) {
	pool, prefix := mediaScopeTestPool(t)
	ctx := context.Background()
	// The row's library holds every type; elsewhere is a TV library holding
	// the series too, with an episode that only it has.
	library, elsewhere := seedScopeLibrary(t, pool, prefix, "movies"), seedScopeLibrary(t, pool, prefix, "series")
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix+"user").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })

	exec := seedExec(t, pool)
	for _, itemType := range []string{"movie", "series", "audiobook", "ebook", "manga"} {
		exec(`INSERT INTO media_items (content_id, type, title, status, genres, year) VALUES ($1, $2, $1, 'matched', '{}'::text[], 9999)`, prefix+itemType, itemType)
		exec(scopeMembershipSQL, prefix+itemType, library)
	}
	exec(scopeMembershipSQL, prefix+"series", elsewhere)
	episodes := []string{prefix + "episode-1", prefix + "episode-2"}
	seedScopeEpisodes(t, pool, prefix+"series", library, 1, episodes...)
	seedScopeEpisodes(t, pool, prefix+"series", elsewhere, 3, prefix+"episode-elsewhere")

	want := map[string][]string{
		"video":   {prefix + "movie", prefix + "series"},
		"episode": episodes,
		"ebook":   {prefix + "ebook"},
		"manga":   {prefix + "manga"},
	}
	// Configs a structured media_scope does not describe alone: a legacy
	// filter_type, and a stray legacy types list beside a media_scope.
	configs := map[string]json.RawMessage{
		"legacy video":     json.RawMessage(`{"filter_type":"video"}`),
		"video with types": json.RawMessage(`{"media_scope":"video","types":["audiobook"]}`),
	}
	want["legacy video"], want["video with types"] = want["video"], want["video"]
	// The profile favorites every title, including the episode the row's
	// library does not hold.
	profileID := prefix + "profile"
	provider := seedFavorites(t, pool, userID, profileID, prefix, "movie", "series", "audiobook", "ebook", "manga", "episode-1", "episode-2", "episode-elsewhere")

	fetcher := NewFetcher(pool)
	fetcher.StoreProvider = provider
	for _, scope := range []string{"video", "episode", "ebook", "manga", "legacy video", "video with types"} {
		for _, sectionType := range []SectionType{SectionRecentlyAdded, SectionRecentlyReleased, SectionRandom, SectionFavorites} {
			if scope == "video with types" && sectionType != SectionRecentlyAdded {
				continue // only Recently Added reads the legacy types list
			}
			t.Run(fmt.Sprintf("%s %s", sectionType, scope), func(t *testing.T) {
				config, ok := configs[scope]
				if !ok {
					config = defaultMediaScopeConfig(scope)
				}
				section := ResolvedSection{ID: "scoped", SectionType: sectionType, Title: "Row", ItemLimit: 100, Config: config}
				row, err := fetcher.FetchOne(ctx, section, &library, nil, userID, profileID, catalog.AccessFilter{})
				if err != nil {
					t.Fatalf("FetchOne: %v", err)
				}
				resolver := catalog.NewCatalogResolver(catalog.NewBrowseRepository(pool), catalog.NewItemRepository(pool)).
					WithSectionResolver(seeAllSection{sectionType: sectionType, config: config}).
					WithUserStoreProvider(provider)
				page, err := resolver.Resolve(ctx, catalog.CatalogRequest{
					Source: catalog.CatalogSourceSection, Scope: "library", LibraryID: library, SectionID: "scoped", Limit: 100, CursorPaging: true,
				}, catalog.AccessFilter{UserID: userID, ProfileID: profileID})
				if err != nil {
					t.Fatalf("resolve See All: %v", err)
				}
				rowIDs, seeAllIDs := sortedContentIDs(row.Items), sortedContentIDs(page.Items)
				if !slices.Equal(seeAllIDs, want[scope]) {
					t.Fatalf("See All = %v, want %v", seeAllIDs, want[scope])
				}
				if !slices.Equal(rowIDs, seeAllIDs) {
					t.Errorf("row = %v, See All = %v", rowIDs, seeAllIDs)
				}
			})
		}
	}

	// A Recently Added row over the TV library and the mixed one merges two
	// reads; the video scope holds on the mixed side too.
	mixed := ResolvedSection{ID: "mixed", SectionType: SectionRecentlyAdded, Title: "Row", ItemLimit: 100,
		Config: json.RawMessage(fmt.Sprintf(`{"media_scope":"video","library_ids":[%d,%d]}`, library, elsewhere))}
	row, err := fetcher.FetchOne(ctx, mixed, nil, nil, userID, profileID, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("FetchOne mixed: %v", err)
	}
	got := sortedContentIDs(row.Items)
	if !slices.Contains(got, prefix+"movie") || slices.ContainsFunc(got, func(id string) bool {
		return id == prefix+"audiobook" || id == prefix+"ebook" || id == prefix+"manga"
	}) {
		t.Errorf("video row over TV and mixed libraries = %v, want the movie and no books", got)
	}
}

// An episode row longer than one catalog cursor page reads several pages.
func TestEpisodeScopeRowReadsPastOneCursorPagePostgres(t *testing.T) {
	pool, prefix := mediaScopeTestPool(t)
	library := seedScopeLibrary(t, pool, prefix, "series")
	exec := seedExec(t, pool)
	exec(`INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'series', $1, 'matched', '{}'::text[])`, prefix+"series")
	exec(scopeMembershipSQL, prefix+"series", library)
	episodes := make([]string, 202)
	for i := range episodes {
		episodes[i] = fmt.Sprintf("%sepisode-%03d", prefix, i)
	}
	seedScopeEpisodes(t, pool, prefix+"series", library, 1, episodes...)

	section := ResolvedSection{ID: "long", SectionType: SectionRecentlyReleased, Title: "Row", ItemLimit: 201, Config: defaultMediaScopeConfig("episode")}
	row, err := NewFetcher(pool).FetchOne(context.Background(), section, &library, nil, 0, "", catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("FetchOne: %v", err)
	}
	distinct := slices.Compact(sortedContentIDs(row.Items))
	if len(row.Items) != 201 || len(distinct) != 201 || row.TotalCount <= 201 {
		t.Fatalf("row = %d distinct of %d items, total %d; want 201 distinct episodes and more beyond", len(distinct), len(row.Items), row.TotalCount)
	}
}

const scopeMembershipSQL = `INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, NOW())`

// mediaScopeTestPool connects to the test database and returns a unique
// content ID prefix; seeded titles and episodes under it are removed after
// the test.
func mediaScopeTestPool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	prefix := "media-scope-" + uuid.NewString()[:8] + "-"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM episodes WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		pool.Close()
	})
	return pool, prefix
}

func seedScopeLibrary(t *testing.T, pool *pgxpool.Pool, prefix, libraryType string) int {
	t.Helper()
	var id int
	if err := pool.QueryRow(context.Background(), `INSERT INTO media_folders (type, name, enabled) VALUES ($1, $2 || gen_random_uuid()::text, true) RETURNING id`, libraryType, prefix).Scan(&id); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, id) })
	return id
}

// seedScopeEpisodes adds playable episodes of series in library, numbered
// from first and aired one day apart in the given order.
func seedScopeEpisodes(t *testing.T, pool *pgxpool.Pool, series string, library, first int, ids ...string) {
	t.Helper()
	exec := seedExec(t, pool)
	for i, id := range ids {
		exec(`INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, overview, runtime, rating_imdb, air_date)
			VALUES ($1, $2, 1, $3, 'Episode', '', 0, 0, DATE '2020-01-01' + $3::int)`, id, series, first+i)
		exec(`INSERT INTO episode_libraries (episode_id, media_folder_id) VALUES ($1, $2)`, id, library)
		exec(`INSERT INTO media_files (content_id, episode_id, media_folder_id, file_path) VALUES ($1, $2, $3, $2 || '.mkv')`, series, id, library)
	}
}

func seedExec(t *testing.T, pool *pgxpool.Pool) func(string, ...any) {
	return func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

// seedFavorites creates profileID on userID's store and favorites each
// prefix+id.
func seedFavorites(t *testing.T, pool *pgxpool.Pool, userID int, profileID, prefix string, ids ...string) userstore.UserStoreProvider {
	t.Helper()
	ctx := context.Background()
	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatalf("user store: %v", err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: "Scope"}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	for _, id := range ids {
		if _, err := store.AddFavorite(ctx, profileID, prefix+id); err != nil {
			t.Fatalf("seed favorite: %v", err)
		}
	}
	return provider
}

func sortedContentIDs(items []*models.MediaItem) []string {
	ids := contentIDs(items)
	slices.Sort(ids)
	return ids
}
