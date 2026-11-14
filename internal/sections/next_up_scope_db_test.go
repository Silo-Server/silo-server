package sections

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// A series outside the row's library or the viewer's access gives its place
// in Next Up to the next eligible series, so a row shorter than its limit
// means no more series in scope are eligible.
func TestNextUpOutOfScopeSeriesKeepTheRowFullPostgres(t *testing.T) {
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

	prefix := "nextup-scope-" + uuid.NewString()[:8]
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	folders := make([]int, 2)
	for i := range folders {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type,name,enabled) VALUES ('series',$1,true) RETURNING id`,
			fmt.Sprintf("%s-%d", prefix, i)).Scan(&folders[i]); err != nil {
			t.Fatal(err)
		}
	}
	libraryA, libraryB := folders[0], folders[1]
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username,role) VALUES ($1,'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id=$1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM media_files WHERE media_folder_id = ANY($1)`, folders)
		_, _ = pool.Exec(bg, `DELETE FROM episodes WHERE series_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(bg, `DELETE FROM media_item_libraries WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(bg, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(bg, `DELETE FROM media_folders WHERE id = ANY($1)`, folders)
	})
	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	profileID := prefix + "-profile"
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: "Scope"}); err != nil {
		t.Fatal(err)
	}

	// Three series with episode 1 watched, newest first; Next Up offers
	// episode 2 of each. The newest is in library B and rated 18, the other
	// two are in library A and unrated.
	start := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	next := make([]string, 3)
	for i := range next {
		series := fmt.Sprintf("%s-series-%d", prefix, i)
		folder, age := libraryA, any(nil)
		if i == 0 {
			folder, age = libraryB, 18
		}
		exec(`INSERT INTO media_items (content_id,type,title,content_rating_age) VALUES ($1,'series',$1,$2)`, series, age)
		exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, series, folder)
		for ep := 1; ep <= 2; ep++ {
			id := fmt.Sprintf("%s-e%d", series, ep)
			exec(`INSERT INTO episodes (content_id,series_id,season_number,episode_number,title,overview,runtime) VALUES ($1,$2,1,$3,$1,'',0)`, id, series, ep)
			exec(`INSERT INTO media_files (episode_id,media_folder_id,file_path) VALUES ($1,$2,$3)`, id, folder, "/"+id)
		}
		exec(`INSERT INTO user_watch_progress (user_id,profile_id,media_item_id,position_seconds,completed,updated_at) VALUES ($1,$2,$3,0,true,$4)`,
			userID, profileID, series+"-e1", start.Add(-time.Duration(i)*time.Hour))
		next[i] = series + "-e2"
	}

	fetcher := NewFetcher(pool)
	fetcher.StoreProvider = provider
	fetcher.NextUpRepo = catalog.NewNextUpRepository(pool, provider)
	for _, tc := range []struct {
		name      string
		libraryID *int
		filter    catalog.AccessFilter
	}{
		{name: "other library", libraryID: &libraryA},
		{name: "above rating ceiling", filter: catalog.AccessFilter{MaturityLimits: access.MaturityLimits{
			MaxContentRating: "PG-13", AllowUnratedContent: true,
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items, _, err := fetcher.FetchNextUpItems(ctx, userID, profileID, tc.libraryID, nil, tc.filter, 2)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, len(items))
			for i, item := range items {
				got[i] = item.ContentID
			}
			if want := next[1:]; !slices.Equal(got, want) {
				t.Fatalf("Next Up with limit 2 = %v, want %v", got, want)
			}
		})
	}
}
