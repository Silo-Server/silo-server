package catalog

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type providerAliasFixture struct {
	pool     *pgxpool.Pool
	repo     *ItemRepository
	prefix   string
	enabled  int
	disabled int
}

func newProviderAliasFixture(t *testing.T) *providerAliasFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := &providerAliasFixture{pool: pool, repo: NewItemRepository(pool), prefix: fmt.Sprintf("alias-%d", time.Now().UnixNano())}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('mixed', $1, TRUE) RETURNING id`, f.prefix+"-on").Scan(&f.enabled); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('mixed', $1, FALSE) RETURNING id`, f.prefix+"-off").Scan(&f.disabled); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, f.prefix+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{f.enabled, f.disabled})
	})
	return f
}

// item inserts a catalog item in the given folder (0 for none) and returns
// its content ID.
func (f *providerAliasFixture) item(t *testing.T, suffix, mediaType, tmdbID, imdbID, tvdbID string, folder int) string {
	t.Helper()
	ctx := t.Context()
	contentID := f.prefix + "-" + suffix
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, genres, tmdb_id, imdb_id, tvdb_id)
		VALUES ($1, $2, $1, '{}'::text[], $3, $4, $5)`, contentID, mediaType, tmdbID, imdbID, tvdbID); err != nil {
		t.Fatal(err)
	}
	if folder != 0 {
		if _, err := f.pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, contentID, folder); err != nil {
			t.Fatal(err)
		}
	}
	return contentID
}

func TestResolveProviderAliasesDB(t *testing.T) {
	f := newProviderAliasFixture(t)
	ctx := t.Context()
	p := f.prefix
	// Unique provider values per run keep parallel fixtures apart.
	v := func(s string) string { return p + "-" + s }

	byTMDB := f.item(t, "tmdb", "movie", v("tmdb-1"), "", "", f.enabled)
	byIMDb := f.item(t, "imdb", "movie", "", v("tt-2"), "", f.enabled)
	byTVDB := f.item(t, "tvdb", "series", "", "", v("tvdb-3"), f.enabled)
	byProviderRow := f.item(t, "prov", "series", "", "", "", f.enabled)
	if _, err := f.pool.Exec(ctx, `INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id) VALUES ($1, 'series', 'tmdb', $2)`, byProviderRow, v("tmdb-4")); err != nil {
		t.Fatal(err)
	}
	byStale := f.item(t, "stale", "movie", v("tmdb-5-new"), "", "", f.enabled)
	if _, err := f.pool.Exec(ctx, `INSERT INTO stale_media_ids (content_id, provider, provider_id) VALUES ($1, 'tmdb', $2)`, byStale, v("tmdb-5-dead")); err != nil {
		t.Fatal(err)
	}
	dupA := f.item(t, "dup-a", "movie", v("tmdb-6"), "", "", f.enabled)
	dupB := f.item(t, "dup-b", "movie", "", v("tt-6"), "", f.enabled)
	f.item(t, "disabled", "movie", v("tmdb-7"), "", "", f.disabled)
	f.item(t, "unlinked", "movie", v("tmdb-8"), "", "", 0)
	f.item(t, "series-not-movie", "series", v("tmdb-9"), "", "", f.enabled)
	// One item reached through two aliases counts once.
	twice := f.item(t, "twice", "movie", v("tmdb-10"), v("tt-10"), "", f.enabled)
	// An item in a disabled folder and an enabled one still resolves.
	both := f.item(t, "both", "movie", v("tmdb-11"), "", "", f.disabled)
	if _, err := f.pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, both, f.enabled); err != nil {
		t.Fatal(err)
	}

	aliases := []ProviderAlias{
		{Key: 1, MediaType: "movie", Provider: "tmdb", ProviderID: v("tmdb-1")},
		{Key: 2, MediaType: "movie", Provider: "imdb", ProviderID: v("tt-2")},
		{Key: 3, MediaType: "series", Provider: "tvdb", ProviderID: v("tvdb-3")},
		{Key: 4, MediaType: "series", Provider: "tmdb", ProviderID: v("tmdb-4")},
		{Key: 5, MediaType: "movie", Provider: "tmdb", ProviderID: v("tmdb-5-dead")},
		{Key: 6, MediaType: "movie", Provider: "tmdb", ProviderID: v("tmdb-6")},
		{Key: 6, MediaType: "movie", Provider: "imdb", ProviderID: v("tt-6")},
		{Key: 7, MediaType: "movie", Provider: "tmdb", ProviderID: v("tmdb-7")},
		{Key: 8, MediaType: "movie", Provider: "tmdb", ProviderID: v("tmdb-8")},
		{Key: 9, MediaType: "movie", Provider: "tmdb", ProviderID: v("tmdb-9")},
		{Key: 10, MediaType: "movie", Provider: "tmdb", ProviderID: v("tmdb-10")},
		{Key: 10, MediaType: "movie", Provider: "imdb", ProviderID: v("tt-10")},
		{Key: 11, MediaType: "movie", Provider: "tmdb", ProviderID: v("tmdb-11")},
		{Key: 12, MediaType: "movie", Provider: "tmdb", ProviderID: v("missing")},
		{Key: 13, MediaType: "movie", Provider: "trakt", ProviderID: v("tmdb-1")},
	}
	got, err := f.repo.ResolveProviderAliases(ctx, aliases)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64][]string{
		1:  {byTMDB},
		2:  {byIMDb},
		3:  {byTVDB},
		4:  {byProviderRow},
		5:  {byStale},
		6:  {dupA, dupB},
		10: {twice},
		11: {both},
	}
	if len(got) != len(want) {
		t.Fatalf("resolved keys = %v, want %v", got, want)
	}
	for key, ids := range want {
		if !slices.Equal(got[key], ids) {
			t.Errorf("key %d resolved to %v, want %v", key, got[key], ids)
		}
	}

	empty, err := f.repo.ResolveProviderAliases(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("no aliases = %v, %v", empty, err)
	}

	mediaType, itemAliases, err := f.repo.ItemProviderAliases(ctx, byStale)
	if err != nil {
		t.Fatal(err)
	}
	gotAliases := make([]string, 0, len(itemAliases))
	for _, a := range itemAliases {
		gotAliases = append(gotAliases, a.Provider+":"+a.ProviderID)
	}
	wantAliases := []string{"tmdb:" + v("tmdb-5-dead"), "tmdb:" + v("tmdb-5-new")}
	if mediaType != "movie" || !slices.Equal(gotAliases, wantAliases) {
		t.Fatalf("ItemProviderAliases = %q %v, want movie %v", mediaType, gotAliases, wantAliases)
	}
	if mediaType, itemAliases, err := f.repo.ItemProviderAliases(ctx, f.prefix+"-absent"); err != nil || mediaType != "" || len(itemAliases) != 0 {
		t.Fatalf("absent item = %q %v %v", mediaType, itemAliases, err)
	}
}
